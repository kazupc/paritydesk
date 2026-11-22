package leveldb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// fixture is a synthetic Local Storage item.
type fixture struct{ name, value string }

var items = []fixture{
	{"LSS-persisted.theme", `{"value":"dark","tabId":"t1","timestamp":1}`},
	{"dframe-store", `{"state":{"a":1},"version":2}`},
	{"persisted.sidebar", `{"state":{"open":true},"version":0}`},
	{"unicode-\u03a9", "value \u03a9\u0416"},
}

func lsKey(t testing.TB, name string) []byte {
	t.Helper()
	k, err := localstorage.Key(name)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// makeDB builds a LevelDB database in dir with the fixture items plus
// extra raw pairs, compacted into table files, and leaves its LOCK file.
func makeDB(t testing.TB, dir string, c opt.Compression, extra map[string]string) {
	t.Helper()
	db, err := leveldb.OpenFile(dir, &opt.Options{Compression: c})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if err := db.Put(lsKey(t, it.name), localstorage.EncodeString(it.value), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Put(localstorage.MetaKey, []byte("meta"), nil); err != nil {
		t.Fatal(err)
	}
	for k, v := range extra {
		if err := db.Put([]byte(k), []byte(v), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "LOCK")); err != nil {
		t.Fatalf("fixture has no LOCK: %v", err)
	}
	if len(tables(t, dir)) == 0 {
		t.Fatal("fixture has no table files")
	}
}

// tables returns the paths of the live tables of the database in dir
// (those its manifest lists), in number order. goleveldb may leave an
// obsolete table file behind after a compaction; it is not listed.
func tables(t testing.TB, dir string) []string {
	t.Helper()
	m, err := readManifest(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range m.tableNums() {
		p := filepath.Join(dir, fmt.Sprintf("%06d.ldb", n))
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("live table: %v", err)
		}
		out = append(out, p)
	}
	return out
}

// hashTree returns name -> sha256 for every file under dir.
func hashTree(t testing.TB, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		rel, _ := filepath.Rel(dir, p)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertSameTree(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	got := hashTree(t, dir)
	if len(got) != len(want) {
		t.Fatalf("file set changed: got %v want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s changed", k)
		}
	}
}

type env struct {
	o    *Opener
	fs   *fsxtest.FS
	root string // TempRoot
	dir  string // original database
}

func newEnv(t *testing.T, c opt.Compression) env {
	t.Helper()
	f := fsxtest.New(t)
	root := filepath.Join(t.TempDir(), "snapshots")
	o, err := New(root, f)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "leveldb")
	makeDB(t, dir, c, nil)
	return env{o: o, fs: f, root: root, dir: dir}
}

func assertTempRootEmpty(t *testing.T, root string) {
	t.Helper()
	es, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if len(es) != 0 {
		t.Fatalf("temp root not cleaned: %v", es)
	}
}

// assertCallsConfined checks the fsxtest log: every path is under the temp
// root, except the source of a Clone, which must be the original.
func assertCallsConfined(t *testing.T, e env) {
	t.Helper()
	if len(e.fs.Calls) == 0 {
		t.Fatal("no filesystem calls recorded")
	}
	for _, c := range e.fs.Calls {
		op, arg, _ := strings.Cut(c, " ")
		switch {
		case op == "Clone" && arg == e.dir:
		case !filepath.IsAbs(arg): // RemoveAll logs its rel; its root is confined
			if op != "RemoveAll" {
				t.Fatalf("unexpected relative call %q", c)
			}
		case arg == e.root || strings.HasPrefix(arg, e.root+string(filepath.Separator)):
		default:
			t.Fatalf("call outside the temp root: %q", c)
		}
	}
}

func readAll(t *testing.T, r kv.Reader, prefix []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := r.Scan(prefix, func(k, v []byte) error {
		out[string(k)] = string(v)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSnapshotReturnsWrittenData(t *testing.T) {
	for _, c := range []opt.Compression{opt.SnappyCompression, opt.NoCompression} {
		t.Run(c.String(), func(t *testing.T) {
			e := newEnv(t, c)
			r, err := e.o.OpenSnapshot(context.Background(), e.dir)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			for _, it := range items {
				raw, found, err := r.Get(lsKey(t, it.name))
				if err != nil || !found {
					t.Fatalf("%s: found=%v err=%v", it.name, found, err)
				}
				got, err := localstorage.DecodeText(raw)
				if err != nil || got != it.value {
					t.Fatalf("%s: got %q, %v", it.name, got, err)
				}
			}
			if _, found, err := r.Get(lsKey(t, "absent")); err != nil || found {
				t.Fatalf("absent key: found=%v err=%v", found, err)
			}
		})
	}
}

func TestSnapshotAndCheckFormatLeaveOriginalUntouched(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	before := hashTree(t, e.dir)
	if _, ok := before["LOCK"]; !ok {
		t.Fatal("original must have a LOCK file for this test")
	}
	r, err := e.o.OpenSnapshot(context.Background(), e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(readAll(t, r, nil)) == 0 {
		t.Fatal("empty snapshot")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	assertSameTree(t, e.dir, before)
	if err := e.o.CheckFormat(context.Background(), e.dir); err != nil {
		t.Fatal(err)
	}
	assertSameTree(t, e.dir, before)
	assertTempRootEmpty(t, e.root)
	assertCallsConfined(t, e)
}

func TestSnapshotWhileOriginalIsLocked(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	holder, err := leveldb.OpenFile(e.dir, nil) // stands in for the running app
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	r, err := e.o.OpenSnapshot(context.Background(), e.dir)
	if err != nil {
		t.Fatalf("snapshot of a locked database: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if w, err := e.o.OpenWritable(context.Background(), e.dir); err == nil {
		w.Close()
		t.Fatal("OpenWritable succeeded on a locked database")
	}
}

func TestCloneRemovedAfterClose(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	r, err := e.o.OpenSnapshot(context.Background(), e.dir)
	if err != nil {
		t.Fatal(err)
	}
	es, err := os.ReadDir(e.root)
	if err != nil || len(es) != 1 {
		t.Fatalf("want one snapshot directory, got %v %v", es, err)
	}
	fi, err := os.Stat(filepath.Join(e.root, es[0].Name()))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("snapshot directory mode: %v %v", fi, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	assertTempRootEmpty(t, e.root)
	assertCallsConfined(t, e)
}

func TestReadOnlyOpener(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	before := hashTree(t, e.dir)
	ro := kv.ReadOnly(e.o)
	r, err := ro.OpenSnapshot(context.Background(), e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ro.CheckFormat(context.Background(), e.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := ro.OpenWritable(context.Background(), e.dir); !errors.Is(err, kv.ErrReadOnly) {
		t.Fatalf("got %v, want kv.ErrReadOnly", err)
	}
	assertSameTree(t, e.dir, before)
	assertTempRootEmpty(t, e.root)
}

func TestWriteThenSnapshotReadsBack(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	ctx := context.Background()
	w, err := e.o.OpenWritable(ctx, e.dir)
	if err != nil {
		t.Fatal(err)
	}
	var b kv.Batch
	b.Put(lsKey(t, "new-item"), localstorage.EncodeString(`{"x":1}`))
	b.Put(lsKey(t, items[0].name), localstorage.EncodeString("replaced"))
	if err := w.Write(&b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := e.o.OpenSnapshot(ctx, e.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for name, want := range map[string]string{"new-item": `{"x":1}`, items[0].name: "replaced", items[1].name: items[1].value} {
		raw, found, err := r.Get(lsKey(t, name))
		if err != nil || !found {
			t.Fatalf("%s: found=%v err=%v", name, found, err)
		}
		if got, err := localstorage.DecodeText(raw); err != nil || got != want {
			t.Fatalf("%s: got %q %v", name, got, err)
		}
	}
}

func TestBatchPutAndDeleteIsAtomic(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	ctx := context.Background()
	w, err := e.o.OpenWritable(ctx, e.dir)
	if err != nil {
		t.Fatal(err)
	}
	del, put := lsKey(t, items[0].name), lsKey(t, "moved")
	var b kv.Batch
	b.Put(put, localstorage.EncodeString("v"))
	b.Delete(del)
	if err := w.Write(&b); err != nil {
		t.Fatal(err)
	}
	// Both effects are visible together through the writer.
	if _, found, _ := w.Get(del); found {
		t.Fatal("deleted key still present")
	}
	if _, found, _ := w.Get(put); !found {
		t.Fatal("put key missing")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// A batch on a closed writer applies nothing.
	var late kv.Batch
	late.Put(lsKey(t, "late"), []byte("\x01x"))
	late.Delete(put)
	if err := w.Write(&late); !errors.Is(err, kv.ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
	r, err := e.o.OpenSnapshot(ctx, e.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := readAll(t, r, localstorage.KeyPrefix())
	if _, ok := got[string(del)]; ok {
		t.Fatal("delete not durable")
	}
	if _, ok := got[string(put)]; !ok {
		t.Fatal("put not durable")
	}
	if _, ok := got[string(lsKey(t, "late"))]; ok {
		t.Fatal("late batch applied")
	}
}

// patchFirstDataBlock sets the compression byte of the table's first data
// block to typ and recomputes its masked CRC-32C.
func patchFirstDataBlock(t *testing.T, path string, typ byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	footer := b[len(b)-footerLen:]
	_, n, err := decodeHandle(footer)
	if err != nil {
		t.Fatal(err)
	}
	indexBH, _, err := decodeHandle(footer[n:])
	if err != nil {
		t.Fatal(err)
	}
	tr := tableReader{r: bytes.NewReader(b), limit: uint64(len(b) - footerLen)}
	index, err := tr.block(indexBH)
	if err != nil {
		t.Fatal(err)
	}
	var first *blockHandle
	if err := forEachValue(index, func(v []byte) error {
		if first == nil {
			h, _, err := decodeHandle(v)
			first = &h
			return err
		}
		return nil
	}); err != nil || first == nil {
		t.Fatalf("no data block: %v", err)
	}
	end := first.offset + first.length
	b[end] = typ
	binary.LittleEndian.PutUint32(b[end+1:], maskedCRC(b[first.offset:end+1]))
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownCompressionRefused(t *testing.T) {
	for _, c := range []opt.Compression{opt.SnappyCompression, opt.NoCompression} {
		t.Run(c.String(), func(t *testing.T) {
			e := newEnv(t, c)
			table := tables(t, e.dir)[0]
			patchFirstDataBlock(t, table, 2)
			before := hashTree(t, e.dir)
			ctx := context.Background()
			check := func(what string, err error) {
				t.Helper()
				if !errors.Is(err, kv.ErrUnknownCompression) {
					t.Fatalf("%s: got %v, want kv.ErrUnknownCompression", what, err)
				}
				msg := err.Error()
				if !strings.Contains(msg, filepath.Base(table)) {
					t.Fatalf("%s: error does not name the file: %v", what, err)
				}
				if strings.Contains(msg, e.dir) || strings.Contains(msg, e.root) {
					t.Fatalf("%s: error names more than the file: %v", what, err)
				}
			}
			w, err := e.o.OpenWritable(ctx, e.dir)
			if err == nil {
				w.Close()
			}
			check("OpenWritable", err)
			check("CheckFormat", e.o.CheckFormat(ctx, e.dir))
			check("CheckCompressions", CheckCompressions(e.dir))
			assertSameTree(t, e.dir, before)
			assertTempRootEmpty(t, e.root)
		})
	}
}

func TestOpenWritableMissingDirectory(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	missing := filepath.Join(filepath.Dir(e.dir), "missing")
	if w, err := e.o.OpenWritable(context.Background(), missing); err == nil {
		w.Close()
		t.Fatal("OpenWritable succeeded on a missing directory")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing directory was created: %v", err)
	}
	if _, err := e.o.OpenSnapshot(context.Background(), missing); err == nil {
		t.Fatal("OpenSnapshot succeeded on a missing directory")
	}
	assertTempRootEmpty(t, e.root)
}

func TestOpenWritableEmptyDirectoryNotCreated(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	empty := filepath.Join(filepath.Dir(e.dir), "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if w, err := e.o.OpenWritable(context.Background(), empty); err == nil {
		w.Close()
		t.Fatal("OpenWritable created a database")
	}
	if _, err := os.Stat(filepath.Join(empty, "CURRENT")); err == nil {
		t.Fatal("a database was created")
	}
}

func TestSymlinkedDirectoryRefused(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	link := filepath.Join(filepath.Dir(e.dir), "link")
	if err := os.Symlink(e.dir, link); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := e.o.OpenSnapshot(ctx, link); err == nil {
		t.Fatal("snapshot of a symlink")
	}
	if _, err := e.o.OpenWritable(ctx, link); err == nil {
		t.Fatal("writable open of a symlink")
	}
	if err := e.o.CheckFormat(ctx, link); err == nil {
		t.Fatal("format check of a symlink")
	}
}

func TestScanPrefixOrder(t *testing.T) {
	f := fsxtest.New(t)
	root := filepath.Join(t.TempDir(), "snapshots")
	o, err := New(root, f)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "leveldb")
	makeDB(t, dir, opt.SnappyCompression, map[string]string{
		"_https://example.com\x00\x01k": "other origin",
		"VERSION":                       "1",
		"_https://claude.ai\x00\x01zz":  "last",
		"_https://claude.ai\x00\x01!":   "first",
	})
	r, err := o.OpenSnapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var keys []string
	if err := r.Scan(localstorage.KeyPrefix(), func(k, _ []byte) error {
		keys = append(keys, string(k))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(keys) != len(items)+2 {
		t.Fatalf("got %d keys, want %d", len(keys), len(items)+2)
	}
	if !slices.IsSorted(keys) {
		t.Fatalf("keys not in order: %q", keys)
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, string(localstorage.KeyPrefix())) {
			t.Fatalf("key outside prefix: %q", k)
		}
	}
	stop := errors.New("stop")
	n := 0
	if err := r.Scan(nil, func(_, _ []byte) error { n++; return stop }); !errors.Is(err, stop) || n != 1 {
		t.Fatalf("scan did not stop: %v after %d", err, n)
	}
}

func TestCallsAfterClose(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	ctx := context.Background()
	r, err := e.o.OpenSnapshot(ctx, e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	w, err := e.o.OpenWritable(ctx, e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var b kv.Batch
	b.Put([]byte("k"), []byte("v"))
	for name, s := range map[string]kv.Reader{"snapshot": r, "writer": w} {
		if _, _, err := s.Get([]byte("k")); !errors.Is(err, kv.ErrClosed) {
			t.Errorf("%s Get: %v", name, err)
		}
		if err := s.Scan(nil, func(_, _ []byte) error { return nil }); !errors.Is(err, kv.ErrClosed) {
			t.Errorf("%s Scan: %v", name, err)
		}
		if err := s.Close(); !errors.Is(err, kv.ErrClosed) {
			t.Errorf("%s Close: %v", name, err)
		}
	}
	if err := w.Write(&b); !errors.Is(err, kv.ErrClosed) {
		t.Errorf("Write: %v", err)
	}
}

func TestCanceledContext(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.o.OpenSnapshot(ctx, e.dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenSnapshot: %v", err)
	}
	if _, err := e.o.OpenWritable(ctx, e.dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenWritable: %v", err)
	}
	if err := e.o.CheckFormat(ctx, e.dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("CheckFormat: %v", err)
	}
	assertTempRootEmpty(t, e.root)
}

func TestNewRefusesRealPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	for _, p := range []string{
		filepath.Join(home, "Library", "Application Support", "Claude", "tmp"),
		filepath.Join(home, "Library", "Application Support", "paritydesk", "tmp"),
		"/Applications/x",
	} {
		if _, err := New(p, fsxtest.New(t)); err == nil {
			t.Errorf("New accepted %s", p)
		}
	}
}

// liveTable builds makeDB's database in a new directory and returns the
// directory and the path of its first live table.
func liveTable(t *testing.T, c opt.Compression) (dir, table string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "db")
	makeDB(t, dir, c, nil)
	return dir, tables(t, dir)[0]
}

func TestCheckCompressions(t *testing.T) {
	tests := []struct {
		name    string
		comp    opt.Compression
		mutate  func(b []byte) []byte // the live table's new content; nil keeps it
		wantErr bool
	}{
		{name: "snappy", comp: opt.SnappyCompression},
		{name: "none", comp: opt.NoCompression},
		{name: "truncated footer", comp: opt.SnappyCompression, mutate: func(b []byte) []byte { return b[:20] }, wantErr: true},
		{name: "truncated body", comp: opt.NoCompression, mutate: func(b []byte) []byte { return b[len(b)-footerLen-3:] }, wantErr: true},
		{name: "bad magic", comp: opt.SnappyCompression, mutate: func(b []byte) []byte { b[len(b)-1] ^= 0xff; return b }, wantErr: true},
		{name: "bad checksum", comp: opt.NoCompression, mutate: func(b []byte) []byte { b[0] ^= 0xff; return b }, wantErr: true},
		{name: "empty", comp: opt.SnappyCompression, mutate: func([]byte) []byte { return nil }, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, table := liveTable(t, tc.comp)
			if tc.mutate != nil {
				b, err := os.ReadFile(table)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(table, tc.mutate(b), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := hashTree(t, dir)
			err := CheckCompressions(dir)
			assertSameTree(t, dir, before)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want an error")
			}
			if errors.Is(err, kv.ErrUnknownCompression) {
				t.Fatalf("a malformed table reported as unknown compression: %v", err)
			}
			if !strings.Contains(err.Error(), filepath.Base(table)) || strings.Contains(err.Error(), dir) {
				t.Fatalf("error must name only the file: %v", err)
			}
		})
	}
	t.Run("no tables", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "db")
		db, err := leveldb.OpenFile(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := CheckCompressions(dir); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("no manifest", func(t *testing.T) {
		_, table := liveTable(t, opt.SnappyCompression)
		dir := t.TempDir()
		b, err := os.ReadFile(table)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(table)), b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CheckCompressions(dir); err == nil {
			t.Fatal("tables without a manifest were accepted")
		}
	})
}

// TestUnlistedTablesIgnored: table files the manifest does not list, as
// the C++ library leaves an unfinished compaction output when it closes
// during a compaction (empty, or cut short before its footer), are
// ignored by the format check and do not stop OpenWritable, as neither
// library reads them. A table the manifest lists but the directory lacks
// is refused before anything is written.
func TestUnlistedTablesIgnored(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	live, err := os.ReadFile(tables(t, e.dir)[0])
	if err != nil {
		t.Fatal(err)
	}
	orphans := map[string][]byte{
		"000097.ldb": nil,
		"000098.ldb": make([]byte, 4448),
		"000099.ldb": live[:len(live)/2],
		"000096.sst": live[:len(live)-1],
	}
	for name, b := range orphans {
		if err := os.WriteFile(filepath.Join(e.dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := CheckCompressions(e.dir); err != nil {
		t.Fatalf("CheckCompressions: %v", err)
	}
	if err := e.o.CheckFormat(ctx, e.dir); err != nil {
		t.Fatalf("CheckFormat: %v", err)
	}
	w, err := e.o.OpenWritable(ctx, e.dir)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	b := &kv.Batch{}
	b.Put([]byte("k"), []byte("v"))
	if err := errors.Join(w.Write(b), w.Close()); err != nil {
		t.Fatal(err)
	}
	r, err := e.o.OpenSnapshot(ctx, e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, err := r.Get([]byte("k")); err != nil || !ok || string(v) != "v" {
		t.Fatalf("Get after the write: %q %v %v", v, ok, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	t.Run("table the manifest deleted", func(t *testing.T) {
		// A compaction records its input tables as deleted; a copy of
		// one left on disk, cut short, is ignored as well.
		e := newEnv(t, opt.SnappyCompression)
		old := tables(t, e.dir)[0]
		data, err := os.ReadFile(old)
		if err != nil {
			t.Fatal(err)
		}
		db, err := leveldb.OpenFile(e.dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(db.Put(lsKey(t, items[0].name), []byte("x"), nil), db.CompactRange(util.Range{}), db.Close()); err != nil {
			t.Fatal(err)
		}
		m, err := readManifest(e.dir, true)
		if err != nil {
			t.Fatal(err)
		}
		num, _, _ := parseFileName(filepath.Base(old))
		if slices.Contains(m.tableNums(), num) {
			t.Fatalf("table %s is still live after the compaction", filepath.Base(old))
		}
		if err := os.WriteFile(old, data[:len(data)-1], 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CheckCompressions(e.dir); err != nil {
			t.Fatalf("CheckCompressions: %v", err)
		}
		w, err := e.o.OpenWritable(ctx, e.dir)
		if err != nil {
			t.Fatalf("OpenWritable: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("listed table missing", func(t *testing.T) {
		e := newEnv(t, opt.SnappyCompression)
		table := tables(t, e.dir)[0]
		if err := os.Remove(table); err != nil {
			t.Fatal(err)
		}
		before := hashTree(t, e.dir)
		name := strings.TrimSuffix(filepath.Base(table), filepath.Ext(table))
		for what, err := range map[string]error{
			"CheckCompressions": CheckCompressions(e.dir),
			"CheckFormat":       e.o.CheckFormat(ctx, e.dir),
		} {
			if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), e.dir) {
				t.Fatalf("%s: %v, want an error naming table %s only", what, err, name)
			}
		}
		if w, err := e.o.OpenWritable(ctx, e.dir); err == nil {
			w.Close()
			t.Fatal("OpenWritable accepted a database missing a live table")
		}
		assertSameTree(t, e.dir, before)
		assertTempRootEmpty(t, e.root)
	})
}

func TestCheckCompressionsRefusesSymlinkedTable(t *testing.T) {
	_, src := liveTable(t, opt.SnappyCompression)
	dir, table := liveTable(t, opt.SnappyCompression)
	if err := os.Remove(table); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, table); err != nil {
		t.Fatal(err)
	}
	if err := CheckCompressions(dir); err == nil || !strings.Contains(err.Error(), filepath.Base(table)) {
		t.Fatalf("got %v", err)
	}
}

func TestCheckCompressionsMissingDirectory(t *testing.T) {
	if err := CheckCompressions(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("want an error")
	}
}

func FuzzCheckTable(f *testing.F) {
	for _, c := range []opt.Compression{opt.SnappyCompression, opt.NoCompression} {
		d := filepath.Join(f.TempDir(), "db")
		makeDB(f, d, c, nil)
		b, err := os.ReadFile(tables(f, d)[0])
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Add([]byte{})
	f.Add(make([]byte, footerLen))
	f.Fuzz(func(t *testing.T, b []byte) {
		err := checkTable(bytes.NewReader(b), int64(len(b)))
		if err != nil && !errors.Is(err, errMalformed) && !errors.Is(err, kv.ErrUnknownCompression) {
			// Short reads are the only other failures.
			if !strings.Contains(err.Error(), "read") {
				t.Fatalf("unexpected error kind: %v", err)
			}
		}
	})
}

// TestSnapshotWithTwoJournals covers a directory holding two journal files,
// as left by an unclean exit or a memtable flush in progress. goleveldb's
// read-only recovery fails on it with io.EOF; OpenSnapshot then opens its
// private clone read-write. The original stays untouched.
func TestSnapshotWithTwoJournals(t *testing.T) {
	e := newEnv(t, opt.SnappyCompression)
	logs, err := filepath.Glob(filepath.Join(e.dir, "*.log"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("no journal in the test database: %v %v", logs, err)
	}
	maxNum := 0
	for _, l := range logs {
		var n int
		if _, err := fmt.Sscanf(filepath.Base(l), "%06d.log", &n); err == nil && n > maxNum {
			maxNum = n
		}
	}
	extra := filepath.Join(e.dir, fmt.Sprintf("%06d.log", maxNum+10))
	if err := os.WriteFile(extra, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The read-only path alone fails on this layout (on a throwaway copy).
	probe := filepath.Join(t.TempDir(), "probe")
	copyDir(t, e.dir, probe)
	if err := os.Remove(filepath.Join(probe, "LOCK")); err != nil {
		t.Fatal(err)
	}
	if db, err := leveldb.OpenFile(probe, snapshotOptions()); err == nil {
		db.Close()
		t.Log("read-only open succeeded; the goleveldb journal bug may be fixed")
	}
	before := hashTree(t, e.dir)
	r, err := e.o.OpenSnapshot(context.Background(), e.dir)
	if err != nil {
		t.Fatalf("snapshot with two journals: %v", err)
	}
	for _, it := range items {
		raw, found, err := r.Get(lsKey(t, it.name))
		if err != nil || !found {
			t.Fatalf("%s: found=%v err=%v", it.name, found, err)
		}
		if got, err := localstorage.DecodeText(raw); err != nil || got != it.value {
			t.Fatalf("%s: got %q, %v", it.name, got, err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	assertSameTree(t, e.dir, before)
	assertTempRootEmpty(t, e.root)
	assertCallsConfined(t, e)
}

// copyDir copies the regular files of src into a new directory dst.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	es, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range es {
		if !de.Type().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, de.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, de.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
