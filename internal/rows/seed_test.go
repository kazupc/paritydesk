package rows

import (
	"encoding/json"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
)

const (
	seedSort  = "code-sessions-sort-ascending"
	seedEmpty = "code-sessions-show-empty-projects"
)

func seedFields() map[string][]string {
	return map[string][]string{
		RoleSeedKeys:    {seedSort, seedEmpty},
		RoleSeedOnly:    {seedSort},
		RoleSeedSection: {"sessions"},
		RoleSeedFrom:    {"ascending", "show.emptyGroups"},
	}
}

func seedPolicy(seeded bool, from map[string]json.RawMessage) Policy {
	p := openPolicy()
	p.Fields = seedFields()
	p.EnteringSeeded = seeded
	p.SeedFrom = from
	return p
}

func seedSnap(t fatalHelper, kv map[string]string) *model.RowSnapshot {
	t.Helper()
	s := snap(model.RowSidebarSeed)
	for k, v := range kv {
		s.Items[k] = mk(t, k, v, 0)
	}
	return s
}

func viewDesired(t fatalHelper, section string) *model.RowSnapshot {
	t.Helper()
	return snap(model.RowSidebarView, mk(t, sectionPrefix+"sessions", section, 0))
}

func TestSeedValues(t *testing.T) {
	got, err := SeedValues(viewDesired(t, `{"id":"sessions","ascending":true,"show":{"emptyGroups":false,"metadata":true}}`), seedFields())
	if err != nil || string(got[seedSort]) != "true" || string(got[seedEmpty]) != "false" || len(got) != 2 {
		t.Fatalf("values %v, %v", got, err)
	}
	for name, s := range map[string]*model.RowSnapshot{
		"missing section": snap(model.RowSidebarView, mk(t, sectionPrefix+"pinned", `{"ascending":true}`, 0)),
		"nil snapshot":    nil,
	} {
		if got, err := SeedValues(s, seedFields()); err != nil || len(got) != 0 {
			t.Errorf("%s: %v, %v", name, got, err)
		}
	}
	got, err = SeedValues(viewDesired(t, `{"ascending":"x","show":{"emptyGroups":true}}`), seedFields())
	if err != nil || len(got) != 1 || string(got[seedEmpty]) != "true" {
		t.Fatalf("non-boolean ascending: %v, %v", got, err)
	}
	bad := seedFields()
	bad[RoleSeedFrom] = bad[RoleSeedFrom][:1]
	if _, err := SeedValues(nil, bad); err == nil {
		t.Fatal("mismatched field lists accepted")
	}
}

func mergeSeed(t *testing.T, in Input) Output {
	t.Helper()
	in.Row = model.RowSidebarSeed
	out := mustMerge(t, model.RowSidebarSeed, in)
	if out.NeedsConfirmation {
		t.Fatal("NeedsConfirmation set")
	}
	for _, c := range out.Changes {
		if c.Kind == model.ChangeDeleted {
			t.Fatalf("deletion %+v", c)
		}
	}
	return out
}

func noteOf(out Output, key string) string {
	for _, c := range out.Changes {
		if c.Key == key {
			return c.Note
		}
	}
	return ""
}

func TestSeedUnseededTargetGetsCarriedView(t *testing.T) {
	from := map[string]json.RawMessage{seedSort: json.RawMessage("true"), seedEmpty: json.RawMessage("false")}
	cur := seedSnap(t, map[string]string{seedEmpty: "true"})
	canon := seedSnap(t, map[string]string{seedEmpty: "true"})
	out := mergeSeed(t, input(model.RowSidebarSeed, cur.Clone(), cur, cur, cur, canon, seedPolicy(false, from)))
	if string(out.Desired.Items[seedSort].Value) != "true" || string(out.Desired.Items[seedEmpty].Value) != "false" {
		t.Fatalf("desired %v", out.Desired.Items)
	}
	if noteOf(out, seedSort) != noteSeed || noteOf(out, seedEmpty) != noteFromView {
		t.Fatalf("changes %+v", out.Changes)
	}
	if out.Canonical.Items[seedEmpty].Hash != canon.Items[seedEmpty].Hash {
		t.Fatal("derivation changed canonical")
	}
}

