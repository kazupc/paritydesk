package scratch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources"
)

// Note constants (model.Change.Note of ChangeSkipped changes of row
// scratch). Keys are given per constant.
const (
	NoteOversizeFiles = "oversize-files"       // key: WorkspaceKey
	NoteOversizeBytes = "oversize-bytes"       // key: WorkspaceKey
	NoteBudget        = "budget"               // key: WorkspaceKey; run byte or file budget
	NoteRootLink      = "root-link"            // key: WorkspaceKey; a component of the workspace path is a symlink
	NoteUnreadable    = "unreadable"           // key: WorkspaceKey; workspace not merged
	NoteSpecial       = "special-file"         // key: entry key; socket, fifo, device
	NoteName          = "unsupported-name"     // key: WorkspaceKey
	NoteReserved      = "reserved-name"        // key: entry key; name matches tmp_recovery_globs
	NoteHeldBack      = "held-back"            // key: dir key of a dependency/cache/VCS folder
	NoteCollision     = "name-collision"       // key: the new path; T has another spelling of it
	NoteBytes         = "bytes-unavailable"    // key: file key; Desired names content neither folder holds
	NoteTypeConflict  = "type-conflict"        // key: the path kept in T's form
	NoteLinkKept      = "link-kept"            // key: link key
	NoteGitConflict   = "git-conflict"         // key: unit key; both sides changed it this run
	NoteGitDiverged   = "git-diverged"         // key: unit key; still diverged from an earlier run
	NoteConflictName  = "conflict-name"        // key: file key; no free sibling name
	NoteDeletionOff   = "deletion-not-carried" // key: WorkspaceKey
	NoteDeletionDefer = "deletion-deferred"    // key: file or unit key (3.3)
	NoteMissing       = "missing"              // key: WorkspaceKey named by Desired, absent in both folders
	NoteUnrecognized  = "unrecognized-ref"     // key: ""
	NoteSourceChanged = "source-changed"       // key: file key; soft copy skipped at P7 (result only)
	NoteReadOnly      = "read-only-dir"        // key: WorkspaceKey; a folder lacks owner write permission
)

// Notes lists every note constant.
var Notes = []string{NoteOversizeFiles, NoteOversizeBytes, NoteBudget, NoteRootLink, NoteUnreadable,
	NoteSpecial, NoteName, NoteReserved, NoteHeldBack, NoteCollision, NoteBytes, NoteTypeConflict,
	NoteLinkKept, NoteGitConflict, NoteGitDiverged, NoteConflictName, NoteDeletionOff, NoteDeletionDefer,
	NoteMissing, NoteUnrecognized, NoteSourceChanged, NoteReadOnly}

// Values of a root marker's "over" member: why a workspace is not merged
// this run.
const (
	OverFiles      = "files"
	OverBytes      = "bytes"
	OverBudget     = "budget"
	OverLink       = "link"
	OverUnreadable = "unreadable"
	// OverReadOnly: a folder of the workspace (or the workspace itself)
	// lacks owner write permission, so files cannot be written into it.
	OverReadOnly = "read-only"
)

// Limits bound what one ReadRefs call reads.
type Limits struct {
	MaxFilesPerWorkspace int   // > 0
	MaxWorkspaceBytes    int64 // > 0
	MaxRunBytes          int64 // > 0, bytes hashed by one ReadRefs call (cache hits count too)
	MaxRunFiles          int   // > 0, files listed by one ReadRefs call
}

// DefaultLimits are the [scratch] defaults (spec-scratch 9.2).
func DefaultLimits() Limits {
	return Limits{MaxFilesPerWorkspace: 20000, MaxWorkspaceBytes: 2 << 30, MaxRunBytes: 8 << 30, MaxRunFiles: 50000}
}

// Adapter reads and writes row scratch. It reads only the workspaces
// another row references (sources.RefReader).
type Adapter struct {
	Limits   Limits
	Exclude  []string   // config scratch.exclude, added to the manifest's
	HeldBack []string   // manifest dependency_dirs + vcs_dirs + config scratch.cache_dirs
	Cache    *StatCache // nil: hash everything
}

