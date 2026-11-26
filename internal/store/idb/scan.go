package idb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/syndtr/goleveldb/leveldb/comparer"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/table"

	"github.com/kazupc/paritydesk/internal/store/kv"
	"github.com/kazupc/paritydesk/internal/store/kv/leveldb"
)

// comparatorName is Chromium's IndexedDB comparator.
const comparatorName = "idb_cmp1"

// ctxCheckEvery is how many entries are walked between context checks.
const ctxCheckEvery = 4096

// skipFile reports the files the reader never opens, clones or copies:
// the lock and the info logs (which hold absolute user paths).
func skipFile(name string) bool {
	return name == "LOCK" || name == "LOG" || strings.HasPrefix(name, "LOG.")
}

// openFile opens a file of the database directory read-only without
// following a symlink. Tests replace it to slow a read down.
var openFile = func(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

// rawComparer is the bytewise comparer under Chromium's name. The reader
// walks every table in full, so ordering is never consulted.
type rawComparer struct{}

func (rawComparer) Compare(a, b []byte) int { return comparer.DefaultComparer.Compare(a, b) }
func (rawComparer) Name() string            { return comparatorName }
func (rawComparer) Separator(dst, a, b []byte) []byte {
	return comparer.DefaultComparer.Separator(dst, a, b)
}
func (rawComparer) Successor(dst, b []byte) []byte { return comparer.DefaultComparer.Successor(dst, b) }

// entry is the newest entry seen for one user key.
type entry struct {
	seq   uint64
	put   bool
	value []byte
}

// collector keeps, per matched user key, the entry with the highest
// sequence.
type collector struct {
	spec    Spec
	keyText []byte // 0x01 + StringWithLength(spec.Key)
	dbName  map[string]entry
	osName  map[string]entry
	osDB    map[string]uint64
	data    map[string]entry
	dataIDs map[string][2]uint64
	undo    map[string]entry
	meta    int
	n       int
}

func newCollector(spec Spec) *collector {
	kt := append([]byte{typeString}, stringWithLength(spec.Key)...)
	return &collector{spec: spec, keyText: kt, dbName: map[string]entry{}, osName: map[string]entry{},
		osDB: map[string]uint64{}, data: map[string]entry{}, dataIDs: map[string][2]uint64{}, undo: map[string]entry{}}
}

func keep(m map[string]entry, k []byte, e entry) {
	if old, ok := m[string(k)]; ok && old.seq >= e.seq {
		return
	}
	if e.put {
		e.value = slices.Clone(e.value)
	} else {
		e.value = nil
	}
	m[string(k)] = e
}

// add considers one entry.
func (c *collector) add(uk []byte, e entry) {
	switch scopeKind(uk) {
	case scopesUndo:
		keep(c.undo, uk, e)
		return
	case scopesMeta:
		c.meta++
		return
	}
	if name, ok := matchDatabaseName(uk); ok {
		if name == c.spec.Database {
			keep(c.dbName, uk, e)
		}
		return
	}
	if db, name, ok := matchObjectStoreName(uk); ok {
		if name == c.spec.ObjectStore {
			keep(c.osName, uk, e)
			c.osDB[string(uk)] = db
		}
		return
	}
	if db, os, ok := matchData(uk, c.keyText); ok {
		keep(c.data, uk, e)
		c.dataIDs[string(uk)] = [2]uint64{db, os}
	}
}

// ReadString reads in place. It opens files O_RDONLY|O_NOFOLLOW, regular
// files only, confined below folder/spec.Dir; it never opens LOG, LOG.old
// or LOCK, never writes, never opens a database. Used on clones and in
// tests.
func ReadString(ctx context.Context, folder string, spec Spec) (Value, error) {
	if spec.MaxRead <= 0 {
		spec.MaxRead = 10 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, spec.MaxRead)
	defer cancel()
	v, err := readString(rctx, folder, spec)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		if ctx.Err() != nil {
			return Value{}, ctx.Err()
		}
		return Value{}, fail("read: time budget", ErrTooLarge)
	}
	return v, err
}

// stamp is what a file looked like when it was read.
type stamp struct {
	size  int64
	mtime time.Time
}

