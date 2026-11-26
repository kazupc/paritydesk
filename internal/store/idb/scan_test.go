package idb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/store/idb/idbtest"
)

func testSpec() Spec {
	return Spec{Dir: idbtest.Dir, BackendPrefix: idbtest.BackendPrefix, Database: idbtest.Database,
		ObjectStore: idbtest.ObjectStore, Key: idbtest.PinKey, BlinkMin: 19, BlinkMax: 21, V8Min: 13, V8Max: 16,
		MaxValueBytes: 1 << 20, MaxCopyBytes: 64 << 20, MaxCloneBytes: 2 << 30, MaxFiles: 512, MaxRead: 10 * time.Second}
}

// build writes a layout as the IndexedDB directory of a new data folder
// and returns the folder.
func build(t *testing.T, l idbtest.Layout) string {
	t.Helper()
	folder := t.TempDir()
	if _, err := idbtest.Write(filepath.Join(folder, idbtest.Dir), l); err != nil {
		t.Fatal(err)
	}
	return folder
}

func pinText(ids ...string) string { return idbtest.PinJSON(ids) }

func mustRead(t *testing.T, folder string) Value {
	t.Helper()
	v, err := ReadString(context.Background(), folder, testSpec())
	if err != nil {
		t.Fatalf("ReadString: %v", err)
	}
	return v
}

func wantErr(t *testing.T, folder string, want error) {
	t.Helper()
	_, err := ReadString(context.Background(), folder, testSpec())
	if !errors.Is(err, want) {
		t.Fatalf("ReadString err = %v, want %v", err, want)
	}
}

var (
	a1 = idbtest.FakeID(1)
	a2 = idbtest.FakeID(2)
)

func meta() []idbtest.Op {
	return []idbtest.Op{
		idbtest.Put(idbtest.DatabaseNameKey(idbtest.Origin, idbtest.Database), idbtest.Varint(6)),
		idbtest.Put(idbtest.ObjectStoreNameKey(6, idbtest.ObjectStore), idbtest.Varint(1)),
	}
}

func dk() []byte { return idbtest.DataKey(6, 1, idbtest.PinKey) }

func TestValueOnlyInLog(t *testing.T) {
	v := mustRead(t, build(t, idbtest.PinDB(idbtest.PinValue([]string{a1}))))
	if v.Text != pinText(a1) || !v.DBFromMetadata || !v.OSFromMetadata || v.DB != 6 || v.OS != 1 || v.Wrapped != "" ||
		v.Blink != 21 || v.V8 != 16 || v.Seq != 10 || v.TailDropped {
		t.Fatalf("value = %+v", v)
	}
}

func TestValueOnlyInTable(t *testing.T) {
	for _, snap := range []bool{false, true} {
		entries := []idbtest.TableEntry{
			{Op: meta()[0], Seq: 1}, {Op: meta()[1], Seq: 2},
			{Op: idbtest.Put(dk(), idbtest.PinValue([]string{a1, a2})), Seq: 5},
		}
		// Noise so the table holds several blocks.
		for i := 0; i < 200; i++ {
			entries = append(entries, idbtest.TableEntry{Op: idbtest.Put(idbtest.DataKey(6, 1, "other-"+idbtest.FakeID(i)), []byte("xxxxxxxxxxxxxxxxxxxxxxxx")), Seq: uint64(100 + i)})
		}
		f := build(t, idbtest.Layout{Tables: []idbtest.Table{{Num: 5, Entries: entries, Snappy: snap, BlockSize: 512}},
			Logs: []idbtest.Log{{Num: 6}}})
		if v := mustRead(t, f); v.Text != pinText(a1, a2) {
			t.Fatalf("snappy=%v: %q", snap, v.Text)
		}
	}
}

func TestLogOverridesTable(t *testing.T) {
	f := build(t, idbtest.Layout{
		Tables: []idbtest.Table{{Num: 5, Entries: []idbtest.TableEntry{{Op: idbtest.Put(dk(), idbtest.PinValue([]string{a1})), Seq: 5}}}},
		Logs:   []idbtest.Log{{Num: 6, Batches: []idbtest.Batch{{Seq: 9, Ops: []idbtest.Op{idbtest.Put(dk(), idbtest.PinValue([]string{a2}))}}}}},
	})
	v := mustRead(t, f)
	if v.Text != pinText(a2) || v.DBFromMetadata {
		t.Fatalf("value = %+v", v)
	}
}

