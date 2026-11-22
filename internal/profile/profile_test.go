package profile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources/configjson"
	"github.com/kazupc/paritydesk/internal/state"
)

// fakeUUID returns the n-th allow-listed fake UUID, in the same format as
// testkit.FakeUUID. testkit is not imported so these tests do not depend
// on its LevelDB fixtures.
func fakeUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func acct(n int) ids.AccountID { return ids.AccountID(fakeUUID(n)) }
func org(n int) ids.OrgID      { return ids.OrgID(fakeUUID(n)) }
func rep(a, o int) model.Replica {
	return model.Replica{Account: acct(a), Org: org(o)}
}

// fakeIDs serves identities and replicas by folder path.
type fakeIDs struct {
	ident map[string]configjson.Identity
	errs  map[string]error
	reps  map[string][]model.Replica
}

func (f *fakeIDs) read(folder string) (configjson.Identity, error) {
	if err, ok := f.errs[folder]; ok {
		return configjson.Identity{}, err
	}
	if id, ok := f.ident[folder]; ok {
		return id, nil
	}
	return configjson.Identity{}, fs.ErrNotExist
}

func (f *fakeIDs) list(folder string) ([]model.Replica, error) { return f.reps[folder], nil }

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

func readOnlyCalls(calls []string) bool {
	for _, c := range calls {
		switch strings.SplitN(c, " ", 2)[0] {
		case "ID", "GetXattr":
		default:
			return false
		}
	}
	return true
}