func TestSeedSeededTargetKeepsSortKey(t *testing.T) {
	from := map[string]json.RawMessage{seedSort: json.RawMessage("true"), seedEmpty: json.RawMessage("true")}
	cur := seedSnap(t, map[string]string{seedSort: "false", seedEmpty: "false"})
	out := mergeSeed(t, input(model.RowSidebarSeed, cur.Clone(), cur, cur, cur, cur, seedPolicy(true, from)))
	if out.Desired.Items[seedSort].Hash != cur.Items[seedSort].Hash {
		t.Fatal("seed-only key written to a seeded folder")
	}
	if string(out.Desired.Items[seedEmpty].Value) != "true" {
		t.Fatal("show-empty key not set to the carried value")
	}
	// A seeded target also keeps its sort key against the leaving side's
	// own change.
	l := seedSnap(t, map[string]string{seedSort: "true", seedEmpty: "false"})
	out = mergeSeed(t, input(model.RowSidebarSeed, l, cur, cur.Clone(), cur, cur, seedPolicy(true, nil)))
	if out.Desired.Items[seedSort].Hash != cur.Items[seedSort].Hash {
		t.Fatal("seed-only key written to a seeded folder")
	}
}

func TestSeedAbsentCandidateKeepsTarget(t *testing.T) {
	both := seedSnap(t, map[string]string{seedSort: "true", seedEmpty: "true"})
	// The app deleted the keys on the leaving side (sign-out): never a
	// deletion on the target.
	out := mergeSeed(t, input(model.RowSidebarSeed, snap(model.RowSidebarSeed), both, both.Clone(), both, both, seedPolicy(false, nil)))
	if len(out.Desired.Items) != 2 {
		t.Fatalf("desired %v", out.Desired.Items)
	}
}

func TestSeedNoImportNoDerivation(t *testing.T) {
	from := map[string]json.RawMessage{seedSort: json.RawMessage("true"), seedEmpty: json.RawMessage("true")}
	cur := seedSnap(t, map[string]string{seedEmpty: "false"})
	pol := seedPolicy(false, from)
	pol.EnteringImports = false
	out := mergeSeed(t, input(model.RowSidebarSeed, cur.Clone(), cur, cur, cur, cur, pol))
	if len(out.Desired.Items) != 1 || out.Desired.Items[seedEmpty].Hash != cur.Items[seedEmpty].Hash || len(out.Changes) != 0 {
		t.Fatalf("desired %v changes %+v", out.Desired.Items, out.Changes)
	}
}

func TestSeedUnknownKeyIgnored(t *testing.T) {
	from := map[string]json.RawMessage{"other-key": json.RawMessage("true")}
	cur := seedSnap(t, nil)
	out := mergeSeed(t, input(model.RowSidebarSeed, cur, cur, cur, cur, cur, seedPolicy(false, from)))
	if len(out.Desired.Items) != 0 {
		t.Fatalf("desired %v", out.Desired.Items)
	}
}

// TestSeedUnreadableTargetUnchanged: when the entering folder's state
// could not be read (store missing and seeded), nothing is written.
func TestSeedUnreadableTargetUnchanged(t *testing.T) {
	from := map[string]json.RawMessage{seedSort: json.RawMessage("true"), seedEmpty: json.RawMessage("true")}
	cur := seedSnap(t, map[string]string{seedEmpty: "false"})
	l := seedSnap(t, map[string]string{seedSort: "true", seedEmpty: "true"})
	pol := seedPolicy(true, from)
	pol.EnteringStoreMissing = true
	out := mergeSeed(t, input(model.RowSidebarSeed, l, cur, cur.Clone(), cur, cur, pol))
	if len(out.Desired.Items) != 1 || out.Desired.Items[seedEmpty].Hash != cur.Items[seedEmpty].Hash || len(out.Changes) != 0 {
		t.Fatalf("desired %v changes %+v", out.Desired.Items, out.Changes)
	}
}