func TestNewestDeleteAndPutAfterDelete(t *testing.T) {
	del := build(t, idbtest.Layout{Logs: []idbtest.Log{{Num: 3, Batches: []idbtest.Batch{
		{Seq: 1, Ops: []idbtest.Op{idbtest.Put(dk(), idbtest.PinValue([]string{a1}))}},
		{Seq: 2, Ops: []idbtest.Op{idbtest.Del(dk())}},
	}}}})
	wantErr(t, del, ErrKeyAbsent)
	again := build(t, idbtest.Layout{Logs: []idbtest.Log{{Num: 3, Batches: []idbtest.Batch{
		{Seq: 1, Ops: []idbtest.Op{idbtest.Put(dk(), idbtest.PinValue([]string{a1}))}},
		{Seq: 2, Ops: []idbtest.Op{idbtest.Del(dk())}},
		{Seq: 3, Ops: []idbtest.Op{idbtest.Put(dk(), idbtest.PinValue([]string{a2}))}},
	}}}})
	if v := mustRead(t, again); v.Text != pinText(a2) {
		t.Fatal(v.Text)
	}
}

func TestDirAbsentAndBackend(t *testing.T) {
	folder := t.TempDir()
	wantErr(t, folder, ErrDirAbsent)
	if err := os.MkdirAll(filepath.Join(folder, "IndexedDB", "https_claude.ai_0.indexeddb.blob"), 0o700); err != nil {
		t.Fatal(err)
	}
	wantErr(t, folder, ErrDirAbsent)
	if err := os.WriteFile(filepath.Join(folder, "IndexedDB", "https_claude.ai_0.indexeddb.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr(t, folder, ErrBackend)
}

func TestResolution(t *testing.T) {
	v1, v2 := idbtest.PinValue([]string{a1}), idbtest.PinValue([]string{a2})
	cases := []struct {
		name string
		ops  []idbtest.Op
		want string
		err  error
	}{
		{"metadata picks one of two pairs", append(meta(), idbtest.Put(dk(), v1), idbtest.Put(idbtest.DataKey(7, 1, idbtest.PinKey), v2)), pinText(a1), nil},
		{"no metadata, one pair", []idbtest.Op{idbtest.Put(idbtest.DataKey(9, 3, idbtest.PinKey), v2)}, pinText(a2), nil},
		{"no metadata, two live pairs", []idbtest.Op{idbtest.Put(dk(), v1), idbtest.Put(idbtest.DataKey(7, 1, idbtest.PinKey), v2)}, "", ErrAmbiguous},
		{"delete-only pair ignored", []idbtest.Op{idbtest.Put(dk(), v1), idbtest.Del(idbtest.DataKey(7, 1, idbtest.PinKey))}, pinText(a1), nil},
		{"two live database names", []idbtest.Op{
			idbtest.Put(idbtest.DatabaseNameKey(idbtest.Origin, idbtest.Database), idbtest.Varint(6)),
			idbtest.Put(idbtest.DatabaseNameKey("https://other.example", idbtest.Database), idbtest.Varint(7)),
			idbtest.Put(dk(), v1)}, "", ErrAmbiguous},
		{"object store under another db ignored", []idbtest.Op{
			idbtest.Put(idbtest.DatabaseNameKey(idbtest.Origin, idbtest.Database), idbtest.Varint(6)),
			idbtest.Put(idbtest.ObjectStoreNameKey(7, idbtest.ObjectStore), idbtest.Varint(2)),
			idbtest.Put(dk(), v1)}, pinText(a1), nil},
		{"exists entry ignored", []idbtest.Op{idbtest.Put(idbtest.ExistsKey(6, 1, idbtest.PinKey), v2), idbtest.Put(dk(), v1)}, pinText(a1), nil},
		{"database id with trailing bytes", []idbtest.Op{
			idbtest.Put(idbtest.DatabaseNameKey(idbtest.Origin, idbtest.Database), []byte{6, 0}), idbtest.Put(dk(), v1)}, "", ErrUnsupported},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ops []idbtest.Op
			ops = append(ops, c.ops...)
			f := build(t, idbtest.Layout{Logs: []idbtest.Log{{Num: 3, Batches: []idbtest.Batch{{Seq: 1, Ops: ops}}}}})
			v, err := ReadString(context.Background(), f, testSpec())
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("err = %v, want %v", err, c.err)
				}
				return
			}
			if err != nil || v.Text != c.want {
				t.Fatalf("v = %+v, err = %v", v, err)
			}
		})
	}
}