func TestScan(t *testing.T) {
	fsys := fsxtest.New(t)
	root := t.TempDir()
	for _, n := range []string{"Claude", "Claude-home", "Claude-noacct", "Claude-broken", "Claude-nocfg", "Other"} {
		mkdir(t, filepath.Join(root, n))
	}
	if err := os.Symlink(filepath.Join(root, "Claude-home"), filepath.Join(root, "Claude-x")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Claude-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := func(n string) string { return filepath.Join(root, n) }
	ids := &fakeIDs{
		ident: map[string]configjson.Identity{p("Claude"): {Account: acct(1)}, p("Claude-home"): {Account: acct(2)}},
		errs: map[string]error{
			p("Claude-noacct"): configjson.ErrNoAccount,
			p("Claude-broken"): errors.New("parse error near " + fakeUUID(9)),
		},
		reps: map[string][]model.Replica{p("Claude-home"): {rep(2, 20)}},
	}
	want := NewMarker("home", rep(2, 20), 3, "base")
	if err := WriteMarker(fsys, p("Claude-home"), want); err != nil {
		t.Fatal(err)
	}
	if err := fsys.SetXattr(p("Claude"), fsx.MarkerXattr, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	fsys.Calls = nil

	sc := Scanner{FS: fsys, DataRoot: root, ReadIdentity: ids.read, ListReplicas: ids.list}
	got, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]Folder{}
	for _, f := range got {
		names = append(names, f.Name)
		byName[f.Name] = f
	}
	if !slices.Equal(names, []string{"Claude", "Claude-broken", "Claude-home", "Claude-noacct", "Claude-nocfg"}) {
		t.Fatalf("folders = %v (symlink, file or other directory listed?)", names)
	}
	if f := byName["Claude"]; !f.Active || f.Identity == nil || f.Identity.Account != acct(1) || f.Marker != nil {
		t.Fatalf("Claude = %+v", f)
	}
	if f := byName["Claude-home"]; f.Active || f.Marker == nil || *f.Marker != want || !slices.Equal(f.Replicas, []model.Replica{rep(2, 20)}) || f.ID.Ino == 0 {
		t.Fatalf("Claude-home = %+v", f)
	}
	errTexts := map[string]string{"Claude-noacct": IdentityNoAccount, "Claude-broken": IdentityUnreadable, "Claude-nocfg": IdentityNoConfig}
	for n, text := range errTexts {
		if f := byName[n]; f.Identity != nil || f.IdentityErr != text {
			t.Errorf("%s: identity %v, err %q, want %q", n, f.Identity, f.IdentityErr, text)
		}
	}
	if !readOnlyCalls(fsys.Calls) {
		t.Fatalf("Scan wrote: %v", fsys.Calls)
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	fsys := fsxtest.New(t)
	dir := t.TempDir()
	if m, err := ReadMarker(fsys, dir); m != nil || err != nil {
		t.Fatalf("absent marker = %v, %v", m, err)
	}
	m := NewMarker("work", rep(1, 10), 4, "abc")
	if err := WriteMarker(fsys, dir, m); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMarker(fsys, dir)
	if err != nil || *got != m {
		t.Fatalf("marker = %+v, %v", got, err)
	}
	for _, bad := range []string{"{", `{"v":2}`} {
		if err := fsys.SetXattr(dir, fsx.MarkerXattr, []byte(bad)); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadMarker(fsys, dir); !errors.Is(err, ErrBadMarker) {
			t.Fatalf("marker %q = %v", bad, err)
		}
	}
}

// world is a resolvable setup: profiles work (account 1, active) and
// home (account 2, parked), with matching state and markers.
type world struct {
	cfg     *config.Config
	root    *state.Root
	folders map[string]*Folder
	hints   Hints
}

func id(n uint64) fsx.FileID { return fsx.FileID{Dev: 1, Ino: n, Birth: t0} }

func newWorld() *world {
	cfg := config.Default()
	cfg.Profiles = []config.Profile{{Alias: "work"}, {Alias: "home"}}
	root := state.NewRoot("/data")
	root.Active = "work"
	root.Profiles["work"] = &state.ProfileState{Alias: "work", Replica: rep(1, 10), FolderID: id(1), MarkerBase: "b1"}
	root.Profiles["home"] = &state.ProfileState{Alias: "home", Replica: rep(2, 20), FolderID: id(2), MarkerBase: "b2"}
	mk := func(alias ids.Alias, r model.Replica, base string) *model.Marker {
		m := NewMarker(alias, r, 1, base)
		return &m
	}
	return &world{
		cfg:  cfg,
		root: root,
		folders: map[string]*Folder{
			"Claude": {Name: "Claude", Path: "/data/Claude", Active: true, Identity: &configjson.Identity{Account: acct(1)},
				Replicas: []model.Replica{rep(1, 10)}, Marker: mk("work", rep(1, 10), "b1"), ID: id(1)},
			"Claude-home": {Name: "Claude-home", Path: "/data/Claude-home", Identity: &configjson.Identity{Account: acct(2)},
				Replicas: []model.Replica{rep(2, 20)}, Marker: mk("home", rep(2, 20), "b2"), ID: id(2)},
		},
	}
}

func (w *world) resolve() (*Resolution, error) {
	var names []string
	for n := range w.folders {
		names = append(names, n)
	}
	slices.Sort(names)
	var fs []Folder
	for _, n := range names {
		fs = append(fs, *w.folders[n])
	}
	return Resolve(w.cfg, w.root, fs, w.hints)
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name  string
		setup func(w *world)
		check func(t *testing.T, r *Resolution)
	}{
		{"bound", nil, func(t *testing.T, r *Resolution) {
			if r.Active == nil || r.Active.Profile.Alias != "work" || r.Active.Folder.Name != "Claude" {
				t.Fatalf("active = %+v", r.Active)
			}
			h := r.Bindings["home"]
			if h.Folder.Name != "Claude-home" || h.Replica != rep(2, 20) || !h.BaseValid || !r.Active.BaseValid {
				t.Fatalf("home = %+v, work valid %v (%s)", h, r.Active.BaseValid, r.Active.BaseInvalidWhy)
			}
			if len(r.Unregistered) != 0 || r.OutsideSwitch || r.PendingAdd != nil {
				t.Fatalf("resolution = %+v", r)
			}
		}},
		{"missing folder", func(w *world) { delete(w.folders, "Claude-home") }, func(t *testing.T, r *Resolution) {
			h := r.Bindings["home"]
			if h.Folder != nil || h.BaseValid || h.BaseInvalidWhy != WhyFolderMissing || h.Replica != rep(2, 20) {
				t.Fatalf("home = %+v", h)
			}
		}},
		{"unregistered folder", func(w *world) {
			w.folders["Claude-old"] = &Folder{Name: "Claude-old", Identity: &configjson.Identity{Account: acct(4)}}
		}, func(t *testing.T, r *Resolution) {
			if len(r.Unregistered) != 1 || r.Unregistered[0].Name != "Claude-old" {
				t.Fatalf("unregistered = %+v", r.Unregistered)
			}
		}},
		{"outside switch", func(w *world) {
			w.folders["Claude"].Identity = &configjson.Identity{Account: acct(2)}
			w.folders["Claude"].Replicas = []model.Replica{rep(2, 20)}
			delete(w.folders, "Claude-home")
			w.folders["Claude-work"] = &Folder{Name: "Claude-work", Identity: &configjson.Identity{Account: acct(1)},
				Replicas: []model.Replica{rep(1, 10)}}
		}, func(t *testing.T, r *Resolution) {
			if !r.OutsideSwitch || r.Active.Profile.Alias != "home" || r.Bindings["work"].Folder.Name != "Claude-work" {
				t.Fatalf("resolution = %+v", r)
			}
			// Claude/ still carries work's marker.
			if r.Active.BaseValid || r.Active.BaseInvalidWhy != WhyMarkerAlias {
				t.Fatalf("active base = %v (%s)", r.Active.BaseValid, r.Active.BaseInvalidWhy)
			}
		}},
		{"marker base mismatch", func(w *world) { w.folders["Claude-home"].Marker.BaseHash = "other" },
			wantWhy("home", WhyMarkerBase)},
		{"marker alias mismatch", func(w *world) { w.folders["Claude-home"].Marker.Alias = "work" },
			wantWhy("home", WhyMarkerAlias)},
		{"no marker", func(w *world) { w.folders["Claude-home"].Marker = nil }, wantWhy("home", WhyNoMarker)},
		{"birth mismatch", func(w *world) { w.folders["Claude-home"].ID.Birth = t0.Add(time.Second) },
			wantWhy("home", WhyFolderID)},
		{"replica change", func(w *world) {
			f := w.folders["Claude-home"]
			f.Replicas = []model.Replica{rep(2, 21)}
			f.Marker.Replica = rep(2, 21)
		}, func(t *testing.T, r *Resolution) {
			h := r.Bindings["home"]
			if h.Replica != rep(2, 21) || h.BaseValid || h.BaseInvalidWhy != WhyReplicaChanged {
				t.Fatalf("home = %+v", h)
			}
		}},
		{"active org from hints", func(w *world) {
			w.folders["Claude"].Replicas = []model.Replica{rep(1, 10), rep(1, 11), rep(9, 90)}
			w.hints.ActiveOrg = org(11)
		}, func(t *testing.T, r *Resolution) {
			if r.Active.Replica != rep(1, 11) || r.Active.BaseValid {
				t.Fatalf("active = %+v", r.Active)
			}
		}},
		{"unknown org with several", func(w *world) {
			w.folders["Claude"].Replicas = []model.Replica{rep(1, 10), rep(1, 11)}
			w.hints.ActiveOrg = org(99)
		}, func(t *testing.T, r *Resolution) {
			if r.Active.Replica != (model.Replica{Account: acct(1)}) {
				t.Fatalf("active replica = %+v", r.Active.Replica)
			}
		}},
		{"hint ignored for a parked folder", func(w *world) {
			w.folders["Claude-home"].Replicas = []model.Replica{rep(2, 20), rep(2, 21)}
			w.hints.ActiveOrg = org(20)
		}, func(t *testing.T, r *Resolution) {
			if r.Bindings["home"].Replica.Org != "" {
				t.Fatalf("home replica = %+v", r.Bindings["home"].Replica)
			}
		}},
		{"pending add, unidentified Claude", func(w *world) {
			w.root.PendingAdd = &state.PendingAdd{Alias: "new", ParkedFrom: "work"}
			w.folders["Claude"] = &Folder{Name: "Claude", Active: true, IdentityErr: IdentityNoConfig}
			w.folders["Claude-work"] = &Folder{Name: "Claude-work", Identity: &configjson.Identity{Account: acct(1)},
				Replicas: []model.Replica{rep(1, 10)}}
		}, func(t *testing.T, r *Resolution) {
			if r.PendingAdd == nil || r.PendingAdd.Name != "Claude" || r.Active != nil || len(r.Unregistered) != 0 {
				t.Fatalf("resolution = %+v", r)
			}
			if r.Bindings["work"].Folder.Name != "Claude-work" {
				t.Fatalf("work = %+v", r.Bindings["work"])
			}
		}},
		{"pending add, new account", func(w *world) {
			w.root.PendingAdd = &state.PendingAdd{Alias: "new", ParkedFrom: "work"}
			w.folders["Claude"] = &Folder{Name: "Claude", Active: true, Identity: &configjson.Identity{Account: acct(5)}}
			w.folders["Claude-work"] = &Folder{Name: "Claude-work", Identity: &configjson.Identity{Account: acct(1)}}
		}, func(t *testing.T, r *Resolution) {
			if r.PendingAdd == nil || r.Active != nil {
				t.Fatalf("resolution = %+v", r)
			}
		}},
		{"unidentified Claude without pending add", func(w *world) {
			w.folders["Claude"].Identity = nil
			w.folders["Claude"].IdentityErr = IdentityNoAccount
		}, func(t *testing.T, r *Resolution) {
			if r.Active == nil || r.Active.Profile.Alias != "work" || r.Active.BaseInvalidWhy != WhyIdentityUnknown {
				t.Fatalf("active = %+v", r.Active)
			}
		}},
		{"fresh state", func(w *world) { w.root = nil }, func(t *testing.T, r *Resolution) {
			if r.Active == nil || r.Active.Profile.Alias != "work" || r.Active.Replica != rep(1, 10) {
				t.Fatalf("active = %+v", r.Active)
			}
			if h := r.Bindings["home"]; h.Folder.Name != "Claude-home" || h.BaseValid || h.BaseInvalidWhy != WhyNoState {
				t.Fatalf("home = %+v", h)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			if tc.setup != nil {
				tc.setup(w)
			}
			r, err := w.resolve()
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, r)
		})
	}
}

func wantWhy(alias ids.Alias, why string) func(t *testing.T, r *Resolution) {
	return func(t *testing.T, r *Resolution) {
		t.Helper()
		b := r.Bindings[alias]
		if b.BaseValid || b.BaseInvalidWhy != why {
			t.Fatalf("%s: valid %v, why %q, want %q", alias, b.BaseValid, b.BaseInvalidWhy, why)
		}
	}
}

func TestResolveIdentityMismatch(t *testing.T) {
	tests := []struct {
		name  string
		setup func(w *world)
	}{
		{"parked folder holds another account", func(w *world) {
			w.folders["Claude-home"].Identity = &configjson.Identity{Account: acct(3)}
		}},
		{"parked folder holds the other profile's account", func(w *world) {
			w.root.Active = "home"
			w.folders["Claude"].Identity = &configjson.Identity{Account: acct(2)}
			w.folders["Claude-work"] = &Folder{Name: "Claude-work", Identity: &configjson.Identity{Account: acct(2)}}
		}},
		{"active folder holds another account", func(w *world) {
			w.folders["Claude"].Identity = &configjson.Identity{Account: acct(3)}
		}},
		{"two unrecorded folders share an account", func(w *world) {
			w.root = nil
			w.folders["Claude-home"].Identity = &configjson.Identity{Account: acct(1)}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			tc.setup(w)
			_, err := w.resolve()
			if pderr.CodeOf(err) != pderr.IdentityMismatch {
				t.Fatalf("err = %v, want code 20", err)
			}
			if strings.Contains(strings.ToLower(err.Error()), "sign") {
				t.Fatalf("message mentions signing: %v", err)
			}
		})
	}
}

func TestCopiedFolderInvalidatesBase(t *testing.T) {
	fsys := fsxtest.New(t)
	root := t.TempDir()
	home := filepath.Join(root, "Claude-home")
	mkdir(t, filepath.Join(root, "Claude"))
	mkdir(t, home)
	ids := &fakeIDs{
		ident: map[string]configjson.Identity{filepath.Join(root, "Claude"): {Account: acct(1)}, home: {Account: acct(2)}},
		reps:  map[string][]model.Replica{filepath.Join(root, "Claude"): {rep(1, 10)}, home: {rep(2, 20)}},
	}
	sc := Scanner{FS: fsys, DataRoot: root, ReadIdentity: ids.read, ListReplicas: ids.list}
	marker := NewMarker("home", rep(2, 20), 1, "b2")
	if err := WriteMarker(fsys, home, marker); err != nil {
		t.Fatal(err)
	}
	folders, err := sc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Profiles = []config.Profile{{Alias: "work"}, {Alias: "home"}}
	st := state.NewRoot(root)
	st.Active = "work"
	st.Profiles["work"] = &state.ProfileState{Alias: "work", Replica: rep(1, 10)}
	st.Profiles["home"] = &state.ProfileState{Alias: "home", Replica: rep(2, 20), FolderID: folders[1].ID, MarkerBase: "b2"}
	r, err := Resolve(cfg, st, folders, Hints{})
	if err != nil || !r.Bindings["home"].BaseValid {
		t.Fatalf("before copy: %+v, %v", r, err)
	}

	// Replace the folder by a copy (as a backup restore or Finder copy
	// would), carrying the marker along.
	if err := os.Rename(home, filepath.Join(t.TempDir(), "orig")); err != nil {
		t.Fatal(err)
	}
	mkdir(t, home)
	if err := WriteMarker(fsys, home, marker); err != nil {
		t.Fatal(err)
	}
	folders, err = sc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r, err = Resolve(cfg, st, folders, Hints{})
	if err != nil {
		t.Fatal(err)
	}
	if h := r.Bindings["home"]; h.BaseValid || h.BaseInvalidWhy != WhyFolderID {
		t.Fatalf("after copy: valid %v, why %q", h.BaseValid, h.BaseInvalidWhy)
	}
}

func TestDetectForeign(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	for _, n := range []string{"Claude", "Claude-home", "Switcher-profiles"} {
		mkdir(t, filepath.Join(root, n))
	}
	mkdir(t, filepath.Join(home, ".switcher-b"))
	folders := []Folder{{Name: "Claude", Path: filepath.Join(root, "Claude")}, {Name: "Claude-home", Path: filepath.Join(root, "Claude-home")}}
	fps := []compat.Footprint{
		{Tool: "switcher-a", FolderGlobs: []string{"Switcher-*"}},
		{Tool: "switcher-b", PathsUnderHome: []string{".switcher-b"}},
		{Tool: "switcher-c", FolderGlobs: []string{"Claude-home"}, PathsUnderHome: []string{".nothing"}},
		{Tool: "absent", FolderGlobs: []string{"Nope*"}, PathsUnderHome: []string{"nope/config"}},
	}
	got, err := DetectForeign(folders, home, fps)
	if err != nil {
		t.Fatal(err)
	}
	want := []ForeignTool{
		{Tool: "switcher-a", Folders: []string{"Switcher-profiles"}},
		{Tool: "switcher-b"},
		{Tool: "switcher-c", Folders: []string{"Claude-home"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i].Tool != want[i].Tool || !slices.Equal(got[i].Folders, want[i].Folders) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
	bad := [][]compat.Footprint{
		{{Tool: "x", FolderGlobs: []string{"a/b"}}},
		{{Tool: "x", FolderGlobs: []string{"["}}},
		{{Tool: "x", PathsUnderHome: []string{"../etc"}}},
	}
	for _, fp := range bad {
		if _, err := DetectForeign(folders, home, fp); err == nil {
			t.Fatalf("footprint %+v accepted", fp)
		}
	}
}

func TestNativeMultiAccount(t *testing.T) {
	tests := []struct {
		name    string
		ps      *state.ProfileState
		current ids.AccountID
		want    bool
	}{
		{"no state", nil, acct(1), false},
		{"same account", &state.ProfileState{Replica: rep(1, 10), AccountHistory: []ids.AccountID{acct(1)}}, acct(1), false},
		{"first change", &state.ProfileState{Replica: rep(1, 10), AccountHistory: []ids.AccountID{acct(1)}}, acct(2), false},
		{"back and forth", &state.ProfileState{Replica: rep(2, 20), AccountHistory: []ids.AccountID{acct(1), acct(2)}}, acct(1), true},
		{"third account", &state.ProfileState{Replica: rep(2, 20), AccountHistory: []ids.AccountID{acct(1), acct(2)}}, acct(3), true},
		{"history only", &state.ProfileState{AccountHistory: []ids.AccountID{acct(1), acct(2)}}, acct(1), true},
		{"unknown current", &state.ProfileState{Replica: rep(2, 20), AccountHistory: []ids.AccountID{acct(1), acct(2)}}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NativeMultiAccount(tc.ps, tc.current); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
