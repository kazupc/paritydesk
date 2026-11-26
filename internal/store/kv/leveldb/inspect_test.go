package leveldb

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/kazupc/paritydesk/internal/store/idb/idbtest"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

func dirOpener(dir string) func(string) (io.ReadCloser, error) {
	return func(name string) (io.ReadCloser, error) { return os.Open(filepath.Join(dir, name)) }
}

func idbLayout(snappy bool) idbtest.Layout {
	var entries []idbtest.TableEntry
	for i := 0; i < 100; i++ {
		entries = append(entries, idbtest.TableEntry{Op: idbtest.Put(idbtest.DataKey(6, 1, idbtest.FakeID(i)), bytes.Repeat([]byte("v"), 40)), Seq: uint64(i + 1)})
	}
	return idbtest.Layout{
		Tables: []idbtest.Table{{Num: 5, Level: 1, Entries: entries, Snappy: snappy, BlockSize: 256}, {Num: 4, Entries: entries[:3]}},
		Logs:   []idbtest.Log{{Num: 6}}, PrevLogNum: 2,
	}
}

func TestInspectManifest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	sizes, err := idbtest.Write(dir, idbLayout(true))
	if err != nil {
		t.Fatal(err)
	}
	info, err := InspectManifest(dir, dirOpener(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Comparator != "idb_cmp1" || info.LogNum != 6 || info.PrevLogNum != 2 || len(info.Tables) != 2 {
		t.Fatalf("info = %+v", info)
	}
	if info.Tables[0].Level != 0 || info.Tables[0].Num != 4 || info.Tables[1].Num != 5 {
		t.Fatalf("order = %+v", info.Tables)
	}
	for _, tf := range info.Tables {
		if tf.Size != sizes[tf.Num] || tf.Size == 0 {
			t.Fatalf("table %d size %d, want %d", tf.Num, tf.Size, sizes[tf.Num])
		}
	}
	// readManifest keeps refusing this comparator (Local Storage rule).
	if _, err := readManifest(dir, true); err == nil {
		t.Fatal("readManifest accepted idb_cmp1")
	}
}

func TestDecodeManifestKeepsSizes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	sizes, err := idbtest.Write(dir, idbLayout(false))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "MANIFEST-000001"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := decodeManifest(f, true)
	if err != nil {
		t.Fatal(err)
	}
	for id, ti := range m.tables {
		if ti.size != sizes[id.num] {
			t.Fatalf("table %d size %d, want %d", id.num, ti.size, sizes[id.num])
		}
	}
}

func TestInspectManifestRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		l    func(*idbtest.Layout)
		want error
	}{
		{"CURRENT.bak only", func(l *idbtest.Layout) { l.NoCurrent = true }, ErrCurrent},
		{"no newline", func(l *idbtest.Layout) { l.Current = "MANIFEST-000001" }, ErrCurrent},
		{"short number", func(l *idbtest.Layout) { l.Current = "MANIFEST-1\n" }, ErrCurrent},
		{"extra content", func(l *idbtest.Layout) { l.Current = "MANIFEST-000001\nx" }, ErrCurrent},
		{"unknown tag", func(l *idbtest.Layout) { l.UnknownTag = true }, ErrManifestUnreadable},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "db")
			l := idbLayout(false)
			c.l(&l)
			if _, err := idbtest.Write(dir, l); err != nil {
				t.Fatal(err)
			}
			if _, err := InspectManifest(dir, dirOpener(dir)); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func tableTypes(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	return TableBlockTypes(f, fi.Size())
}

func TestTableBlockTypes(t *testing.T) {
	for _, snappy := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "db")
		if _, err := idbtest.Write(dir, idbLayout(snappy)); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "000005.ldb")
		types, err := tableTypes(t, p)
		if err != nil || len(types) < 2 {
			t.Fatalf("types = %v err = %v", types, err)
		}
		want := byte(0)
		if snappy {
			want = 1
		}
		for _, ty := range types {
			if ty != want {
				t.Fatalf("snappy=%v: type %d", snappy, ty)
			}
		}
		if err := idbtest.SetBlockType(p, 2); err != nil {
			t.Fatal(err)
		}
		types, err = tableTypes(t, p)
		if err != nil || types[0] != 2 {
			t.Fatalf("injected type: %v %v", types, err)
		}
		if err := idbtest.FlipBlockByte(p); err != nil {
			t.Fatal(err)
		}
		if _, err := tableTypes(t, p); !errors.Is(err, ErrTableMalformed) {
			t.Fatalf("flipped: %v", err)
		}
	}
	if _, err := TableBlockTypes(bytes.NewReader([]byte("short")), 5); !errors.Is(err, ErrTableMalformed) || errors.Is(err, kv.ErrUnknownCompression) {
		t.Fatalf("short: %v", err)
	}
}

func FuzzManifestInspect(f *testing.F) {
	dir := filepath.Join(f.TempDir(), "db")
	if _, err := idbtest.Write(dir, idbLayout(false)); err != nil {
		f.Fatal(err)
	}
	seed, err := os.ReadFile(filepath.Join(dir, "MANIFEST-000001"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte("MANIFEST-000001\n"), seed)
	f.Fuzz(func(t *testing.T, current, manifest []byte) {
		open := func(name string) (io.ReadCloser, error) {
			switch name {
			case "CURRENT":
				return io.NopCloser(bytes.NewReader(current)), nil
			case "MANIFEST-000001":
				return io.NopCloser(bytes.NewReader(manifest)), nil
			}
			return nil, os.ErrNotExist
		}
		_, _ = InspectManifest("db", open)
	})
}
