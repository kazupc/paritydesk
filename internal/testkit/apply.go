package testkit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
)

// Apply folds edits onto folder the way plan.Compose and apply would,
// without depending on them: edits are grouped per location in order of
// first appearance and applied in order to the original bytes. It fails
// the test when an edit's BaseHash does not match, when set or delete
// edits of different rows overlap on one location, or when EditRemove is
// combined with another edit.
func Apply(t testing.TB, folder string, edits []plan.Edit) {
	t.Helper()
	var order []plan.Location
	byLoc := map[plan.Location][]plan.Edit{}
	for _, e := range edits {
		if _, ok := byLoc[e.Loc]; !ok {
			order = append(order, e.Loc)
		}
		byLoc[e.Loc] = append(byLoc[e.Loc], e)
	}
	var db *leveldb.DB
	defer func() {
		if db != nil {
			closeDB(t, db)
		}
	}()
	for _, loc := range order {
		es := byLoc[loc]
		checkOverlap(t, loc, es)
		if loc.LSKey != "" {
			if db == nil {
				db = openDB(t, folder)
			}
			applyLS(t, db, loc, es)
			continue
		}
		applyFile(t, folder, loc, es)
	}
}

func checkOverlap(t testing.TB, loc plan.Location, es []plan.Edit) {
	t.Helper()
	for i, a := range es {
		if a.Kind == plan.EditRemove && len(es) > 1 {
			t.Fatalf("testkit: remove combined with other edits on %+v", loc)
		}
		for _, b := range es[i+1:] {
			if a.Row == b.Row || !pathEdit(a) || !pathEdit(b) {
				continue
			}
			if isPrefix(a.Path, b.Path) || isPrefix(b.Path, a.Path) {
				t.Fatalf("testkit: rows %s and %s edit overlapping paths of %+v", a.Row, b.Row, loc)
			}
		}
	}
}

func pathEdit(e plan.Edit) bool { return e.Kind == plan.EditSet || e.Kind == plan.EditDelete }

func isPrefix(p, q jsonx.Path) bool {
	if len(p) > len(q) {
		return false
	}
	for i := range p {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}

// fold applies es to doc and reports the result and whether the location
// still exists. base is the hash every edit must carry.
func fold(t testing.TB, loc plan.Location, orig []byte, present bool, base string, es []plan.Edit) ([]byte, bool) {
	t.Helper()
	doc := orig
	for _, e := range es {
		if e.BaseHash != base {
			t.Fatalf("testkit: edit on %+v computed against other bytes", loc)
		}
		var err error
		switch e.Kind {
		case plan.EditSet:
			if !present {
				t.Fatalf("testkit: set on absent %+v", loc)
			}
			doc, err = jsonx.Set(doc, e.Path, e.Value)
		case plan.EditDelete:
			doc, err = jsonx.Delete(doc, e.Path)
			if errors.Is(err, jsonx.ErrNotFound) {
				err = nil
			}
		case plan.EditReplace:
			doc, present = append([]byte(nil), e.Value...), true
		case plan.EditRemove:
			return nil, false
		default:
			t.Fatalf("testkit: unknown edit kind %q", e.Kind)
		}
		if err != nil {
			t.Fatalf("testkit: edit %s %v on %+v: %v", e.Kind, e.Path, loc, err)
		}
	}
	return doc, present
}

func applyFile(t testing.TB, folder string, loc plan.Location, es []plan.Edit) {
	t.Helper()
	if strings.Contains(loc.File, "..") || filepath.IsAbs(loc.File) {
		t.Fatalf("testkit: unsafe path %q", loc.File)
	}
	p := filepath.Join(folder, filepath.FromSlash(loc.File))
	orig, err := os.ReadFile(p)
	present := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	base := ""
	if present {
		base = model.HashBytes(orig)
	}
	doc, keep := fold(t, loc, orig, present, base, es)
	if !keep {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		return
	}
	mode := os.FileMode(0o600)
	for _, e := range es {
		if e.Kind == plan.EditReplace && e.Mode != 0 {
			mode = e.Mode
		}
	}
	if present {
		if fi, err := os.Stat(p); err == nil {
			mode = fi.Mode().Perm()
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, doc, mode); err != nil {
		t.Fatal(err)
	}
}

func applyLS(t testing.TB, db *leveldb.DB, loc plan.Location, es []plan.Edit) {
	t.Helper()
	key, err := localstorage.Key(loc.LSKey)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.Get(key, nil)
	present := err == nil
	if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
		t.Fatal(err)
	}
	var text []byte
	if present {
		s, err := localstorage.DecodeText(stored)
		if err != nil {
			t.Fatal(err)
		}
		text = []byte(s)
	}
	base := ""
	if present {
		base = model.HashBytes(stored)
	}
	doc, keep := fold(t, loc, text, present, base, es)
	wo := &opt.WriteOptions{Sync: true}
	if !keep {
		err = db.Delete(key, wo)
	} else {
		err = db.Put(key, localstorage.EncodeString(string(doc)), wo)
	}
	if err != nil {
		t.Fatal(err)
	}
}
