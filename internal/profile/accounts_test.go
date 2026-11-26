package profile

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources/configjson"
	"github.com/kazupc/paritydesk/internal/state"
)

var markT = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

func accountsRoot() *state.Root {
	root := state.NewRoot("/data")
	root.Profiles["x"] = &state.ProfileState{Alias: "x", Replica: rep(1, 10)}
	root.Profiles["y"] = &state.ProfileState{Alias: "y", Replica: rep(2, 20), Replicas: []model.Replica{rep(5, 20)}}
	root.Profiles["z"] = &state.ProfileState{Alias: "z", Replica: rep(3, 30), AccountHistory: []ids.AccountID{acct(6), acct(3)}}
	root.Active = "x"
	root.LastCommitAt = markT
	return root
}

func TestAttributeChanges(t *testing.T) {
	root := accountsRoot()
	got := AttributeChanges(root, []LoggedChange{
		{From: acct(1), To: acct(2), At: markT.Add(time.Minute)},  // Replica
		{From: acct(5), To: acct(6), At: markT.Add(-time.Minute)}, // Replicas, AccountHistory; before the mark
		{From: acct(9), To: acct(3)},                              // unknown; no time
	})
	want := []AttributedChange{
		{LoggedChange: LoggedChange{From: acct(1), To: acct(2), At: markT.Add(time.Minute)}, FromAlias: "x", ToAlias: "y", SinceCommit: true},
		{LoggedChange: LoggedChange{From: acct(5), To: acct(6), At: markT.Add(-time.Minute)}, FromAlias: "y", ToAlias: "z"},
		{LoggedChange: LoggedChange{From: acct(9), To: acct(3)}, ToAlias: "z"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	root.LastCommitAt = time.Time{}
	if got := AttributeChanges(root, []LoggedChange{{From: acct(1), To: acct(2), At: markT}}); got[0].SinceCommit {
		t.Fatal("a zero mark counts a change as since the commit")
	}
	if AttributeChanges(nil, nil) != nil {
		t.Fatal("nil input")
	}
}

func change(from, to int, at time.Duration, since bool) AttributedChange {
	return AttributedChange{LoggedChange: LoggedChange{From: acct(from), To: acct(to), At: markT.Add(at)}, FromAlias: ids.Alias(map[int]string{1: "x", 2: "y"}[from]), SinceCommit: since}
}

func TestNativeMultiFromLog(t *testing.T) {
	tests := []struct {
		name    string
		changes []AttributedChange
		alias   ids.Alias
		n       int
	}{
		{"none", nil, "", 0},
		{"single change", []AttributedChange{change(1, 2, time.Minute, true)}, "", 0},
		{"chain of two", []AttributedChange{change(1, 2, time.Minute, true), change(2, 1, 2*time.Minute, true)}, "x", 2},
		{"back and forth to a third account", []AttributedChange{change(1, 3, time.Minute, true), change(3, 1, 2*time.Minute, true)}, "x", 2},
		{"two unrelated changes", []AttributedChange{change(1, 2, time.Minute, true), change(3, 4, 2*time.Minute, true)}, "", 0},
		{"chain split by the mark", []AttributedChange{change(1, 2, -time.Minute, false), change(2, 1, time.Minute, true)}, "", 0},
		{"chain of three, out of order input", []AttributedChange{change(3, 1, 3*time.Minute, true), change(1, 2, time.Minute, true), change(2, 3, 2*time.Minute, true)}, "x", 3},
		{"time order, not input order", []AttributedChange{change(2, 1, time.Minute, true), change(1, 2, 30*time.Second, true)}, "x", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			alias, n := NativeMultiFromLog(tc.changes)
			if alias != tc.alias || n != tc.n {
				t.Fatalf("got %q %d, want %q %d", alias, n, tc.alias, tc.n)
			}
		})
	}
}

// TestNativeMultiPropertyChain (P6): n >= 2 exactly when a chain of two
// SinceCommit changes exists; changes before the mark, in any order,
// never change the result.
func TestNativeMultiPropertyChain(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 22))
	for iter := 0; iter < 500; iter++ {
		var since, before []AttributedChange
		for i := 0; i < r.IntN(6); i++ {
			since = append(since, change(1+r.IntN(4), 1+r.IntN(4), time.Duration(i+1)*time.Minute, true))
		}
		for i := 0; i < r.IntN(6); i++ {
			before = append(before, change(1+r.IntN(4), 1+r.IntN(4), -time.Duration(i+1)*time.Minute, false))
		}
		exists := false
		for i := range since {
			for j := i + 1; j < len(since); j++ {
				exists = exists || since[i].To == since[j].From
			}
		}
		_, n := NativeMultiFromLog(since)
		if (n >= 2) != exists {
			t.Fatalf("iter %d: n %d, chain exists %v: %+v", iter, n, exists, since)
		}
		mixed := append(slices.Clone(before), since...)
		r.Shuffle(len(mixed), func(i, j int) { mixed[i], mixed[j] = mixed[j], mixed[i] })
		a1, n1 := NativeMultiFromLog(since)
		a2, n2 := NativeMultiFromLog(mixed)
		if a1 != a2 || n1 != n2 {
			t.Fatalf("iter %d: %q %d vs %q %d with changes before the mark", iter, a1, n1, a2, n2)
		}
	}
}

