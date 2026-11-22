package rows

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
)

func TestRegistryCoversAllRows(t *testing.T) {
	reg := Registry()
	for _, r := range model.AllRows {
		m, ok := reg[r]
		if !ok || m.Row() != r {
			t.Fatalf("row %s missing or misregistered", r)
		}
		out, err := m.Merge(Input{Row: r, Policy: openPolicy()})
		if err != nil {
			t.Fatalf("row %s: empty merge: %v", r, err)
		}
		if out.Canonical == nil || len(out.Changes) != 0 {
			t.Fatalf("row %s: empty merge gave %+v", r, out)
		}
	}
	if _, err := reg[model.RowTasks].Merge(Input{Row: model.RowSessions}); err == nil {
		t.Fatal("module accepted another row's input")
	}
}

func TestContestedItemIsNotAnEdit(t *testing.T) {
	row := model.RowAppPrefs
	base := mk(t, "theme", `"light"`, 0)
	server := mk(t, "theme", `"dark"`, 0)
	tests := []struct {
		name      string
		contested map[string]model.Contested
		want      string
	}{
		{"server value is not an edit", map[string]model.Contested{"theme": {Row: row, Key: "theme", ServerHash: server.Hash, IntendedHash: base.Hash}}, base.Hash},
		{"intended value is not an edit", map[string]model.Contested{"theme": {Row: row, Key: "theme", ServerHash: "x", IntendedHash: server.Hash}}, base.Hash},
		{"other value is an edit", map[string]model.Contested{"theme": {Row: row, Key: "theme", ServerHash: "x", IntendedHash: "y"}}, server.Hash},
		{"no contested entry", nil, server.Hash},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := input(row, snap(row, server), snap(row, base), snap(row, base), snap(row, base), snap(row, base), openPolicy())
			in.Leaving.Contested = tc.contested
			out := mustMerge(t, row, in)
			if got := out.Desired.Items["theme"].Hash; got != tc.want {
				t.Fatalf("desired hash = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestContestedNeverDeletesFromInvalidBase checks C1 with a stale
// contested entry: the leaving side's base is invalid, so its value stays
// in the union even when it matches a contested hash and the entering
// side removed the key against its valid base.
func TestContestedNeverDeletesFromInvalidBase(t *testing.T) {
	row := model.RowAppPrefs
	dark := mk(t, "theme", `"dark"`, 0)
	in := input(row, snap(row, dark), nil, snap(row), snap(row, dark), snap(row, dark), openPolicy())
	in.Leaving.BaseValid = false
	in.Leaving.Contested = map[string]model.Contested{"theme": {Row: row, Key: "theme", ServerHash: dark.Hash, IntendedHash: "x"}}
	out := mustMerge(t, row, in)
	if _, ok := out.Desired.Items["theme"]; !ok {
		t.Fatalf("an invalid base deleted theme: %+v", out.Changes)
	}
	if _, ok := out.Canonical.Tombstones["theme"]; ok {
		t.Fatal("canonical tombstone recorded")
	}
}

// TestContestedNotHarvestedFromInvalidBase checks C2 on a side with an
// invalid base: a value that matches a contested hash is not harvested
// over canonical when the other side did not remove the key.
func TestContestedNotHarvestedFromInvalidBase(t *testing.T) {
	row := model.RowAppPrefs
	light := mk(t, "theme", `"light"`, 0)
	dark := mk(t, "theme", `"dark"`, 0)
	in := input(row, snap(row, dark), nil, snap(row, light), snap(row, light), snap(row, light), openPolicy())
	in.Leaving.BaseValid = false
	in.Leaving.Contested = map[string]model.Contested{"theme": {Row: row, Key: "theme", ServerHash: dark.Hash, IntendedHash: "x"}}
	out := mustMerge(t, row, in)
	if got := out.Canonical.Items["theme"].Hash; got != light.Hash {
		t.Fatalf("canonical theme = %s, want the light value", got)
	}
	if got := out.Desired.Items["theme"].Hash; got != light.Hash {
		t.Fatalf("desired theme = %s, want the light value", got)
	}
	if len(out.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v", out.Conflicts)
	}
}

func TestToolWrittenAndZeroTimeNeverWin(t *testing.T) {
	row := model.RowWindowState
	tests := []struct {
		name       string
		lTime      int64
		toolStamps map[string]int64
		eTime      int64
		wantL      bool
	}{
		{"later app time wins", 500, nil, 100, true},
		{"tool-written time never wins", 500, map[string]int64{"window-state.json": 500}, 100, false},
		{"zero time never wins", 0, nil, 100, false},
		{"both unusable: leaving side", 0, nil, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := mk(t, "window-state.json", `{"sha256":"aa"}`, tc.lTime)
			e := mk(t, "window-state.json", `{"sha256":"bb"}`, tc.eTime)
			in := input(row, snap(row, l), nil, snap(row, e), nil, nil, openPolicy())
			in.Leaving.BaseValid, in.Entering.BaseValid = false, false
			in.Leaving.ToolStamps = tc.toolStamps
			out := mustMerge(t, row, in)
			if got := out.Desired.Items[l.Key].Hash == l.Hash; got != tc.wantL {
				t.Fatalf("leaving won = %v, want %v", got, tc.wantL)
			}
			if !out.NeedsConfirmation {
				t.Fatal("invalid bases did not ask for confirmation")
			}
			if len(out.Conflicts) != 1 {
				t.Fatalf("conflicts = %+v", out.Conflicts)
			}
		})
	}
}

func TestDataBoundary(t *testing.T) {
	row := model.RowAppPrefs
	base := mk(t, "k", `1`, 0)
	lEdit := mk(t, "l", `"mine"`, 0)
	eEdit := mk(t, "e", `"yours"`, 0)
	tests := []struct {
		name                    string
		lExp, eExp, eImp        bool
		canonL, canonE          bool
		desiredL, desiredE      bool
		loHasL, eoHasE, desired bool
	}{
		{"open boundary", true, true, true, true, true, true, true, false, false, true},
		{"leaving does not export", false, true, true, false, true, false, true, true, false, true},
		{"entering does not export", true, false, true, true, false, true, true, false, true, true},
		{"entering does not import", true, true, false, true, true, false, true, false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pol := openPolicy()
			pol.LeavingExports, pol.EnteringExports, pol.EnteringImports = tc.lExp, tc.eExp, tc.eImp
			in := input(row, snap(row, base, lEdit), snap(row, base), snap(row, base, eEdit), snap(row, base), snap(row, base), pol)
			out := mustMerge(t, row, in)
			check := func(what string, s *model.RowSnapshot, key string, want bool) {
				t.Helper()
				if _, ok := s.Items[key]; ok != want {
					t.Fatalf("%s has %s = %v, want %v", what, key, ok, want)
				}
			}
			check("canonical", out.Canonical, "l", tc.canonL)
			check("canonical", out.Canonical, "e", tc.canonE)
			check("desired", out.Desired, "l", tc.desiredL)
			check("desired", out.Desired, "e", tc.desiredE)
			check("leaving overlay", out.LeavingOverlay, "l", tc.loHasL)
			check("entering overlay", out.EnteringOverlay, "e", tc.eoHasE)
		})
	}
}

func TestOverlayCarriedForward(t *testing.T) {
	row := model.RowAppPrefs
	priv := mk(t, "p", `"private"`, 0)
	shared := mk(t, "p", `"shared"`, 0)
	in := input(row, snap(row), snap(row), snap(row, priv), snap(row, priv), snap(row, shared), openPolicy())
	in.EnteringOverlay = snap(row, priv)
	in.LeavingOverlay = snap(row, mk(t, "q", `1`, 0))
	out := mustMerge(t, row, in)
	if out.Desired.Items["p"].Hash != priv.Hash {
		t.Fatal("entering overlay did not override canonical")
	}
	if _, ok := out.LeavingOverlay.Items["q"]; !ok {
		t.Fatal("leaving overlay lost an item")
	}
	if out.Canonical.Items["p"].Hash != shared.Hash {
		t.Fatal("overlay item leaked into canonical")
	}
	next := input(row, snap(row), snap(row), out.Desired, out.Desired, out.Canonical, openPolicy())
	next.LeavingOverlay, next.EnteringOverlay = out.LeavingOverlay, out.EnteringOverlay
	if again := mustMerge(t, row, next); len(again.Changes) != 0 || again.Canonical.Items["p"].Hash != shared.Hash {
		t.Fatalf("second merge changed state: %+v", again.Changes)
	}
}

func TestWaitingInputUnknownKinds(t *testing.T) {
	row := model.RowWaitingInput
	file := func(name, hash, kind string) model.Item {
		return mk(t, name, fmt.Sprintf(`{"sha256":%q,"kind":%q}`, hash, kind), 0)
	}
	pol := openPolicy()
	pol.Fields = map[string][]string{RoleKnownKinds: {"json"}}
	baseKnown, baseOdd := file("a.json", "1", "json"), file("b.bin", "1", "bin")
	in := input(row,
		snap(row, file("a.json", "2", "json"), file("b.bin", "2", "bin"), file("c.bin", "1", "bin")),
		snap(row, baseKnown, baseOdd),
		snap(row, baseKnown, baseOdd), snap(row, baseKnown, baseOdd), snap(row, baseKnown, baseOdd), pol)
	out := mustMerge(t, row, in)
	tests := []struct {
		key, hash string
	}{
		{"a.json", file("a.json", "2", "json").Hash},
		{"b.bin", baseOdd.Hash},
		{"c.bin", file("c.bin", "1", "bin").Hash},
	}
	for _, tc := range tests {
		if got := out.Desired.Items[tc.key].Hash; got != tc.hash {
			t.Fatalf("%s: hash %s, want %s", tc.key, got, tc.hash)
		}
	}
	del := input(row, snap(row), snap(row, baseKnown, baseOdd), snap(row, baseKnown, baseOdd), snap(row, baseKnown, baseOdd), snap(row, baseKnown, baseOdd), pol)
	out = mustMerge(t, row, del)
	if _, ok := out.Desired.Items["a.json"]; ok {
		t.Fatal("known file deletion not carried")
	}
	if _, ok := out.Desired.Items["b.bin"]; !ok {
		t.Fatal("unknown-kind file removed from the target")
	}
}

func TestTasksMaxTime(t *testing.T) {
	row := model.RowTasks
	base := mk(t, "backlog/1", `{"id":"1","title":"a","lastRunAt":10}`, 10)
	l := mk(t, "backlog/1", `{"id":"1","title":"b","lastRunAt":20}`, 20)
	e := mk(t, "backlog/1", `{"id":"1","title":"a","lastRunAt":30}`, 30)
	tests := []struct {
		name   string
		fields map[string][]string
		title  string
		run    float64
	}{
		{"max time with per-field", map[string][]string{RolePerField: {"title"}, RoleMaxTime: {"lastRunAt"}}, "b", 30},
		{"empty fields: whole record", nil, "b", 20},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pol := openPolicy()
			pol.Fields = tc.fields
			out := mustMerge(t, row, input(row, snap(row, l), snap(row, base), snap(row, e), snap(row, base), snap(row, base), pol))
			got := out.Desired.Items["backlog/1"]
			if field(t, got, "title") != tc.title || field(t, got, "lastRunAt") != tc.run {
				t.Fatalf("desired = %s", got.Value)
			}
		})
	}
}