var (
	_ sources.Adapter   = (*Adapter)(nil)
	_ sources.RefReader = (*Adapter)(nil)
)

// New returns an adapter.
func New(l Limits, exclude, heldBack []string, c *StatCache) *Adapter {
	return &Adapter{Limits: l, Exclude: exclude, HeldBack: heldBack, Cache: c}
}

// Row implements sources.Adapter.
func (*Adapter) Row() model.RowID { return model.RowScratch }

// Read implements sources.Adapter: row scratch is read only with ReadRefs.
func (*Adapter) Read(context.Context, sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, sources.ErrRefsRequired
}

// ErrNoSpec is returned when the manifest row has no [row.scratch].
var ErrNoSpec = errors.New("scratch: the manifest row has no [row.scratch] table")

// rules returns the read rules of one call.
func (a *Adapter) rules(rc sources.ReadContext) (readRules, error) {
	spec := rc.Spec.Scratch
	if spec == nil {
		return readRules{}, ErrNoSpec
	}
	r := readRules{
		root:     spec.Root,
		exclude:  union(spec.Exclude, a.Exclude),
		heldBack: union(spec.DependencyDirs, spec.VCSDirs, a.HeldBack),
	}
	if rc.Manifest != nil {
		r.reserved = rc.Manifest.TmpRecoveryGlobs
	}
	return r, nil
}

// union returns the distinct entries of the lists, in order.
func union(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, s := range l {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// ReadRefs reads the given workspaces of rc.Folder (spec-scratch 6.2): a
// listing pass that never follows a symlink, the limits, hashing through
// the stat cache, git unit digests and one root marker per workspace.
// A workspace that cannot be merged this run keeps only its marker with
// an "over" member, and its note goes to RowSnapshot.Notes.
func (a *Adapter) ReadRefs(ctx context.Context, rc sources.ReadContext, refs []string) (*model.RowSnapshot, error) {
	rules, err := a.rules(rc)
	if err != nil {
		return nil, err
	}
	keys := slices.Clone(refs)
	sort.Strings(keys)
	keys = slices.Compact(keys)
	snap := sources.NewSnapshot(model.RowScratch)
	snap.Tombstones = nil
	fk := folderKeyOf(rc.Folder)
	var runFiles int
	var runBytes int64
	marker := func(ws, over, n string, mode uint32) error {
		it, err := RootItem(ws, mode, over)
		if err != nil {
			return err
		}
		snap.Items[it.Key] = it
		snap.Notes = append(snap.Notes, note(ws, n))
		return nil
	}
	for _, ws := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !ValidWorkspaceKey(ws) {
			return nil, fmt.Errorf("scratch: read: %w", ErrKey)
		}
		ok, err := Present(rc.Folder, rules.root, ws)
		switch {
		case errors.Is(err, ErrLink):
			if err := marker(ws, OverLink, NoteRootLink, 0); err != nil {
				return nil, err
			}
			continue
		case err != nil:
			if err := marker(ws, OverUnreadable, NoteUnreadable, 0); err != nil {
				return nil, err
			}
			continue
		case !ok:
			continue
		}
		w, err := listWorkspace(ctx, rc.Folder, ws, rules, listLimits{
			files: min(a.Limits.MaxFilesPerWorkspace, a.Limits.MaxRunFiles-runFiles),
			bytes: min(a.Limits.MaxWorkspaceBytes, a.Limits.MaxRunBytes-runBytes)})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			if err := marker(ws, OverUnreadable, NoteUnreadable, 0); err != nil {
				return nil, err
			}
			continue
		}
		over, n := "", ""
		switch {
		case w.files > a.Limits.MaxFilesPerWorkspace:
			over, n = OverFiles, NoteOversizeFiles
		case w.bytes > a.Limits.MaxWorkspaceBytes:
			over, n = OverBytes, NoteOversizeBytes
		case w.stopped || runBytes+w.bytes > a.Limits.MaxRunBytes || runFiles+w.files > a.Limits.MaxRunFiles:
			over, n = OverBudget, NoteBudget
		case w.readOnly:
			over, n = OverReadOnly, NoteReadOnly
		}
		if over != "" {
			if err := marker(ws, over, n, w.mode); err != nil {
				return nil, err
			}
			continue
		}
		snap.Notes = append(snap.Notes, w.notes...)
		if w.badName {
			snap.Notes = append(snap.Notes, note(ws, NoteName))
		}
		runFiles += w.files
		runBytes += w.bytes
		hashes := map[string]string{}
		unreadable := false
		for _, e := range w.entries {
			if e.kind != KindFile {
				continue
			}
			sum, err := hashFile(e, fk, a.Cache)
			if err != nil {
				unreadable = true
				break
			}
			hashes[e.key] = sum
		}
		if unreadable {
			if err := marker(ws, OverUnreadable, NoteUnreadable, w.mode); err != nil {
				return nil, err
			}
			continue
		}
		items, err := w.items(hashes)
		if err != nil {
			return nil, err
		}
		for k, it := range items {
			snap.Items[k] = it
		}
	}
	return snap, nil
}

