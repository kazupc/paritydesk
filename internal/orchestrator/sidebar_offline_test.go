package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
)

// Tests of the sidebar-offline rows (sidebar-collapse, sidebar-seed) in
// the fake environment: what the orchestrator hands their merges, and how
// it selects them.

// stateAdapter is a file adapter that also reports a target state.
type stateAdapter struct {
	fileAdapter
	ts  sources.TargetState
	err error
}

func (a stateAdapter) TargetState(context.Context, sources.ReadContext) (sources.TargetState, error) {
	return a.ts, a.err
}

// remapModule is the fake environments module plus an environment remap.
type remapModule struct{ fakeModule }

func (m remapModule) Merge(in rows.Input) (rows.Output, error) {
	out, err := m.fakeModule.Merge(in)
	out.EnvRemap = map[string]string{"p1": "p2"}
	return out, err
}

// sidebarRowSpecs are the manifest rows of the sidebar-offline tests (on
// row files, like the other fake rows).
func sidebarRowSpecs() []compat.RowSpec {
	return []compat.RowSpec{
		{Row: model.RowSidebarView, WritePath: compat.PathOffVerify, Files: []string{rowFile(model.RowSidebarView)}},
		{Row: model.RowSidebarCollapse, WritePath: compat.PathOffVerify, Contested: true, Files: []string{rowFile(model.RowSidebarCollapse)},
			Limits: map[string]int{"max_members": 4096, "max_member_bytes": 2048},
			Fields: map[string][]string{rows.RoleSets: {"collapsedGroups", "collapsedFamilies"}}},
		{Row: model.RowSidebarSeed, WritePath: compat.PathOffVerify, Contested: true, Files: []string{rowFile(model.RowSidebarSeed)},
			Fields: map[string][]string{rows.RoleSeedKeys: {"sort", "empty"}, rows.RoleSeedOnly: {"sort"},
				rows.RoleSeedSection: {"sessions"}, rows.RoleSeedFrom: {"ascending", "show.emptyGroups"}}},
	}
}

// withSidebarRows adds the three sidebar rows to e's manifest and data;
// mods replaces the default modules by row.
func withSidebarRows(e *env, collapse sources.Adapter, mods map[model.RowID]rows.Module) {
	e.compat.m.Rows = append(e.compat.m.Rows, sidebarRowSpecs()...)
	e.writeRow(ids.DefaultFolder, model.RowSidebarView, map[string]any{
		"section:sessions": map[string]any{"id": "sessions", "ascending": true, "show": map[string]any{"emptyGroups": false}}})
	e.writeRow("Claude-b", model.RowSidebarView, map[string]any{})
	e.writeRow(ids.DefaultFolder, model.RowSidebarCollapse, map[string]any{"collapsedGroups": []string{"a"}})
	e.writeRow("Claude-b", model.RowSidebarCollapse, map[string]any{"collapsedGroups": []string{"b"}})
	e.writeRow(ids.DefaultFolder, model.RowSidebarSeed, map[string]any{"empty": true})
	e.writeRow("Claude-b", model.RowSidebarSeed, map[string]any{})
	reg := rows.Registry()
	e.customize = func(d *Deps) {
		d.Adapters[model.RowSidebarView] = fileAdapter{model.RowSidebarView}
		d.Adapters[model.RowSidebarCollapse] = collapse
		d.Adapters[model.RowSidebarSeed] = fileAdapter{model.RowSidebarSeed}
		d.Rows[model.RowSidebarView] = fakeModule{model.RowSidebarView}
		d.Rows[model.RowSidebarCollapse] = reg[model.RowSidebarCollapse]
		d.Rows[model.RowSidebarSeed] = reg[model.RowSidebarSeed]
		for r, m := range mods {
			d.Rows[r] = m
		}
	}
}

func TestSidebarPolicyFacts(t *testing.T) {
	e := newEnv(t)
	got := map[model.RowID]*rows.Policy{}
	mods := map[model.RowID]rows.Module{model.RowEnvironments: remapModule{fakeModule{model.RowEnvironments}}}
	for _, r := range []model.RowID{model.RowSessions, model.RowGroupOrder, model.RowSidebarView, model.RowSidebarCollapse, model.RowSidebarSeed} {
		got[r] = &rows.Policy{}
		mods[r] = policyModule{fakeModule: fakeModule{r}, got: got[r]}
	}
	withSidebarRows(e, stateAdapter{fileAdapter: fileAdapter{model.RowSidebarCollapse}, ts: sources.TargetState{StoreMissing: true}}, mods)
	res, err := e.orch().Switch(context.Background(), yes("b"))
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	for r, p := range got {
		wantRemap := r == model.RowSessions || r == model.RowSidebarCollapse
		if (p.EnvRemap != nil) != wantRemap {
			t.Errorf("%s: EnvRemap %v", r, p.EnvRemap)
		}
		if (p.SeedFrom != nil) != (r == model.RowSidebarSeed) {
			t.Errorf("%s: SeedFrom %v", r, p.SeedFrom)
		}
	}
	seed := got[model.RowSidebarSeed]
	if string(seed.SeedFrom["sort"]) != "true" || string(seed.SeedFrom["empty"]) != "false" {
		t.Errorf("SeedFrom %v", seed.SeedFrom)
	}
	col := got[model.RowSidebarCollapse]
	if col.Limits["max_members"] != 4096 || !col.EnteringStoreMissing || col.EnteringSeeded {
		t.Errorf("collapse policy limits %v store missing %v seeded %v", col.Limits, col.EnteringStoreMissing, col.EnteringSeeded)
	}
	if got[model.RowSessions].EnteringStoreMissing {
		t.Error("target state reached a row whose adapter has none")
	}
	if res.Plan.Skipped[model.RowSidebarSeed] != "" {
		t.Errorf("seed skipped: %v", res.Plan.Skipped)
	}
}