func TestManifestAndTableDefects(t *testing.T) {
	val := idbtest.PinValue([]string{a1})
	tbl := func() idbtest.Layout {
		return idbtest.Layout{Tables: []idbtest.Table{{Num: 5, Entries: []idbtest.TableEntry{{Op: idbtest.Put(dk(), val), Seq: 5}}, Snappy: true}},
			Logs: []idbtest.Log{{Num: 6}}}
	}
	t.Run("bytewise comparator", func(t *testing.T) {
		l := tbl()
		l.Comparator = "leveldb.BytewiseComparator"
		wantErr(t, build(t, l), ErrUnsupported)
	})
	t.Run("unknown manifest tag", func(t *testing.T) {
		l := tbl()
		l.UnknownTag = true
		wantErr(t, build(t, l), ErrUnsupported)
	})
	t.Run("only CURRENT.bak", func(t *testing.T) {
		l := tbl()
		l.NoCurrent = true
		wantErr(t, build(t, l), ErrUnsupported)
	})
	t.Run("odd CURRENT", func(t *testing.T) {
		l := tbl()
		l.Current = "MANIFEST-1\n"
		wantErr(t, build(t, l), ErrUnsupported)
	})
	t.Run("table size differs", func(t *testing.T) {
		f := build(t, tbl())
		p := filepath.Join(f, idbtest.Dir, "000005.ldb")
		data, _ := os.ReadFile(p)
		if err := os.WriteFile(p, append(data, 0), 0o600); err != nil {
			t.Fatal(err)
		}
		wantErr(t, f, ErrTorn)
	})
	t.Run("table missing", func(t *testing.T) {
		f := build(t, tbl())
		if err := os.Remove(filepath.Join(f, idbtest.Dir, "000005.ldb")); err != nil {
			t.Fatal(err)
		}
		wantErr(t, f, ErrTorn)
	})
	t.Run("block type 2", func(t *testing.T) {
		f := build(t, tbl())
		if err := idbtest.SetBlockType(filepath.Join(f, idbtest.Dir, "000005.ldb"), 2); err != nil {
			t.Fatal(err)
		}
		wantErr(t, f, ErrUnsupported)
	})
	t.Run("block checksum flipped", func(t *testing.T) {
		f := build(t, tbl())
		if err := idbtest.FlipBlockByte(filepath.Join(f, idbtest.Dir, "000005.ldb")); err != nil {
			t.Fatal(err)
		}
		wantErr(t, f, ErrUnclean)
	})
	t.Run("obsolete table not in the manifest is ignored", func(t *testing.T) {
		f := build(t, idbtest.PinDB(val))
		if err := os.WriteFile(filepath.Join(f, idbtest.Dir, "000099.ldb"), []byte("garbage"), 0o600); err != nil {
			t.Fatal(err)
		}
		if v := mustRead(t, f); v.Text != pinText(a1) {
			t.Fatal(v.Text)
		}
	})
}

// bigBatch is a batch whose record spans more than one log block.
func bigBatch(seq uint64) idbtest.Batch {
	return idbtest.Batch{Seq: seq, Ops: []idbtest.Op{idbtest.Put([]byte("noise"), make([]byte, 40<<10))}}
}

func TestLogTails(t *testing.T) {
	good := idbtest.Batch{Seq: 1, Ops: []idbtest.Op{idbtest.Put(dk(), idbtest.PinValue([]string{a1}))}}
	later := idbtest.Batch{Seq: 2, Ops: []idbtest.Op{idbtest.Put(dk(), idbtest.PinValue([]string{a2}))}}
	cases := []struct {
		name    string
		logs    []idbtest.Log
		want    string
		dropped bool
		err     error
	}{
		{"truncated header at EOF", []idbtest.Log{{Num: 3, Batches: []idbtest.Batch{good, later}, Cut: len(idbtest.BatchBytes(later)) + 3}}, pinText(a1), true, nil},
		{"truncated payload", []idbtest.Log{{Num: 3, Batches: []idbtest.Batch{good, later}, Cut: 5}}, pinText(a1), true, nil},
		{"FIRST without LAST", []idbtest.Log{{Num: 3, Batches: []idbtest.Batch{good, bigBatch(5)}, TruncateTo: 32 << 10}}, pinText(a1), true, nil},
		{"torn in an older log", []idbtest.Log{{Num: 3, Batches: []idbtest.Batch{good, later}, Cut: 5}, {Num: 4}}, "", false, ErrUnclean},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := build(t, idbtest.Layout{Logs: c.logs, LogNum: c.logs[0].Num})
			v, err := ReadString(context.Background(), f, testSpec())
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || v.Text != c.want || v.TailDropped != c.dropped {
				t.Fatalf("v = %+v err = %v", v, err)
			}
		})
	}
	t.Run("checksum error mid-file", func(t *testing.T) {
		f := build(t, idbtest.Layout{Logs: []idbtest.Log{{Num: 3, Batches: []idbtest.Batch{good, later}}}})
		p := filepath.Join(f, idbtest.Dir, "000003.log")
		data, _ := os.ReadFile(p)
		data[10] ^= 0xff
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		wantErr(t, f, ErrUnclean)
	})
}

