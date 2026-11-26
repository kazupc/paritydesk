package profile

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources/configjson"
)

// failingList fails for one folder.
type failingList struct {
	fakeIDs
	bad string
}

func (f *failingList) list(folder string) ([]model.Replica, error) {
	if folder == f.bad {
		return nil, errors.New("permission denied")
	}
	return f.fakeIDs.list(folder)
}

func TestScanLenient(t *testing.T) {
	fsys := fsxtest.New(t)
	root := t.TempDir()
	for _, n := range []string{"Claude", "Claude-home", "Claude-locked"} {
		mkdir(t, filepath.Join(root, n))
	}
	p := func(n string) string { return filepath.Join(root, n) }
	ids := &failingList{fakeIDs: fakeIDs{ident: map[string]configjson.Identity{p("Claude"): {Account: acct(1)},
		p("Claude-home"): {Account: acct(2)}, p("Claude-locked"): {Account: acct(3)}}}, bad: p("Claude-locked")}
	sc := Scanner{FS: fsys, DataRoot: root, ReadIdentity: ids.read, ListReplicas: ids.list}
	if _, err := sc.Scan(context.Background()); err == nil {
		t.Fatal("Scan succeeded with an unreadable folder")
	}
	got, err := sc.ScanLenient(context.Background())
	if err != nil || len(got) != 3 {
		t.Fatalf("ScanLenient = %v, %v", got, err)
	}
	for _, f := range got {
		switch f.Name {
		case "Claude-locked":
			if f.ScanErr != ScanErrUnreadable || f.Identity != nil || f.Path != p("Claude-locked") {
				t.Errorf("locked = %+v", f)
			}
		default:
			if f.ScanErr != "" || f.Identity == nil {
				t.Errorf("%s = %+v", f.Name, f)
			}
		}
	}
	f, err := sc.Identify(context.Background(), "Claude-home")
	if err != nil || f.Identity == nil || f.Identity.Account != acct(2) || f.ID.Ino == 0 {
		t.Fatalf("Identify = %+v, %v", f, err)
	}
	for _, bad := range []string{"Other", "Claude-missing", "../Claude", "Claude-home/x"} {
		if _, err := sc.Identify(context.Background(), bad); err == nil {
			t.Errorf("Identify(%q) succeeded", bad)
		}
	}
	if _, err := (Scanner{FS: fsys, DataRoot: filepath.Join(root, "nope"), ReadIdentity: ids.read, ListReplicas: ids.list}).ScanLenient(context.Background()); err == nil {
		t.Error("an unreadable data root must fail")
	}
}