func readString(ctx context.Context, folder string, spec Spec) (Value, error) {
	dir, err := checkDir(folder, spec)
	if err != nil {
		return Value{}, err
	}
	read := map[string]stamp{}
	open := func(name string) (*os.File, error) {
		if name != filepath.Base(name) || skipFile(name) {
			return nil, fail("open "+filepath.Base(name), ErrUnsupported)
		}
		f, err := openFile(filepath.Join(dir, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			if errors.Is(err, syscall.ELOOP) {
				return nil, fail("open "+name, ErrUnsupported)
			}
			return nil, fmt.Errorf("idb: open %s: %w", name, stripPath(err))
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("idb: stat %s: %w", name, stripPath(err))
		}
		if !fi.Mode().IsRegular() {
			f.Close()
			return nil, fail("open "+name, ErrUnsupported)
		}
		read[name] = stamp{fi.Size(), fi.ModTime()}
		return f, nil
	}
	// 1. Version set.
	info, err := leveldb.InspectManifest(dir, func(name string) (io.ReadCloser, error) { return open(name) })
	if err != nil {
		if ctx.Err() != nil {
			return Value{}, ctx.Err()
		}
		return Value{}, fail("manifest", ErrUnsupported)
	}
	if info.Comparator != comparatorName {
		return Value{}, fail("manifest: comparator", ErrUnsupported)
	}
	// 2. Files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Value{}, fmt.Errorf("idb: list: %w", stripPath(err))
	}
	tableNames := map[int64]string{}
	var logs []int64
	for _, e := range entries {
		name := e.Name()
		num, ext, ok := parseNumbered(name)
		if !ok {
			continue
		}
		switch ext {
		case "ldb", "sst":
			if prev, dup := tableNames[num]; !dup || (ext == "ldb" && !strings.HasSuffix(prev, ".ldb")) {
				tableNames[num] = name
			}
		case "log":
			if num >= info.LogNum || (info.PrevLogNum != 0 && num == info.PrevLogNum) {
				logs = append(logs, num)
			}
		}
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i] < logs[j] })
	c := newCollector(spec)
	// 3, 4. Tables: block types first, then a full walk.
	for _, t := range info.Tables {
		if err := ctx.Err(); err != nil {
			return Value{}, err
		}
		name, ok := tableNames[t.Num]
		if !ok {
			return Value{}, fail(fmt.Sprintf("table %06d missing", t.Num), ErrTorn)
		}
		if err := readTable(ctx, open, name, t, c); err != nil {
			return Value{}, err
		}
	}
	// 5. Logs.
	var tailDropped bool
	for i, num := range logs {
		if err := ctx.Err(); err != nil {
			return Value{}, err
		}
		name := fmt.Sprintf("%06d.log", num)
		f, err := open(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return Value{}, fail("log "+name+" missing", ErrTorn)
			}
			return Value{}, err
		}
		limit := logLimit(spec)
		data, err := io.ReadAll(io.LimitReader(ctxReader{ctx, f}, limit+1))
		f.Close()
		if err != nil {
			return Value{}, fmt.Errorf("idb: read %s: %w", name, stripPath(err))
		}
		if int64(len(data)) > limit {
			return Value{}, fail("log "+name, ErrTooLarge)
		}
		dropped, err := scanLog(data, i == len(logs)-1, func(rec []byte) error {
			return decodeBatch(rec, func(e batchEntry) error {
				c.n++
				if c.n%ctxCheckEvery == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				c.add(e.key, entry{seq: e.seq, put: e.put, value: e.value})
				return nil
			})
		})
		if err != nil {
			return Value{}, err
		}
		tailDropped = tailDropped || dropped
	}
	// 6, 7. Resolve.
	v, winner, err := c.resolve()
	if err != nil && !errors.Is(err, ErrKeyAbsent) {
		return Value{}, err
	}
	// 8. Pending scopes, also for an absent key.
	if perr := c.pending(v, winner); perr != nil {
		return Value{}, perr
	}
	if err != nil {
		return Value{}, err
	}
	// 9. Value.
	d, err := decodeValue(c.data[winner].value, spec)
	if err != nil {
		return Value{}, err
	}
	v.Text, v.Wrapped, v.Blink, v.V8, v.TailDropped = d.text, d.wrapped, d.blink, d.v8, tailDropped
	// Torn re-check: every file read must be unchanged.
	for name, st := range read {
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || fi.Size() != st.size || !fi.ModTime().Equal(st.mtime) {
			return Value{}, fail(name+" changed", ErrTorn)
		}
	}
	return v, nil
}