// Edit order groups (spec-scratch 7.5 step 3).
const (
	groupMkdir = iota
	groupCopyEntering
	groupSymlink
	groupCopyLeaving
	groupRemove
)

// source is a file that holds some content.
type source struct {
	side   string // plan.SideEntering or plan.SideLeaving
	folder string
	key    string
}

// pendingCopy is a file copy whose content only a T file that this plan
// replaces or removes holds (for example a conflict's T loser).
type pendingCopy struct {
	key  string
	rel  string
	base string
	v    Value
}

// opsRun is one Ops call.
type opsRun struct {
	root      string
	tFolder   string
	sFolder   string
	cur, des  map[string]model.Item
	src       map[string]model.Item
	index     map[string][]source
	targets   map[string]bool              // T file keys a copy or removal of this plan writes
	pending   []pendingCopy                // copies no unwritten file can serve (second pass)
	listings  map[string]map[string]string // T parent dir -> folded name -> name
	groups    [5][]plan.Edit
	skipped   []model.Change
	skipDirs  []string // dir keys skipped with their subtree
	madeDirs  map[string]bool
	madeOrder []string
}

// Ops returns the edits that turn T (current) into desired (spec-scratch
// 7.5): mkdirs (ancestors with S's modes, then new directories by depth),
// copies whose source is T's own content, symlinks, copies from S, and
// quarantines of files T has that desired lacks. A new path whose name
// differs from an existing entry of T's parent only in letter case or
// composition is skipped (name-collision); content neither folder holds
// is skipped (bytes-unavailable). Ops(cur, cur) returns no edits.
func (a *Adapter) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	var res sources.OpsResult
	spec := oc.Spec.Scratch
	if spec == nil {
		return res, ErrNoSpec
	}
	if desired == nil {
		return res, nil
	}
	r := &opsRun{
		root: spec.Root, tFolder: oc.Folder, cur: itemsOf(current), des: desired.Items,
		listings: map[string]map[string]string{}, madeDirs: map[string]bool{},
	}
	if oc.Source != nil {
		r.sFolder = oc.Source.Folder
	}
	r.src = itemsOf(oc.SourceSnapshot)
	r.buildIndex()
	for _, ws := range workspacesOf(r.des) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if err := r.workspace(ws); err != nil {
			return res, err
		}
	}
	r.resolvePending()
	if err := r.removals(); err != nil {
		return res, err
	}
	for _, g := range r.groups {
		res.Edits = append(res.Edits, g...)
	}
	res.Skipped = r.skipped
	return res, nil
}

func itemsOf(s *model.RowSnapshot) map[string]model.Item {
	if s == nil || s.Items == nil {
		return map[string]model.Item{}
	}
	return s.Items
}

// workspacesOf returns the sorted workspace keys of a key set.
func workspacesOf(items map[string]model.Item) []string {
	set := map[string]bool{}
	for k := range items {
		if ws := WorkspaceOf(k); ws != "" {
			set[ws] = true
		}
	}
	out := make([]string, 0, len(set))
	for ws := range set {
		out = append(out, ws)
	}
	sort.Strings(out)
	return out
}

