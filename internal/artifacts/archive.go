package artifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/ids"
)

// Names under <SupportDir>/artifacts.
const (
	DirName       = "artifacts"
	LockName      = "artifacts.lock"
	fileManifest  = "manifest.json"
	filePrevFmt   = "manifest.prev-%d.json"
	fileChain     = "chain.json"
	fileDue       = "due.json"
	dirBlobs      = "blobs"
	dirOutbox     = "outbox"
	dirInventory  = "inventory"
	dirPartial    = "partial"
	dirTmp        = ".tmp"
	keepPrev      = 3
	blobGCAge     = time.Hour
	partialGCAge  = 24 * time.Hour
	defaultBlobSf = ".html.txt"
	defaultOutSf  = ".html"
)

// Errors of the archive.
var (
	ErrBlobCorrupt = errors.New("artifacts: a blob does not match its checksum")
	ErrTooLarge    = errors.New("artifacts: file too large")
	ErrSymlink     = errors.New("artifacts: refusing a symlink or special file")
	ErrBadName     = errors.New("artifacts: invalid file name")
)

// Archive is <SupportDir>/artifacts. Reads go to the filesystem directly
// (never following a symlink); writes go through FS.
type Archive struct {
	Root string
	FS   fsx.Ops
	Spec compat.ArtifactsSpec
	// Lock takes <SupportDir>/locks/artifacts.lock for up to wait (nil:
	// no locking).
	Lock func(ctx context.Context, wait time.Duration) (release func(), err error)
	Rand io.Reader
	// BeforeCommit, when set, runs between the blob writes and the
	// manifest commit (fault-injection tests).
	BeforeCommit func() error
}

// NewArchive returns the archive under supportDir.
func NewArchive(supportDir string, ops fsx.Ops, spec compat.ArtifactsSpec) *Archive {
	return &Archive{Root: filepath.Join(supportDir, DirName), FS: ops, Spec: spec}
}

// Paths returns the archive's absolute paths.
func (a *Archive) Paths() ArchivePaths {
	return ArchivePaths{Root: a.Root, Blobs: filepath.Join(a.Root, dirBlobs), Outbox: filepath.Join(a.Root, dirOutbox)}
}

func (a *Archive) blobSuffix() string {
	if a.Spec.BlobSuffix != "" {
		return a.Spec.BlobSuffix
	}
	return defaultBlobSf
}

func (a *Archive) outboxSuffix() string {
	if a.Spec.OutboxSuffix != "" {
		return a.Spec.OutboxSuffix
	}
	return defaultOutSf
}

// Locked runs f under the artifacts lock.
func (a *Archive) Locked(ctx context.Context, wait time.Duration, f func() error) error {
	if a.Lock == nil {
		return f()
	}
	release, err := a.Lock(ctx, wait)
	if err != nil {
		return err
	}
	defer release()
	return f()
}

// checkDirs Lstats the root and every directory between it and rel's
// parent: each that exists must be a real directory.
func (a *Archive) checkDirs(rel string) error {
	p := a.Root
	if fi, err := os.Lstat(p); err == nil && !fi.IsDir() {
		return ErrSymlink
	}
	dir := filepath.Dir(rel)
	if dir == "." {
		return nil
	}
	for _, part := range strings.Split(dir, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return ErrSymlink
		}
	}
	return nil
}

// read reads root/rel (at most max bytes), never following a symlink;
// the open is O_NONBLOCK so a FIFO fails the regular-file check instead
// of blocking.
// A missing file is (nil, nil).
func (a *Archive) read(rel string, max int64) ([]byte, error) {
	p, err := fsx.Contained(a.Root, rel)
	if err != nil {
		return nil, err
	}
	if err := a.checkDirs(rel); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrSymlink
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, ErrSymlink
	}
	if fi.Size() > max {
		return nil, ErrTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrTooLarge
	}
	return b, nil
}