func orderItem(t fatalHelper, xs ...string) model.Item {
	t.Helper()
	v, err := marshalList(xs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return mk(t, orderKey, string(v), 0)
}

func TestOrderRows(t *testing.T) {
	for _, row := range []model.RowID{model.RowGroupOrder, model.RowPinnedOrder} {
		t.Run(string(row), func(t *testing.T) {
			base := snap(row, orderItem(t, "a", "b", "c"))
			l := snap(row, orderItem(t, "c", "a", "b"))
			e := snap(row, orderItem(t, "a", "x", "b", "c"))
			out := mustMerge(t, row, input(row, l, base, e, base, base, openPolicy()))
			want := []string{"c", "a", "x", "b"}
			if !reflect.DeepEqual(out.Desired.Order, want) {
				t.Fatalf("order = %v, want %v", out.Desired.Order, want)
			}
			if !reflect.DeepEqual(out.Canonical.Order, want) {
				t.Fatalf("canonical order = %v", out.Canonical.Order)
			}
			if !hasChange(out, model.ChangeMoved, orderKey) {
				t.Fatalf("no move change: %+v", out.Changes)
			}
		})
	}
}

func TestSidebarView(t *testing.T) {
	row := model.RowSidebarView
	sec := func(id, name string) model.Item { return mk(t, sectionPrefix+id, fmt.Sprintf(`{"name":%q}`, name), 0) }
	withOrder := func(s *model.RowSnapshot, order ...string) *model.RowSnapshot { s.Order = order; return s }
	groupBy := mk(t, "field:groupBy", `"project"`, 0)
	base := func() *model.RowSnapshot {
		return withOrder(snap(row, groupBy, sec("1", "a"), sec("2", "b")), "1", "2")
	}
	l := withOrder(snap(row, mk(t, "field:groupBy", `"none"`, 0), sec("1", "a"), sec("2", "b"), sec("3", "c")), "2", "1", "3")
	e := withOrder(snap(row, groupBy, sec("1", "renamed"), sec("2", "b")), "1", "2")
	out := mustMerge(t, row, input(row, l, base(), e, base(), base(), openPolicy()))
	if got := out.Desired.Items["field:groupBy"].Value; string(got) != `"none"` {
		t.Fatalf("groupBy = %s", got)
	}
	if got := field(t, out.Desired.Items[sectionPrefix+"1"], "name"); got != "renamed" {
		t.Fatalf("section 1 = %v", got)
	}
	if want := []string{"2", "1", "3"}; !reflect.DeepEqual(out.Desired.Order, want) {
		t.Fatalf("order = %v, want %v", out.Desired.Order, want)
	}
	if _, ok := out.Desired.Items[orderKey]; ok {
		t.Fatal("order stand-in leaked into items")
	}
}

func TestMCPServersSecretEnv(t *testing.T) {
	row := model.RowMCPServers
	srv := func(tok string) model.Item {
		it := mk(t, "files", fmt.Sprintf(`{"command":"x","env":{"KEY":%q}}`, tok), 0)
		it.Secret = true
		return it
	}
	pol := openPolicy()
	pol.LeavingManaged = true
	pol.Fields = map[string][]string{RoleSecret: {"env"}}
	base, edit := srv("sha256:1"), srv("sha256:2")
	out := mustMerge(t, row, input(row, snap(row, edit), snap(row, base), snap(row, base), snap(row, base), snap(row, base), pol))
	if out.Desired.Items["files"].Hash != base.Hash {
		t.Fatal("secret crossed a managed edge")
	}
	pol.SecretsAllow = map[string]bool{"files/KEY": true}
	out = mustMerge(t, row, input(row, snap(row, edit), snap(row, base), snap(row, base), snap(row, base), snap(row, base), pol))
	if out.Desired.Items["files"].Hash != edit.Hash {
		t.Fatal("allowed secret not carried")
	}
}

func TestNilSnapshotNeverRemoves(t *testing.T) {
	row := model.RowWorktrees
	a := mk(t, "/w/a", `{"branch":"x"}`, 0)
	in := input(row, nil, snap(row), snap(row, a), snap(row, a), snap(row), openPolicy())
	out := mustMerge(t, row, in)
	if _, ok := out.Desired.Items[a.Key]; !ok || len(out.Changes) != 0 {
		t.Fatalf("nil leaving snapshot changed the target: %+v", out.Changes)
	}
}

func sessionsOut(t *testing.T, items ...model.Item) *Output {
	return &Output{Desired: snap(model.RowSessions, items...)}
}

func TestPostMerge(t *testing.T) {
	row := model.RowEnvironments
	live, dead := rec(t, 1, "", 1), fakeSession(2)
	env := func() *Output {
		d := snap(row, started(t, 1, 9), started(t, 2, 9), preset(t, 9, "p", `{}`))
		return &Output{
			Desired:         d,
			Canonical:       snap(row, started(t, 1, 9), started(t, 2, 9)),
			LeavingOverlay:  snap(row, started(t, 2, 9)),
			EnteringOverlay: snap(row),
			Changes:         []model.Change{{Row: row, Kind: model.ChangeUpdated, Key: startedPrefix + dead}},
		}
	}
	ses := sessionsOut(t, live)
	withTombs(ses.Desired, model.Tombstone{Key: dead, At: 5})
	tests := []struct {
		name   string
		outs   func(e *Output) map[model.RowID]*Output
		remove bool
	}{
		{"tombstoned session drops started", func(e *Output) map[model.RowID]*Output {
			return map[model.RowID]*Output{row: e, model.RowSessions: ses}
		}, true},
		{"missing sessions row is ignored", func(e *Output) map[model.RowID]*Output {
			return map[model.RowID]*Output{row: e}
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := env()
			if err := PostMerge(tc.outs(e)); err != nil {
				t.Fatal(err)
			}
			for _, s := range []*model.RowSnapshot{e.Desired, e.Canonical, e.LeavingOverlay} {
				if _, ok := s.Items[startedPrefix+dead]; ok == tc.remove {
					t.Fatalf("started/<dead> present = %v", ok)
				}
			}
			if _, ok := e.Desired.Items[startedPrefix+live.Key]; !ok {
				t.Fatal("started entry of a live session removed")
			}
			if hasChange(*e, model.ChangeDeleted, startedPrefix+dead) != tc.remove {
				t.Fatalf("changes = %+v", e.Changes)
			}
			if tc.remove && hasChange(*e, model.ChangeUpdated, startedPrefix+dead) {
				t.Fatal("stale update change kept")
			}
		})
	}
	if err := PostMerge(nil); err != nil {
		t.Fatal(err)
	}
}

// TestOrderRemovalStaysDeleted checks that removing the order item of
// group-order or pinned-order is reported as a deletion, not a move: the
// adapter deletes the stored list, and plan.Validate maps that
// destructive op to this change (the breaker counts it too).
func TestOrderRemovalStaysDeleted(t *testing.T) {
	for _, row := range []model.RowID{model.RowGroupOrder, model.RowPinnedOrder} {
		t.Run(string(row), func(t *testing.T) {
			base := snap(row, orderItem(t, "a", "b"))
			l := snap(row) // the leaving side removed the order
			e := snap(row, orderItem(t, "a", "b"))
			out := mustMerge(t, row, input(row, l, base, e, base, base, openPolicy()))
			if _, ok := out.Desired.Items[orderKey]; ok {
				t.Fatalf("order kept: %+v", out.Desired.Items)
			}
			if !hasChange(out, model.ChangeDeleted, orderKey) || hasChange(out, model.ChangeMoved, orderKey) {
				t.Fatalf("changes = %+v", out.Changes)
			}
		})
	}
}

// TestOrderHashMatchesStandIn checks that OrderHash, which verify records
// for section-order drift, is the hash of the merge's stand-in item.
func TestOrderHashMatchesStandIn(t *testing.T) {
	s := snap(model.RowSidebarView)
	s.Order = []string{"2", "1"}
	in, err := withOrderItems(Input{Row: model.RowSidebarView, Leaving: Side{Snapshot: s}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := OrderHash(s.Order)
	if err != nil || h != in.Leaving.Snapshot.Items[OrderItemKey].Hash {
		t.Fatalf("OrderHash = %q, %v; stand-in %q", h, err, in.Leaving.Snapshot.Items[OrderItemKey].Hash)
	}
	if h, _ := OrderHash(nil); h != "" {
		t.Fatalf("empty order hash = %q", h)
	}
}

// TestLaterRemovalBeatsOlderEdit checks D7 rule (1) for a removal: with
// both bases valid, the leaving side (live later) removed an item the
// entering side edited, so the removal wins and the edit is the loser.
func TestLaterRemovalBeatsOlderEdit(t *testing.T) {
	row := model.RowTasks
	base := mk(t, "backlog/1", `{"title":"a"}`, 0)
	edit := mk(t, "backlog/1", `{"title":"b"}`, 0)
	out := mustMerge(t, row, input(row, snap(row), snap(row, base), snap(row, edit), snap(row, base), snap(row, base), openPolicy()))
	if _, ok := out.Desired.Items["backlog/1"]; ok {
		t.Fatalf("edit kept: %+v", out.Changes)
	}
	if len(out.Conflicts) != 1 || out.Conflicts[0].LoserHash != edit.Hash {
		t.Fatalf("conflicts = %+v", out.Conflicts)
	}
}

// TestPostMergeLeavesUnknownStarted: the started-environments map is one
// Local Storage item for the whole folder, so it also holds entries of
// other orgs' local sessions and of remote session_ ids. PostMerge
// removes only entries of local sessions the sessions row knew and
// removed; it leaves the others alone.
func TestPostMergeLeavesUnknownStarted(t *testing.T) {
	row := model.RowEnvironments
	s1, s2 := rec(t, 1, "", 1), rec(t, 2, "", 1)
	base := snap(model.RowSessions, s1, s2)
	// The leaving side deleted session 2; session 50 belongs to another
	// org and the sessions row never saw it.
	left := withTombs(snap(model.RowSessions, s1), model.Tombstone{Key: s2.Key, At: 5})
	sesIn := input(model.RowSessions, left, base, base, base, base, manifestPolicy(t))
	ses := mustMerge(t, model.RowSessions, sesIn)
	remote := mk(t, startedPrefix+"session_01ABCDEF", fmt.Sprintf("%q", fakeUUID(9)), 0)
	all := []model.Item{started(t, 1, 9), started(t, 2, 9), started(t, 50, 9), remote}
	env := &Output{Desired: snap(row, all...), Canonical: snap(row, all...),
		LeavingOverlay: snap(row), EnteringOverlay: snap(row)}
	if err := PostMerge(map[model.RowID]*Output{row: env, model.RowSessions: &ses}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{startedPrefix + fakeSession(1), startedPrefix + fakeSession(50), remote.Key} {
		if _, ok := env.Desired.Items[k]; !ok {
			t.Errorf("%s removed from Desired", k)
		}
		if hasChange(*env, model.ChangeDeleted, k) {
			t.Errorf("%s reported deleted", k)
		}
	}
	gone := startedPrefix + fakeSession(2)
	if _, ok := env.Desired.Items[gone]; ok || !hasChange(*env, model.ChangeDeleted, gone) {
		t.Fatalf("started entry of the removed session kept: %+v", env.Changes)
	}
}