// fileChanges reports whether desired file item d needs a write over
// T's item c.
func fileChanges(c *model.Item, d model.Item) bool {
	if c == nil {
		return true
	}
	cv, _ := Decode(*c)
	dv, _ := Decode(d)
	return cv.K != KindFile || cv.SHA256 != dv.SHA256 || cv.Mode != dv.Mode
}

// buildIndex maps content hashes to files that hold them: T's files that
// no copy or removal of this plan targets, and S's files. The targets are
// kept for the second pass (resolvePending).
func (r *opsRun) buildIndex() {
	targets := map[string]bool{}
	for k, d := range r.des {
		if KindOf(d) != KindFile {
			continue
		}
		var c *model.Item
		if it, ok := r.cur[k]; ok {
			c = &it
		}
		if fileChanges(c, d) {
			targets[k] = true
		}
	}
	for k, c := range r.cur {
		if _, ok := r.des[k]; !ok && KindOf(c) == KindFile {
			targets[k] = true
		}
	}
	r.targets = targets
	r.index = map[string][]source{}
	add := func(items map[string]model.Item, side, folder string, skip map[string]bool) {
		keys := make([]string, 0, len(items))
		for k := range items {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v, ok := Decode(items[k])
			if !ok || v.K != KindFile || skip[k] || folder == "" {
				continue
			}
			r.index[v.SHA256] = append(r.index[v.SHA256], source{side: side, folder: folder, key: k})
		}
	}
	add(r.cur, plan.SideEntering, r.tFolder, targets)
	add(r.src, plan.SideLeaving, r.sFolder, nil)
}

// skip records a per-key skip.
func (r *opsRun) skip(key, n string) {
	r.skipped = append(r.skipped, sources.Skip(model.RowScratch, key, n))
	if strings.HasSuffix(key, "/") {
		r.skipDirs = append(r.skipDirs, key)
	}
}

// underSkipped reports whether key lies below (or is) a skipped dir key.
func (r *opsRun) underSkipped(key string) bool {
	for _, d := range r.skipDirs {
		if strings.HasPrefix(key, d) {
			return true
		}
	}
	return false
}

// lstatData returns the lstat of a data-folder-relative path in folder.
func lstatData(folder, rel string) (fs.FileInfo, error) {
	p, err := containedPath(folder, rel)
	if err != nil {
		return nil, err
	}
	return os.Lstat(p)
}

func containedPath(folder, rel string) (string, error) {
	if !plan.CleanRel(rel) {
		return "", ErrKey
	}
	return filepath.Join(folder, filepath.FromSlash(rel)), nil
}

// mkdir adds one mkdir edit per path.
func (r *opsRun) mkdir(rel, item string, mode fs.FileMode) {
	if r.madeDirs[rel] {
		return
	}
	r.madeDirs[rel] = true
	r.groups[groupMkdir] = append(r.groups[groupMkdir], plan.Edit{
		Loc: plan.Location{File: rel}, Row: model.RowScratch, Item: item, Kind: plan.EditMkdir, Mode: mode.Perm(),
	})
}

// collides reports whether T's parent directory of the data path rel
// holds an entry whose folded name equals name's but whose bytes differ.
func (r *opsRun) collides(rel string) (bool, error) {
	dir, name := filepath.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	names, ok := r.listings[dir]
	if !ok {
		names = map[string]string{}
		p := r.tFolder
		if dir != "" {
			var err error
			if p, err = containedPath(r.tFolder, dir); err != nil {
				return false, err
			}
		}
		entries, err := os.ReadDir(p)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		for _, e := range entries {
			names[FoldName(e.Name())] = e.Name()
		}
		r.listings[dir] = names
	}
	have, found := names[FoldName(name)]
	return found && have != name, nil
}