// ensureRoot creates the root 0700 (a symlink is refused).
func (a *Archive) ensureRoot() error {
	fi, err := os.Lstat(a.Root)
	switch {
	case err == nil && !fi.IsDir():
		return ErrSymlink
	case err == nil:
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	return a.FS.MkdirAll(a.Root, 0o700)
}

// write replaces root/rel atomically (0600).
func (a *Archive) write(rel string, data []byte) error {
	if err := a.ensureRoot(); err != nil {
		return err
	}
	// The temp folder is Lstat'ed like every other one (4.1): a symlinked
	// .tmp would have the temp files written wherever it points.
	if err := a.ensureDir(dirTmp); err != nil {
		return err
	}
	if err := a.checkDirs(rel); err != nil {
		return err
	}
	return a.FS.WriteFileAtomic(a.Root, rel, data, 0o600, filepath.Join(a.Root, dirTmp))
}

// ensureDir creates root/rel 0700.
func (a *Archive) ensureDir(rel string) error {
	if err := a.ensureRoot(); err != nil {
		return err
	}
	if err := a.checkDirs(filepath.Join(rel, "x")); err != nil {
		return err
	}
	p, err := fsx.Contained(a.Root, rel)
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(p); err == nil {
		if !fi.IsDir() {
			return ErrSymlink
		}
		return nil
	}
	return a.FS.MkdirAll(p, 0o700)
}

func (a *Archive) randHex(n int) (string, error) {
	b := make([]byte, n)
	r := a.Rand
	if r == nil {
		r = rand.Reader
	}
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// createExcl creates root/rel with data unless it exists (fs.ErrExist).
func (a *Archive) createExcl(rel string, data []byte) error {
	if err := a.ensureDir(filepath.Dir(rel)); err != nil {
		return err
	}
	name, err := a.randHex(8)
	if err != nil {
		return err
	}
	tmpRel := filepath.Join(dirTmp, "x-"+name)
	if err := a.write(tmpRel, data); err != nil {
		return err
	}
	from := filepath.Join(a.Root, tmpRel)
	to, err := fsx.Contained(a.Root, rel)
	if err != nil {
		return err
	}
	if err := a.FS.RenameExcl(from, to); err != nil {
		_ = a.FS.RemoveAll(a.Root, tmpRel)
		return err
	}
	return nil
}

// remove removes root/rel (missing is fine).
func (a *Archive) remove(rel string) error {
	p, err := fsx.Contained(a.Root, rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return a.FS.RemoveAll(a.Root, rel)
}

// list lists the names in root/rel (nil when missing or not a directory).
func (a *Archive) list(rel string) []os.DirEntry {
	p := a.Root
	if rel != "." {
		var err error
		if p, err = fsx.Contained(a.Root, rel); err != nil {
			return nil
		}
	}
	if err := a.checkDirs(filepath.Join(rel, "x")); err != nil {
		return nil
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.IsDir() {
		return nil
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		return nil
	}
	return ents
}

// Exists reports whether the archive directory exists.
func (a *Archive) Exists() bool {
	fi, err := os.Lstat(a.Root)
	return err == nil && fi.IsDir()
}

// ---------------------------------------------------------------- manifest

// LoadManifest reads manifest.json; nil without one.
func (a *Archive) LoadManifest() (*Manifest, error) {
	b, err := a.read(fileManifest, MaxManifestBytes)
	if err != nil || b == nil {
		return nil, err
	}
	return Decode(b)
}

// PrevManifests returns the seqs of the kept previous manifests, newest
// first.
func (a *Archive) PrevManifests() []uint64 {
	var out []uint64
	for _, e := range a.list(".") {
		var n uint64
		if _, err := fmt.Sscanf(e.Name(), filePrevFmt, &n); err == nil && e.Name() == fmt.Sprintf(filePrevFmt, n) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// CommitManifest writes m as the next manifest: the current one is kept
// as manifest.prev-<seq>.json (the newest three are kept).
func (a *Archive) CommitManifest(m *Manifest, now time.Time) error {
	if a.BeforeCommit != nil {
		if err := a.BeforeCommit(); err != nil {
			return err
		}
	}
	old, err := a.read(fileManifest, MaxManifestBytes)
	if err != nil {
		return err
	}
	var prevSeq uint64
	if old != nil {
		var head struct {
			Seq uint64 `json:"seq"`
		}
		_ = json.Unmarshal(old, &head)
		prevSeq = head.Seq
		if err := a.write(fmt.Sprintf(filePrevFmt, prevSeq), old); err != nil {
			return err
		}
	}
	m.V = ManifestVersion
	if m.Seq <= prevSeq {
		m.Seq = prevSeq + 1
	}
	m.UpdatedAt = now.UTC()
	b, err := Encode(m)
	if err != nil {
		return err
	}
	if err := a.write(fileManifest, b); err != nil {
		return err
	}
	for i, n := range a.PrevManifests() {
		if i >= keepPrev {
			_ = a.remove(fmt.Sprintf(filePrevFmt, n))
		}
	}
	return nil
}

// ---------------------------------------------------------------- blobs

func (a *Archive) blobRel(sha string) (string, error) {
	if !ValidSHA(sha) {
		return "", ErrBadName
	}
	return filepath.Join(dirBlobs, sha+a.blobSuffix()), nil
}

func shaHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// PutBlob writes a blob with an exclusive create; an existing blob must
// hash to sha (else ErrBlobCorrupt).
func (a *Archive) PutBlob(sha string, content []byte) error {
	if shaHex(content) != sha {
		return ErrBlobCorrupt
	}
	rel, err := a.blobRel(sha)
	if err != nil {
		return err
	}
	err = a.createExcl(rel, content)
	if errors.Is(err, fs.ErrExist) {
		b, rerr := a.read(rel, a.maxContent())
		if rerr != nil || b == nil || shaHex(b) != sha {
			return ErrBlobCorrupt
		}
		return nil
	}
	return err
}

func (a *Archive) maxContent() int64 {
	if a.Spec.MaxContentBytes > 0 {
		return a.Spec.MaxContentBytes
	}
	return 16 << 20
}

// ReadBlob returns a blob's content after checking its hash.
func (a *Archive) ReadBlob(sha string) (string, error) {
	rel, err := a.blobRel(sha)
	if err != nil {
		return "", err
	}
	b, err := a.read(rel, a.maxContent())
	if err != nil {
		return "", err
	}
	if b == nil {
		return "", fs.ErrNotExist
	}
	if shaHex(b) != sha {
		return "", ErrBlobCorrupt
	}
	return string(b), nil
}

// HasBlob reports whether a blob file exists (not re-hashed).
func (a *Archive) HasBlob(sha string) bool {
	rel, err := a.blobRel(sha)
	if err != nil {
		return false
	}
	p := filepath.Join(a.Root, rel)
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// ---------------------------------------------------------------- inventories

func inventoryRel(alias ids.Alias) (string, error) {
	if !validAlias(alias) {
		return "", ErrBadName
	}
	return filepath.Join(dirInventory, string(alias)+".json"), nil
}

// LoadInventory reads a profile's inventory (nil without one).
func (a *Archive) LoadInventory(alias ids.Alias) (*Inventory, error) {
	rel, err := inventoryRel(alias)
	if err != nil {
		return nil, err
	}
	b, err := a.read(rel, MaxInventoryBytes)
	if err != nil || b == nil {
		return nil, err
	}
	var inv Inventory
	if err := json.Unmarshal(b, &inv); err != nil {
		return nil, err
	}
	if inv.V != 1 || inv.Profile != alias {
		return nil, errors.New("artifacts: inventory does not belong to the profile")
	}
	return &inv, nil
}

// SaveInventory replaces a profile's inventory.
func (a *Archive) SaveInventory(inv *Inventory) error {
	rel, err := inventoryRel(inv.Profile)
	if err != nil {
		return err
	}
	b, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	return a.write(rel, b)
}

// RemoveInventory removes a profile's inventory.
func (a *Archive) RemoveInventory(alias ids.Alias) error {
	rel, err := inventoryRel(alias)
	if err != nil {
		return err
	}
	return a.remove(rel)
}

// ---------------------------------------------------------------- chain, due

// LoadChain reads chain.json (nil without one).
func (a *Archive) LoadChain() (*Chain, error) {
	b, err := a.read(fileChain, MaxChainBytes)
	if err != nil || b == nil {
		return nil, err
	}
	var c Chain
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// SaveChain replaces chain.json.
func (a *Archive) SaveChain(c *Chain) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if len(b) > MaxChainBytes {
		return ErrTooLarge
	}
	return a.write(fileChain, b)
}

// DueFile is due.json (4.6).
type DueFile struct {
	V       int       `json:"v"`
	Seq     uint64    `json:"seq"`
	Profile ids.Alias `json:"profile"`
	Epoch   uint64    `json:"epoch"`
	Count   int       `json:"count"`
}

// LoadDue reads due.json (nil without one or when it is malformed).
func (a *Archive) LoadDue() *DueFile {
	b, err := a.read(fileDue, MaxDueBytes)
	if err != nil || b == nil {
		return nil
	}
	var d DueFile
	if json.Unmarshal(b, &d) != nil || d.V != 1 || !validAlias(d.Profile) || d.Count < 0 {
		return nil
	}
	return &d
}

// SaveDue replaces due.json.
func (a *Archive) SaveDue(d DueFile) error {
	d.V = 1
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return a.write(fileDue, b)
}

// ---------------------------------------------------------------- partial chunks

// partialDir is partial/<slug>/<version key>. The version key is the
// sha256 hex of the version id (64 characters): a hex of the id itself
// could reach 256 characters, above the file name limit (255) of APFS
// and ext4. The version is kept inside each chunk file.
func partialDir(s Slug, v VersionID) (string, error) {
	if !ValidSlug(string(s)) || v == "" || len(v) > 128 {
		return "", ErrBadName
	}
	return filepath.Join(dirPartial, string(s), versionKey(v)), nil
}

func versionKey(v VersionID) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

// Partials returns the recorded chunks per slug (one version each).
func (a *Archive) Partials() map[Slug]PartialState {
	out := map[Slug]PartialState{}
	for _, se := range a.list(dirPartial) {
		s := Slug(se.Name())
		if !ValidSlug(string(s)) {
			continue
		}
		for _, ve := range a.list(filepath.Join(dirPartial, se.Name())) {
			if !ValidSHA(ve.Name()) {
				continue
			}
			ps := PartialState{}
			for _, ce := range a.list(filepath.Join(dirPartial, se.Name(), ve.Name())) {
				name, ok := strings.CutSuffix(ce.Name(), ".json")
				if !ok || len(name) > 9 {
					continue
				}
				if _, err := strconv.Atoi(name); err != nil {
					continue
				}
				b, err := a.read(filepath.Join(dirPartial, se.Name(), ve.Name(), ce.Name()), MaxPartialBytes)
				if err != nil || b == nil {
					continue
				}
				var c Chunk
				if json.Unmarshal(b, &c) != nil || c.Slug != s || c.Version == "" || len(c.Version) > 128 ||
					versionKey(c.Version) != ve.Name() || (ps.Version != "" && c.Version != ps.Version) {
					continue
				}
				ps.Version = c.Version
				ps.Chunks = append(ps.Chunks, c)
			}
			sort.Slice(ps.Chunks, func(i, j int) bool { return ps.Chunks[i].Offset < ps.Chunks[j].Offset })
			if len(ps.Chunks) > 0 {
				out[s] = ps
			}
		}
	}
	return out
}

// PutPartial records one chunk.
func (a *Archive) PutPartial(c Chunk) error {
	dir, err := partialDir(c.Slug, c.Version)
	if err != nil {
		return err
	}
	if c.Offset < 1 || c.Offset > 999_999_999 {
		return ErrBadName
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if len(b) > MaxPartialBytes {
		return ErrTooLarge
	}
	return a.write(filepath.Join(dir, strconv.Itoa(c.Offset)+".json"), b)
}

// DropPartial removes a slug's recorded chunks.
func (a *Archive) DropPartial(s Slug) error {
	if !ValidSlug(string(s)) {
		return ErrBadName
	}
	return a.remove(filepath.Join(dirPartial, string(s)))
}

// ---------------------------------------------------------------- GC

// referencedBlobs are the blobs a manifest still needs.
func referencedBlobs(m *Manifest) map[string]bool {
	ref := map[string]bool{}
	add := func(ps map[PairID]*Pair) {
		for _, p := range ps {
			if p.Content != nil {
				ref[p.Content.SHA256] = true
			}
			for _, h := range p.History {
				ref[h.SHA256] = true
			}
		}
	}
	if m != nil {
		add(m.Pairs)
		add(m.Forgotten)
	}
	return ref
}

// GC removes unreferenced blobs and outbox files older than an hour,
// misnamed outbox files, partial sets older than a day or of slugs the
// live chain does not read, and stale temp files.
func (a *Archive) GC(m *Manifest, live *Chain, now time.Time) error {
	ref := referencedBlobs(m)
	var errs []error
	old := func(e os.DirEntry, age time.Duration) bool {
		fi, err := e.Info()
		return err != nil || now.Sub(fi.ModTime()) > age
	}
	for _, e := range a.list(dirBlobs) {
		sha, ok := strings.CutSuffix(e.Name(), a.blobSuffix())
		if ok && ValidSHA(sha) && ref[sha] {
			continue
		}
		if old(e, blobGCAge) || !ok || !ValidSHA(sha) {
			errs = append(errs, a.remove(filepath.Join(dirBlobs, e.Name())))
		}
	}
	for _, e := range a.list(dirOutbox) {
		sha, ok := strings.CutSuffix(e.Name(), a.outboxSuffix())
		if !ok || !ValidSHA(sha) || old(e, blobGCAge) {
			errs = append(errs, a.remove(filepath.Join(dirOutbox, e.Name())))
		}
	}
	for _, e := range a.list(dirPartial) {
		s := Slug(e.Name())
		keep := live != nil && !live.Terminal() && live.State != "" && ValidSlug(string(s)) && !old(e, partialGCAge)
		if keep {
			_, cand := live.Candidates[s]
			pub := false
			for _, p := range live.Published {
				pub = pub || p.Slug == s
			}
			keep = cand || pub
		}
		if !keep {
			errs = append(errs, a.remove(filepath.Join(dirPartial, e.Name())))
		}
	}
	for _, e := range a.list(dirTmp) {
		if old(e, time.Minute) {
			errs = append(errs, a.remove(filepath.Join(dirTmp, e.Name())))
		}
	}
	return errors.Join(errs...)
}

// RemoveAll removes the whole archive (uninstall --keep-artifacts=false).
func RemoveAll(ops fsx.Ops, supportDir string) error {
	p := filepath.Join(supportDir, DirName)
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return ops.RemoveAll(supportDir, DirName)
}