func TestSidebarTargetStateErrorLeavesRowUnchanged(t *testing.T) {
	e := newEnv(t)
	withSidebarRows(e, stateAdapter{fileAdapter: fileAdapter{model.RowSidebarCollapse}, err: errors.New("unreadable")}, nil)
	before, _, _ := readContained(filepath.Join(e.root, "Claude-b"), rowFile(model.RowSidebarCollapse))
	res, err := e.orch().Switch(context.Background(), yes("b"))
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	warned := false
	for _, ev := range e.ui.events {
		if ev.Status == "warn" && strings.Contains(ev.Message, "sidebar-collapse: the entering folder's state could not be read") {
			warned = true
		}
	}
	if !warned {
		t.Error("no warning")
	}
	for _, op := range res.Plan.Ops {
		if op.Path == rowFile(model.RowSidebarCollapse) {
			t.Errorf("collapse row written: %+v", op)
		}
	}
	for _, c := range res.Plan.Changes {
		if c.Row == model.RowSidebarCollapse && c.Kind != model.ChangeSkipped {
			t.Errorf("change %+v", c)
		}
	}
	if after, _, _ := readContained(filepath.Join(e.root, ids.DefaultFolder), rowFile(model.RowSidebarCollapse)); string(after) != string(before) {
		t.Errorf("collapse row of b changed: %s → %s", before, after)
	}
	e.checkInvariants("target state error")
}

// TestSidebarSeedCarriesView: with the real seed and collapse merges on
// row files, the entering folder gets the carried Sessions values and the
// union of both collapse sets (first sync, no confirmation for these
// rows).
func TestSidebarSeedCarriesView(t *testing.T) {
	e := newEnv(t)
	withSidebarRows(e, fileAdapter{model.RowSidebarCollapse}, nil)
	res, err := e.orch().Switch(context.Background(), yes("b"))
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	seed := e.keys(ids.DefaultFolder, model.RowSidebarSeed)
	if strings.Join(seed, ",") != "empty,sort" {
		t.Fatalf("seed keys in b: %v", seed)
	}
	for _, c := range res.Plan.Changes {
		if (c.Row == model.RowSidebarSeed || c.Row == model.RowSidebarCollapse) && c.Kind == model.ChangeDeleted {
			t.Errorf("deletion %+v", c)
		}
	}
	e.checkInvariants("seed carries view")
}

func TestSelectRowsSeedNeedsSidebarView(t *testing.T) {
	e := newEnv(t)
	m := &compat.Manifest{Rows: sidebarRowSpecs()}
	reg := rows.Registry()
	d := e.deps()
	for _, r := range []model.RowID{model.RowSidebarView, model.RowSidebarCollapse, model.RowSidebarSeed} {
		d.Adapters[r] = fileAdapter{r}
		d.Rows[r] = reg[r]
	}
	o := New(d)
	_, skipped := o.selectRows(m, compat.Decision{}, nil, nil)
	if _, ok := skipped[model.RowSidebarSeed]; ok {
		t.Fatalf("seed skipped with sidebar-view planned: %v", skipped[model.RowSidebarSeed])
	}
	tests := []struct {
		name string
		m    *compat.Manifest
		dec  compat.Decision
		skip []model.Category
		want string
	}{
		{"view skipped by its probe", m, compat.Decision{SkippedRows: []model.RowID{model.RowSidebarView}}, nil, reasonNeedsSidebarView},
		{"view not in the manifest", &compat.Manifest{Rows: sidebarRowSpecs()[1:]}, compat.Decision{}, nil, reasonNeedsSidebarView},
		{"sidebar category skipped", m, compat.Decision{}, []model.Category{model.CatSidebar}, "skipped (--skip)"},
	}
	for _, tc := range tests {
		out, skipped := o.selectRows(tc.m, tc.dec, nil, tc.skip)
		for _, r := range out {
			if r == model.RowSidebarSeed {
				t.Errorf("%s: seed selected", tc.name)
			}
		}
		if skipped[model.RowSidebarSeed] != tc.want {
			t.Errorf("%s: reason %q, want %q", tc.name, skipped[model.RowSidebarSeed], tc.want)
		}
	}
	e.cfg.Sync.SidebarView = false
	if _, skipped := New(d).selectRows(m, compat.Decision{}, nil, nil); skipped[model.RowSidebarSeed] != "disabled in the config" {
		t.Errorf("sidebar_view off: %q", skipped[model.RowSidebarSeed])
	}
}
