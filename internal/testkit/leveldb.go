package testkit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// LevelDBRel is the Local Storage database inside a data folder.
const LevelDBRel = "Local Storage/leveldb"

// metaValue is a synthetic META:https://claude.ai value.
var metaValue = []byte{0x08, 0x80, 0x01}

// openDB opens (creating) the folder's Local Storage database for writing.
// Only for fixtures in the test's temp tree.
func openDB(t testing.TB, folder string) *leveldb.DB {
	t.Helper()
	dir := filepath.Join(folder, filepath.FromSlash(LevelDBRel))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := leveldb.OpenFile(dir, &opt.Options{})
	if err != nil {
		t.Fatalf("testkit: open fixture leveldb: %v", err)
	}
	return db
}

// WriteLS creates or updates the folder's Local Storage with the given
// items (name -> text), Chromium-encoded, plus the origin's META key.
func WriteLS(t testing.TB, folder string, items map[string]string) {
	t.Helper()
	db := openDB(t, folder)
	defer closeDB(t, db)
	b := new(leveldb.Batch)
	b.Put(localstorage.MetaKey, metaValue)
	for name, text := range items {
		k, err := localstorage.Key(name)
		if err != nil {
			t.Fatal(err)
		}
		b.Put(k, localstorage.EncodeString(text))
	}
	if err := db.Write(b, &opt.WriteOptions{Sync: true}); err != nil {
		t.Fatal(err)
	}
}

func closeDB(t testing.TB, db *leveldb.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// ReadLS returns every claude.ai item of the folder's Local Storage as
// decoded text, read through a snapshot copy. A folder without Local
// Storage gives an empty map.
func ReadLS(t testing.TB, folder string) map[string]string {
	t.Helper()
	out := map[string]string{}
	dir := filepath.Join(folder, filepath.FromSlash(LevelDBRel))
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return out
	}
	r, err := Opener(t).OpenSnapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	err = r.Scan(localstorage.KeyPrefix(), func(k, v []byte) error {
		name, err := localstorage.ParseKey(k)
		if err != nil {
			return err
		}
		text, err := localstorage.DecodeText(v)
		if err != nil {
			return err
		}
		out[name] = text
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Opener returns a minimal goleveldb-backed kv.Opener for adapter tests.
// OpenSnapshot copies the directory (without LOCK) into t.TempDir() and
// opens the copy read-only; OpenWritable is refused; CheckFormat returns
// nil.
func Opener(t testing.TB) kv.Opener { return &snapOpener{t: t} }

type snapOpener struct {
	t testing.TB
	n atomic.Int64
}

// OpenSnapshot implements kv.Opener.
func (o *snapOpener) OpenSnapshot(ctx context.Context, dir string) (kv.Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("testkit: no database: %w", err)
	}
	if !fi.IsDir() {
		return nil, errors.New("testkit: database path is not a directory")
	}
	dst := filepath.Join(o.t.TempDir(), fmt.Sprintf("snap-%d", o.n.Add(1)))
	if err := copyDir(dir, dst); err != nil {
		return nil, err
	}
	db, err := leveldb.OpenFile(dst, &opt.Options{ReadOnly: true, ErrorIfMissing: true, Strict: opt.NoStrict})
	if err != nil {
		return nil, fmt.Errorf("testkit: open snapshot: %w", err)
	}
	return &reader{db: db}, nil
}

// OpenWritable implements kv.Opener; the test opener never writes.
func (o *snapOpener) OpenWritable(context.Context, string) (kv.Writer, error) {
	return nil, kv.ErrReadOnly
}

// CheckFormat implements kv.Opener.
func (o *snapOpener) CheckFormat(context.Context, string) error { return nil }

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.Name() == "LOCK" || !e.Type().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

type reader struct{ db *leveldb.DB }

// Get implements kv.Reader.
func (r *reader) Get(key []byte) ([]byte, bool, error) {
	v, err := r.db.Get(key, nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// Scan implements kv.Reader.
func (r *reader) Scan(prefix []byte, fn func(key, value []byte) error) error {
	it := r.db.NewIterator(util.BytesPrefix(prefix), nil)
	defer it.Release()
	for it.Next() {
		if err := fn(append([]byte(nil), it.Key()...), append([]byte(nil), it.Value()...)); err != nil {
			return err
		}
	}
	return it.Error()
}

// Close implements kv.Reader.
func (r *reader) Close() error { return r.db.Close() }