// readTable checks the block types of one live table and walks it.
func readTable(ctx context.Context, open func(string) (*os.File, error), name string, t leveldb.TableFile, c *collector) (err error) {
	// goleveldb's block decoder trusts length varints and can panic on a
	// crafted block that still carries a valid checksum; treat that as
	// damage (spec-pins 4.3 step 3), never a crash.
	defer func() {
		if recover() != nil {
			err = fail("table "+name, ErrUnclean)
		}
	}()
	f, err := open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fail("table "+name+" missing", ErrTorn)
		}
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("idb: stat %s: %w", name, stripPath(err))
	}
	if fi.Size() != t.Size {
		return fail("table "+name+" size", ErrTorn)
	}
	types, err := leveldb.TableBlockTypes(f, fi.Size())
	switch {
	case errors.Is(err, kv.ErrUnknownCompression):
		return fail("table "+name+" compression", ErrUnsupported)
	case err != nil:
		return fail("table "+name, ErrUnclean)
	}
	for _, typ := range types {
		if typ != 0 && typ != 1 {
			return fail("table "+name+" compression", ErrUnsupported)
		}
	}
	r, err := table.NewReader(f, fi.Size(), storage.FileDesc{Type: storage.TypeTable, Num: t.Num}, nil, nil,
		&opt.Options{Comparer: rawComparer{}, Strict: opt.StrictBlockChecksum})
	if err != nil {
		return fail("table "+name, ErrUnclean)
	}
	defer r.Release()
	it := r.NewIterator(nil, nil)
	defer it.Release()
	for it.Next() {
		c.n++
		if c.n%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		ik := it.Key()
		if len(ik) < 8 {
			return fail("table "+name+" key", ErrUnsupported)
		}
		trailer := binary.LittleEndian.Uint64(ik[len(ik)-8:])
		kind := trailer & 0xff
		if kind > 1 {
			return fail("table "+name+" key", ErrUnsupported)
		}
		c.add(ik[:len(ik)-8], entry{seq: trailer >> 8, put: kind == 1, value: it.Value()})
	}
	if err := it.Error(); err != nil {
		return fail("table "+name, ErrUnclean)
	}
	return nil
}

// resolve picks the winning data key (4.3 step 7).
func (c *collector) resolve() (Value, string, error) {
	var v Value
	var liveDB []entry
	for _, e := range c.dbName {
		if e.put {
			liveDB = append(liveDB, e)
		}
	}
	dbResolved := false
	switch {
	case len(liveDB) > 1:
		return v, "", fail("database name", ErrAmbiguous)
	case len(liveDB) == 1:
		id, n := binary.Uvarint(liveDB[0].value)
		if n <= 0 || n != len(liveDB[0].value) {
			return v, "", fail("database id", ErrUnsupported)
		}
		v.DB, v.DBFromMetadata, dbResolved = id, true, true
	}
	osResolved := false
	if dbResolved {
		var live []entry
		for k, e := range c.osName {
			if e.put && c.osDB[k] == v.DB {
				live = append(live, e)
			}
		}
		if len(live) == 1 {
			id, n := binary.Uvarint(live[0].value)
			if n <= 0 || n != len(live[0].value) {
				return v, "", fail("object store id", ErrUnsupported)
			}
			v.OS, v.OSFromMetadata, osResolved = id, true, true
		}
	}
	var cands []string
	for k, e := range c.data {
		ids := c.dataIDs[k]
		if !e.put || (dbResolved && ids[0] != v.DB) || (osResolved && ids[1] != v.OS) {
			continue
		}
		cands = append(cands, k)
	}
	switch len(cands) {
	case 0:
		return v, "", fail("key", ErrKeyAbsent)
	case 1:
	default:
		return v, "", fail("key", ErrAmbiguous)
	}
	w := cands[0]
	ids := c.dataIDs[w]
	v.DB, v.OS, v.Seq = ids[0], ids[1], c.data[w].seq
	return v, w, nil
}