// inFolderFixture is X (account 1) active by state, with Claude/ now
// signed in to account 2, Y's recorded account, so the resolver bound
// Claude/ to Y.
func inFolderFixture(marker *model.Marker) (*config.Config, *state.Root, *Resolution) {
	cfg := &config.Config{Profiles: []config.Profile{{Alias: "x"}, {Alias: "y"}}}
	root := accountsRoot()
	claude := &Folder{Name: ids.DefaultFolder, Active: true, Identity: &configjson.Identity{Account: acct(2)}, Marker: marker}
	res := &Resolution{Active: &Binding{Profile: config.Profile{Alias: "y"}, Folder: claude}, OutsideSwitch: true,
		Bindings: map[ids.Alias]*Binding{}}
	return cfg, root, res
}

func TestInFolderSignIn(t *testing.T) {
	markX := &model.Marker{Version: 1, Alias: "x", Replica: rep(1, 10)}
	logged := []AttributedChange{{LoggedChange: LoggedChange{From: acct(1), To: acct(2), At: markT.Add(time.Minute)}, SinceCommit: true}}
	t.Run("rule M without changes", func(t *testing.T) {
		cfg, root, res := inFolderFixture(markX)
		c, ok := InFolderSignIn(cfg, root, res, nil)
		if !ok || c != (InFolderChange{Folder: "x", Holder: "y"}) {
			t.Fatalf("got %+v %v", c, ok)
		}
	})
	t.Run("rule L without a marker", func(t *testing.T) {
		cfg, root, res := inFolderFixture(nil)
		c, ok := InFolderSignIn(cfg, root, res, logged)
		if !ok || c != (InFolderChange{Folder: "x", Holder: "y", FromLog: true}) {
			t.Fatalf("got %+v %v", c, ok)
		}
	})
	t.Run("both", func(t *testing.T) {
		cfg, root, res := inFolderFixture(markX)
		c, ok := InFolderSignIn(cfg, root, res, logged)
		if !ok || c != (InFolderChange{Folder: "x", Holder: "y", FromLog: true}) {
			t.Fatalf("got %+v %v", c, ok)
		}
	})
	t.Run("a real outside switch moves the marker with the folder", func(t *testing.T) {
		cfg, root, res := inFolderFixture(&model.Marker{Version: 1, Alias: "y", Replica: rep(2, 20)})
		if c, ok := InFolderSignIn(cfg, root, res, nil); ok {
			t.Fatalf("got %+v", c)
		}
	})
	t.Run("a marker of an unconfigured profile", func(t *testing.T) {
		cfg, root, res := inFolderFixture(&model.Marker{Version: 1, Alias: "gone", Replica: rep(7, 10)})
		if c, ok := InFolderSignIn(cfg, root, res, nil); ok {
			t.Fatalf("got %+v", c)
		}
	})
	t.Run("a pending profile add", func(t *testing.T) {
		cfg, root, res := inFolderFixture(markX)
		root.PendingAdd = &state.PendingAdd{Alias: "w"}
		if _, ok := InFolderSignIn(cfg, root, res, logged); ok {
			t.Fatal("true with a pending add")
		}
	})
	t.Run("a change before the mark", func(t *testing.T) {
		cfg, root, res := inFolderFixture(nil)
		old := slices.Clone(logged)
		old[0].SinceCommit = false
		if _, ok := InFolderSignIn(cfg, root, res, old); ok {
			t.Fatal("true for a change before the mark")
		}
	})
	t.Run("a forged change from another account", func(t *testing.T) {
		cfg, root, res := inFolderFixture(nil)
		forged := slices.Clone(logged)
		forged[0].From = acct(9)
		if _, ok := InFolderSignIn(cfg, root, res, forged); ok {
			t.Fatal("true for a change whose From is not the recorded account")
		}
		forged[0].From, forged[0].To = acct(1), acct(9)
		if _, ok := InFolderSignIn(cfg, root, res, forged); ok {
			t.Fatal("true for a change whose To is not the folder's identity")
		}
	})
	back := append(slices.Clone(logged),
		AttributedChange{LoggedChange: LoggedChange{From: acct(2), To: acct(1), At: markT.Add(2 * time.Minute)}, SinceCommit: true})
	t.Run("a real outside switch after a sign-in and back", func(t *testing.T) {
		// X's folder went A -> B -> A in Claude; then the folders were
		// renamed outside paritydesk, so Claude/ is Y's folder.
		for _, mk := range []*model.Marker{nil, {Version: 1, Alias: "y", Replica: rep(2, 20)}} {
			cfg, root, res := inFolderFixture(mk)
			if c, ok := InFolderSignIn(cfg, root, res, back); ok {
				t.Fatalf("marker %v: refused a real outside switch: %+v", mk, c)
			}
		}
	})
	t.Run("signed in again after changing back", func(t *testing.T) {
		cfg, root, res := inFolderFixture(nil)
		again := append(slices.Clone(back),
			AttributedChange{LoggedChange: LoggedChange{From: acct(1), To: acct(2), At: markT.Add(3 * time.Minute)}, SinceCommit: true})
		if c, ok := InFolderSignIn(cfg, root, res, again); !ok || !c.FromLog || c.Folder != "x" {
			t.Fatalf("got %+v %v", c, ok)
		}
	})
	t.Run("a chain through a third account", func(t *testing.T) {
		cfg, root, res := inFolderFixture(nil)
		chain := []AttributedChange{
			{LoggedChange: LoggedChange{From: acct(3), To: acct(2), At: markT.Add(2 * time.Minute)}, SinceCommit: true},
			{LoggedChange: LoggedChange{From: acct(1), To: acct(3), At: markT.Add(time.Minute)}, SinceCommit: true},
		}
		if c, ok := InFolderSignIn(cfg, root, res, chain); !ok || !c.FromLog || c.Folder != "x" {
			t.Fatalf("got %+v %v", c, ok)
		}
	})
	t.Run("the holder's own marker means a real outside switch", func(t *testing.T) {
		cfg, root, res := inFolderFixture(&model.Marker{Version: 1, Alias: "y", Replica: rep(2, 20)})
		if c, ok := InFolderSignIn(cfg, root, res, logged); ok {
			t.Fatalf("got %+v", c)
		}
	})
	t.Run("nothing to check", func(t *testing.T) {
		cfg, root, res := inFolderFixture(markX)
		if _, ok := InFolderSignIn(cfg, nil, res, nil); ok {
			t.Fatal("nil root")
		}
		res.Active.Folder.Identity = nil
		if _, ok := InFolderSignIn(cfg, root, res, nil); ok {
			t.Fatal("no identity")
		}
		if _, ok := InFolderSignIn(cfg, root, &Resolution{}, nil); ok {
			t.Fatal("no active binding")
		}
	})
}