func TestPendingScopes(t *testing.T) {
	val := idbtest.PinValue([]string{a1})
	undo := func(v []byte) idbtest.Op { return idbtest.Put(idbtest.ScopeUndoKey(7, 1), v) }
	cases := []struct {
		name string
		ops  []idbtest.Op
		err  error
	}{
		{"undo names the data key", []idbtest.Op{undo(idbtest.ProtoBytes(1, idbtest.ProtoBytes(1, dk())))}, ErrPending},
		{"undo names another store's key", []idbtest.Op{undo(idbtest.ProtoBytes(1, idbtest.DataKey(6, 2, "x")))}, nil},
		{"undo holds a store-prefix range start", []idbtest.Op{undo(idbtest.ProtoBytes(3, idbtest.ProtoBytes(1, idbtest.KeyPrefix(6, 1, 1))))}, ErrPending},
		{"unscannable undo", []idbtest.Op{undo([]byte{0x0b, 0x01})}, ErrPending},
		{"live metadata only", []idbtest.Op{idbtest.Put(idbtest.ScopeMetaKey(7), []byte{1})}, nil},
		{"all deleted", []idbtest.Op{undo(idbtest.ProtoBytes(1, dk())), idbtest.Del(idbtest.ScopeUndoKey(7, 1))}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ops := append(meta(), idbtest.Put(dk(), val))
			ops = append(ops, c.ops...)
			f := build(t, idbtest.Layout{Logs: []idbtest.Log{{Num: 3, Batches: []idbtest.Batch{{Seq: 1, Ops: ops}}}}})
			_, err := ReadString(context.Background(), f, testSpec())
			if (c.err == nil && err != nil) || (c.err != nil && !errors.Is(err, c.err)) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
		})
	}
}

func TestTimeBudget(t *testing.T) {
	f := build(t, idbtest.PinDB(idbtest.PinValue([]string{a1})))
	old := openFile
	openFile = func(p string) (*os.File, error) {
		time.Sleep(30 * time.Millisecond)
		return old(p)
	}
	defer func() { openFile = old }()
	s := testSpec()
	s.MaxRead = 20 * time.Millisecond
	if _, err := ReadString(context.Background(), f, s); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadString(ctx, f, testSpec()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestSymlinkRefused(t *testing.T) {
	f := build(t, idbtest.PinDB(idbtest.PinValue([]string{a1})))
	p := filepath.Join(f, idbtest.Dir, "000003.log")
	if err := os.Rename(p, p+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(p+".real", p); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f, ErrUnsupported)
}

func TestReadStringNeverOpensLogFiles(t *testing.T) {
	f := build(t, idbtest.PinDB(idbtest.PinValue([]string{a1})))
	for _, n := range []string{"LOG", "LOG.old", "LOCK"} {
		if err := os.WriteFile(filepath.Join(f, idbtest.Dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var opened []string
	old := openFile
	openFile = func(p string) (*os.File, error) {
		opened = append(opened, filepath.Base(p))
		return old(p)
	}
	defer func() { openFile = old }()
	mustRead(t, f)
	for _, n := range opened {
		if skipFile(n) {
			t.Fatalf("opened %s", n)
		}
	}
}

func TestReasonOf(t *testing.T) {
	cases := map[error]Reason{
		ErrDirAbsent: ReasonDirAbsent, ErrKeyAbsent: ReasonKeyAbsent, ErrAmbiguous: ReasonAmbiguous,
		ErrUnsupported: ReasonUnsupported, ErrBlob: ReasonBlob, ErrVersion: ReasonVersion, ErrTorn: ReasonTorn,
		ErrUnclean: ReasonUnclean, ErrPending: ReasonPending, ErrTooLarge: ReasonTooLarge, ErrBackend: ReasonBackend,
		errors.New("io"): ReasonOther,
	}
	for err, want := range cases {
		if got := ReasonOf(err); got != want {
			t.Errorf("ReasonOf(%v) = %s, want %s", err, got, want)
		}
		if got := ReasonOf(fail("step", err)); got != want {
			t.Errorf("ReasonOf(wrapped %v) = %s, want %s", err, got, want)
		}
	}
}