// pending reports ErrPending when a live undo entry may concern the key
// (4.3 step 8). With a winner, the check is for its key and store; without
// one (key absent), for every data key of the resolved database seen.
func (c *collector) pending(v Value, winner string) error {
	type target struct{ key, prefix []byte }
	var targets []target
	if winner != "" {
		ids := c.dataIDs[winner]
		targets = append(targets, target{[]byte(winner), storePrefix(ids[0], ids[1])})
	} else {
		for k := range c.data {
			ids := c.dataIDs[k]
			if v.DBFromMetadata && ids[0] != v.DB {
				continue
			}
			targets = append(targets, target{[]byte(k), storePrefix(ids[0], ids[1])})
		}
		if v.DBFromMetadata && v.OSFromMetadata {
			targets = append(targets, target{dataKey(v.DB, v.OS, c.spec.Key), storePrefix(v.DB, v.OS)})
		}
	}
	for _, e := range c.undo {
		if !e.put {
			continue
		}
		for _, t := range targets {
			if Touches(e.value, t.key, t.prefix) {
				return fail("scopes", ErrPending)
			}
		}
	}
	return nil
}

// checkDir locates folder/spec.Dir: absent -> ErrBackend when another
// entry of the origin's IndexedDB backend exists, else ErrDirAbsent; a
// symlink anywhere on the way or a non-directory -> ErrUnsupported.
func checkDir(folder string, spec Spec) (string, error) {
	rel := filepath.Clean(spec.Dir)
	if spec.Dir == "" || filepath.IsAbs(rel) || rel == "." || strings.HasPrefix(rel, "..") {
		return "", fail("spec dir", ErrUnsupported)
	}
	p := folder
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if backendElsewhere(folder, spec) {
				return "", fail("dir", ErrBackend)
			}
			return "", fail("dir", ErrDirAbsent)
		case err != nil:
			return "", fmt.Errorf("idb: dir: %w", stripPath(err))
		case fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir():
			return "", fail("dir", ErrUnsupported)
		}
	}
	return p, nil
}

// backendElsewhere reports another entry of the backend prefix beside the
// missing LevelDB directory (another storage engine).
func backendElsewhere(folder string, spec Spec) bool {
	if spec.BackendPrefix == "" {
		return false
	}
	parent := filepath.Join(folder, filepath.Dir(spec.BackendPrefix))
	base := filepath.Base(spec.BackendPrefix)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, base) && n != base+"leveldb" && n != base+"blob" {
			return true
		}
	}
	return false
}

// parseNumbered parses "<num>.<ext>".
func parseNumbered(name string) (int64, string, bool) {
	stem, ext, ok := strings.Cut(name, ".")
	if !ok || stem == "" {
		return 0, "", false
	}
	n, err := strconv.ParseInt(stem, 10, 64)
	if err != nil || n < 0 {
		return 0, "", false
	}
	return n, ext, true
}

// stripPath drops the paths of an *os.LinkError or *fs.PathError,
// keeping base names (spec-pins 4.1: errors never hold absolute paths).
func stripPath(err error) error {
	var le *os.LinkError
	if errors.As(err, &le) {
		return fmt.Errorf("%s %s %s: %w", le.Op, filepath.Base(le.Old), filepath.Base(le.New), le.Err)
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s %s: %w", pe.Op, filepath.Base(pe.Path), pe.Err)
	}
	return err
}

// logLimit is the most bytes one log file may hold. LevelDB rolls a log
// over at its write buffer size (a few MiB), so the byte-copy budget is
// ample and keeps one read far below the clone budget (2 GiB), which is
// a budget for clones, not for memory.
func logLimit(spec Spec) int64 {
	if spec.MaxCopyBytes > 0 && (spec.MaxCloneBytes <= 0 || spec.MaxCopyBytes < spec.MaxCloneBytes) {
		return spec.MaxCopyBytes
	}
	return spec.MaxCloneBytes
}

// ctxReader stops a read when ctx ends (the time budget of ReadString
// also covers reading one large log).
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