// workspace adds the edits of one workspace: ancestors, new directories,
// files and links.
func (r *opsRun) workspace(ws string) error {
	if _, ok := r.cur[RootKey(ws)]; !ok {
		if _, want := r.des[RootKey(ws)]; want {
			if ok, err := r.ancestors(ws); err != nil || !ok {
				return err
			}
		}
	}
	keys := make([]string, 0)
	for k := range r.des {
		if WorkspaceOf(k) == ws && k != RootKey(ws) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		di, dj := strings.Count(strings.TrimSuffix(keys[i], "/"), "/"), strings.Count(strings.TrimSuffix(keys[j], "/"), "/")
		if di != dj {
			return di < dj
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		if r.underSkipped(k) {
			continue
		}
		d := r.des[k]
		v, ok := Decode(d)
		if !ok {
			return fmt.Errorf("scratch: ops: item %q has no scratch value", k)
		}
		var err error
		switch v.K {
		case KindDir, KindGit:
			err = r.dir(k, v)
		case KindFile:
			err = r.file(k, d, v)
		case KindLink:
			err = r.link(k, v)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ancestors adds mkdirs for <root>, <root>/<acct>, <root>/<acct>/<org>
// and the workspace root when T lacks them, with the mode of the same
// directory in S. ok is false when the workspace is skipped.
func (r *opsRun) ancestors(ws string) (bool, error) {
	parts := strings.Split(ws, "/")
	rel := r.root
	parentWritable := true // the data folder itself
	for i := 0; i <= len(parts); i++ {
		if i > 0 {
			rel += "/" + parts[i-1]
		}
		fi, err := lstatData(r.tFolder, rel)
		switch {
		case err == nil && fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0:
			parentWritable = ownerWritable(fi.Mode())
			continue
		case err == nil:
			r.skip(RootKey(ws), NoteCollision)
			return false, nil
		case !errors.Is(err, fs.ErrNotExist):
			return false, err
		}
		if coll, err := r.collides(rel); err != nil || coll {
			if coll {
				r.skip(RootKey(ws), NoteCollision)
			}
			return false, err
		}
		sfi, err := lstatData(r.sFolder, rel)
		if r.sFolder == "" || err != nil || !sfi.IsDir() || sfi.Mode()&fs.ModeSymlink != 0 || sfi.Mode().Perm() == 0 {
			r.skip(RootKey(ws), NoteBytes)
			return false, nil
		}
		// A folder that cannot be written into (T's existing parent, or
		// S's mode for the new one) would stop the copies below it.
		if !parentWritable || !ownerWritable(sfi.Mode()) {
			r.skip(RootKey(ws), NoteReadOnly)
			return false, nil
		}
		item := ""
		if i == len(parts) {
			item = RootKey(ws)
		}
		r.mkdir(rel, item, sfi.Mode().Perm())
	}
	return true, nil
}

// dir adds a mkdir for a directory T lacks.
func (r *opsRun) dir(k string, v Value) error {
	if _, ok := r.cur[k]; ok {
		return nil
	}
	rel := DataPath(r.root, k)
	coll, err := r.collides(rel)
	if err != nil {
		return err
	}
	if coll {
		r.skip(k, NoteCollision)
		return nil
	}
	if v.Mode == 0 {
		r.skip(k, NoteBytes)
		return nil
	}
	r.mkdir(rel, k, fs.FileMode(v.Mode))
	return nil
}

// file adds a copy when T's file differs from desired.
func (r *opsRun) file(k string, d model.Item, v Value) error {
	var c *model.Item
	if it, ok := r.cur[k]; ok {
		c = &it
	}
	if !fileChanges(c, d) {
		return nil
	}
	base := ""
	if c != nil {
		cv, _ := Decode(*c)
		if cv.K != KindFile {
			r.skip(k, NoteTypeConflict)
			return nil
		}
		base = cv.SHA256
	}
	rel := DataPath(r.root, k)
	if c == nil {
		coll, err := r.collides(rel)
		if err != nil {
			return err
		}
		if coll {
			r.skip(k, NoteCollision)
			return nil
		}
	}
	src, fi, ok := r.source(r.index, v.SHA256, v.Size)
	if !ok {
		r.pending = append(r.pending, pendingCopy{key: k, rel: rel, base: base, v: v})
		return nil
	}
	r.addCopy(k, rel, base, v, src, fi)
	return nil
}

// addCopy adds one copy edit to the group of its source's side.
func (r *opsRun) addCopy(k, rel, base string, v Value, src source, fi fs.FileInfo) {
	e := plan.Edit{
		Loc: plan.Location{File: rel}, Row: model.RowScratch, Item: k, Kind: plan.EditCopy,
		Mode: fs.FileMode(v.Mode).Perm(), BaseHash: base,
		Copy: &plan.CopySource{Side: src.side, Path: DataPath(r.root, src.key), Hash: v.SHA256, Size: v.Size,
			MTimeNs: fi.ModTime().UnixNano()},
	}
	if e.Mode == 0 {
		e.Mode = fi.Mode().Perm()
	}
	g := groupCopyLeaving
	if src.side == plan.SideEntering {
		g = groupCopyEntering
	}
	r.groups[g] = append(r.groups[g], e)
}

// resolvePending is the second pass over copies whose content only a T
// file this plan writes holds (spec-scratch 8: a T loser is copied to its
// sibling before it is replaced). Such a T file still holds its content
// while the copies from entering run, when the plan writes it only later:
// by a copy from leaving, by a removal (both ordered after the copies
// from entering), or not at all. A file a copy from entering writes, or
// one that is itself pending, is never a source here. Apply verifies the
// source's hash, so a T file changed after the plan stops the run.
// Content no file holds is skipped (bytes-unavailable).
func (r *opsRun) resolvePending() {
	if len(r.pending) == 0 {
		return
	}
	written := map[string]bool{}
	for _, e := range r.groups[groupCopyEntering] {
		written[e.Item] = true
	}
	for _, p := range r.pending {
		written[p.key] = true
	}
	late := map[string][]source{}
	if r.tFolder != "" {
		for _, k := range sortedKeys(r.targets) {
			v, ok := Decode(r.cur[k])
			if !ok || v.K != KindFile || written[k] {
				continue
			}
			late[v.SHA256] = append(late[v.SHA256], source{side: plan.SideEntering, folder: r.tFolder, key: k})
		}
	}
	for _, p := range r.pending {
		src, fi, ok := r.source(late, p.v.SHA256, p.v.Size)
		if !ok {
			r.skip(p.key, NoteBytes)
			continue
		}
		r.addCopy(p.key, p.rel, p.base, p.v, src, fi)
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// source finds a file of index holding content sha. The file must still
// be a regular file of the recorded size.
func (r *opsRun) source(index map[string][]source, sha string, size int64) (source, fs.FileInfo, bool) {
	for _, s := range index[sha] {
		fi, err := lstatData(s.folder, DataPath(r.root, s.key))
		if err == nil && fi.Mode().IsRegular() && fi.Size() == size {
			return s, fi, true
		}
	}
	return source{}, nil, false
}

// link adds a symlink T lacks (links are create-only). A link whose
// target is absolute or leaves its workspace is never created (the rows
// module drops it already; this is the backstop before Validate).
func (r *opsRun) link(k string, v Value) error {
	if _, ok := r.cur[k]; ok {
		return nil
	}
	if !LinkInside(k, v.Target) {
		r.skip(k, NoteLinkKept)
		return nil
	}
	rel := DataPath(r.root, k)
	coll, err := r.collides(rel)
	if err != nil {
		return err
	}
	if coll {
		r.skip(k, NoteCollision)
		return nil
	}
	r.groups[groupSymlink] = append(r.groups[groupSymlink], plan.Edit{
		Loc: plan.Location{File: rel}, Row: model.RowScratch, Item: k, Kind: plan.EditSymlink, Link: v.Target,
	})
	return nil
}

// removals quarantines files T has that desired lacks.
func (r *opsRun) removals() error {
	keys := make([]string, 0)
	for k, c := range r.cur {
		if _, ok := r.des[k]; ok {
			continue
		}
		if KindOf(c) == KindFile {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		cv, _ := Decode(r.cur[k])
		r.groups[groupRemove] = append(r.groups[groupRemove], plan.Edit{
			Loc: plan.Location{File: DataPath(r.root, k)}, Row: model.RowScratch, Item: k, Kind: plan.EditRemove,
			BaseHash: cv.SHA256, Destructive: true,
		})
	}
	return nil
}
