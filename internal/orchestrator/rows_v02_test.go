package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
)

// flakyAdapter is a row adapter whose source can be made unavailable,
// on both sides or only in folders whose path ends in failIn.
type flakyAdapter struct {
	fileAdapter
	fail   bool
	failIn string
	other  error // returned instead of ErrSourceUnavailable when set
}

func (a *flakyAdapter) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if a.fail && (a.failIn == "" || strings.HasSuffix(rc.Folder, a.failIn)) {
		if a.other != nil {
			return nil, a.other
		}
		return nil, fmt.Errorf("%s: unavailable: %w", a.row, sources.ErrSourceUnavailable)
	}
	return a.fileAdapter.Read(ctx, rc)
}

// refAdapter is read only with ReadRefs; Read must never be called.
type refAdapter struct {
	fileAdapter
	reads, refReads int
	refs            [][]string
}

func (a *refAdapter) Read(context.Context, sources.ReadContext) (*model.RowSnapshot, error) {
	a.reads++
	return nil, sources.ErrRefsRequired
}

func (a *refAdapter) ReadRefs(ctx context.Context, rc sources.ReadContext, refs []string) (*model.RowSnapshot, error) {
	a.refReads++
	a.refs = append(a.refs, refs)
	return a.fileAdapter.Read(ctx, rc)
}

// policyModule records the policy it was given.
type policyModule struct {
	fakeModule
	got *rows.Policy
}

func (m policyModule) Merge(in rows.Input) (rows.Output, error) {
	*m.got = in.Policy
	return m.fakeModule.Merge(in)
}

// twoSwitches runs a→b with every row, then b→a with the environments row
// made unavailable (fail) or disabled in the config (disable). It returns
// the root before and after the second switch and its result.
func twoSwitches(t *testing.T, failIn string, fail, disable bool) (before, after *state.Root, res *Result) {
	t.Helper()
	e := newEnv(t)
	fa := &flakyAdapter{fileAdapter: fileAdapter{model.RowEnvironments}, failIn: failIn}
	orch := func() *Orchestrator {
		d := e.deps()
		d.Adapters[model.RowEnvironments] = fa
		return New(d)
	}
	if _, err := orch().Switch(context.Background(), yes("b")); err != nil {
		t.Fatalf("first switch: %v", err)
	}
	// Both profiles change something before the second switch.
	e.writeRow(ids.DefaultFolder, model.RowSessions, map[string]any{
		string(fakeSession(1)): map[string]any{"title": "one"}, string(fakeSession(2)): map[string]any{"title": "two"},
		string(fakeSession(3)): map[string]any{"title": "three"},
	})
	e.writeRow(ids.DefaultFolder, model.RowEnvironments, map[string]any{"preset/p9": map[string]any{"name": "env nine"}})
	e.writeRow("Claude-a", model.RowEnvironments, map[string]any{"preset/p1": map[string]any{"name": "env one, renamed"}})
	before = e.st.root(t)
	e.markBases()
	fa.fail = fail
	if disable {
		e.cfg.Sync.Environments = false
	}
	res, err := orch().Switch(context.Background(), yes("a"))
	if err != nil {
		t.Fatalf("second switch: %v", err)
	}
	e.checkInvariants("second switch")
	return before, e.st.root(t), res
}

// TestSourceUnavailableSkipsOnlyItsRow: an adapter whose source is
// unavailable leaves only its row out. Every other row's bases and
// canonical are byte-identical to a run in which that row is disabled, and
// the row's own bases and canonical are untouched.
func TestSourceUnavailableSkipsOnlyItsRow(t *testing.T) {
	const row = model.RowEnvironments
	refBefore, ref, refRes := twoSwitches(t, "", false, true)
	if refRes.Plan.Skipped[row] != "disabled in the config" {
		t.Fatalf("reference skipped = %v", refRes.Plan.Skipped)
	}
	for _, tc := range []struct {
		name   string
		failIn string
	}{{"both sides", ""}, {"entering side only", "Claude-a"}, {"leaving side only", ids.DefaultFolder}} {
		t.Run(tc.name, func(t *testing.T) {
			before, after, res := twoSwitches(t, tc.failIn, true, false)
			if res.Code != pderr.OK {
				t.Fatalf("code %d", res.Code)
			}
			if got := res.Plan.Skipped[row]; got != reasonSourceUnavailable {
				t.Fatalf("skipped = %v", res.Plan.Skipped)
			}
			for _, ch := range res.Plan.Changes {
				if ch.Row == row {
					t.Errorf("change of the skipped row: %+v", ch)
				}
			}
			for _, op := range res.Plan.Ops {
				if op.Row == row {
					t.Errorf("op of the skipped row: %+v", op)
				}
			}
			if !maps.Equal(after.Bases, ref.Bases) {
				t.Errorf("bases differ from the disabled-row run:\n got %v\nwant %v", after.Bases, ref.Bases)
			}
			if !maps.Equal(after.Canonical, ref.Canonical) || !maps.Equal(after.Overlays, ref.Overlays) {
				t.Errorf("canonical or overlays differ from the disabled-row run")
			}
			for k, v := range before.Bases {
				if strings.HasSuffix(k, "/"+string(row)) && after.Bases[k] != v {
					t.Errorf("base %s of the skipped row changed", k)
				}
			}
			if after.Canonical[row] != before.Canonical[row] || before.Canonical[row] == "" {
				t.Error("canonical of the skipped row changed")
			}
			// Sanity: the other rows did move on in this run.
			if after.Canonical[model.RowSessions] == before.Canonical[model.RowSessions] {
				t.Error("sessions canonical did not change")
			}
			if !maps.Equal(refBefore.Bases, before.Bases) || !maps.Equal(refBefore.Canonical, before.Canonical) {
				t.Error("the two runs did not start from the same state")
			}
		})
	}
}

// TestSourceUnavailablePrintsNoGenericWarning: buildPlan records the skip
// in Plan.Skipped only; warnings belong to the unit that owns the reason.
func TestSourceUnavailablePrintsNoGenericWarning(t *testing.T) {
	e := newEnv(t)
	fa := &flakyAdapter{fileAdapter: fileAdapter{model.RowEnvironments}, fail: true}
	d := e.deps()
	d.Adapters[model.RowEnvironments] = fa
	res, err := New(d).Switch(context.Background(), yes("b"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan.Skipped[model.RowEnvironments] != reasonSourceUnavailable {
		t.Fatalf("skipped = %v", res.Plan.Skipped)
	}
	for _, ev := range e.ui.events {
		if strings.Contains(ev.Message.String(), reasonSourceUnavailable) {
			t.Errorf("generic skip warning printed: %+v", ev)
		}
	}
}

func TestOtherReadErrorsStillFail(t *testing.T) {
	e := newEnv(t)
	fa := &flakyAdapter{fileAdapter: fileAdapter{model.RowGroupOrder}, fail: true, other: errors.New("malformed group-order")}
	d := e.deps()
	d.Adapters[model.RowGroupOrder] = fa
	_, err := New(d).Switch(context.Background(), yes("b"))
	if pderr.CodeOf(err) != pderr.Internal || !strings.Contains(err.Error(), "read group-order") {
		t.Fatalf("err %v", err)
	}
	if e.account(ids.DefaultFolder) != acctA {
		t.Fatal("folders swapped after a failed read")
	}
}

func TestRereadRowsRefsAndUnavailable(t *testing.T) {
	e := newEnv(t)
	e.writeRow(ids.DefaultFolder, model.RowGroupOrder, map[string]any{"order": []string{"g1"}})
	ra := &refAdapter{fileAdapter: fileAdapter{model.RowGroupOrder}}
	fa := &flakyAdapter{fileAdapter: fileAdapter{model.RowEnvironments}}
	d := e.deps()
	d.Adapters[model.RowGroupOrder] = ra
	d.Adapters[model.RowEnvironments] = fa
	o := New(d)
	ctx := context.Background()
	m := testManifest()
	folder := filepath.Join(e.root, ids.DefaultFolder)
	all := []model.RowID{model.RowEnvironments, model.RowSessions, model.RowGroupOrder}

	got, err := o.rereadRows(ctx, m, folder, repA, all, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[model.RowSessions] == nil || got[model.RowEnvironments] == nil {
		t.Fatalf("rows missing: %v", slices.Collect(maps.Keys(got)))
	}
	if _, ok := got[model.RowGroupOrder]; ok || ra.reads != 0 || ra.refReads != 0 {
		t.Fatalf("a RefReader row without refs was read (reads %d, refReads %d)", ra.reads, ra.refReads)
	}

	got, err = o.rereadRows(ctx, m, folder, repA, all, map[model.RowID][]string{model.RowGroupOrder: {"k1", "k2"}})
	if err != nil {
		t.Fatal(err)
	}
	if s := got[model.RowGroupOrder]; s == nil || len(s.Items) != 1 || ra.reads != 0 || ra.refReads != 1 || !slices.Equal(ra.refs[0], []string{"k1", "k2"}) {
		t.Fatalf("ReadRefs not used: %+v reads %d refReads %d refs %v", s, ra.reads, ra.refReads, ra.refs)
	}

	fa.fail = true
	got, err = o.rereadRows(ctx, m, folder, repA, all, nil)
	if err != nil {
		t.Fatalf("an unavailable source failed the re-read: %v", err)
	}
	if _, ok := got[model.RowEnvironments]; ok || got[model.RowSessions] == nil {
		t.Fatalf("unavailable row kept or other row lost: %v", slices.Collect(maps.Keys(got)))
	}

	fa.other = errors.New("broken")
	if _, err := o.rereadRows(ctx, m, folder, repA, all, nil); pderr.CodeOf(err) != pderr.Internal || !strings.Contains(err.Error(), "re-read environments") {
		t.Fatalf("other error = %v", err)
	}

	// readRow reads one row on its own, like rereadRows does.
	fa.fail = false
	one, err := o.readRow(ctx, m, folder, repA, model.RowSessions)
	if err != nil || one == nil || !sameItems(one, got[model.RowSessions]) {
		t.Fatalf("readRow = %+v, %v", one, err)
	}
	if s, err := o.readRow(ctx, m, folder, repA, model.RowGroupOrder); s != nil || err != nil || ra.reads != 0 {
		t.Fatalf("readRow of a RefReader row = %+v, %v (reads %d)", s, err, ra.reads)
	}
	if s, err := o.readRow(ctx, m, folder, repA, model.RowPins); s != nil || err != nil {
		t.Fatalf("readRow of a row without adapter = %+v, %v", s, err)
	}
}

func TestMergeRowRefsAndEnvRemap(t *testing.T) {
	e := newEnv(t)
	ra := &refAdapter{fileAdapter: fileAdapter{model.RowGroupOrder}}
	var sessPolicy, groupPolicy rows.Policy
	d := e.deps()
	d.Adapters[model.RowGroupOrder] = ra
	d.Rows[model.RowSessions] = policyModule{fakeModule{model.RowSessions}, &sessPolicy}
	d.Rows[model.RowGroupOrder] = policyModule{fakeModule{model.RowGroupOrder}, &groupPolicy}
	o := New(d)
	in := planInput{
		S:        sideRef{Alias: "a", Replica: repA, Folder: filepath.Join(e.root, ids.DefaultFolder)},
		T:        sideRef{Alias: "b", Replica: repB, Folder: filepath.Join(e.root, "Claude-b")},
		Manifest: testManifest(), Root: state.NewRoot(e.root),
	}
	remap := map[string]string{"env-s": "env-t"}
	prior := map[model.RowID]*rows.Output{model.RowEnvironments: {EnvRemap: remap}}
	w, err := o.mergeRow(context.Background(), in, model.RowGroupOrder, nil, prior, []string{"x/y"})
	if err != nil {
		t.Fatal(err)
	}
	if ra.reads != 0 || ra.refReads != 2 || !slices.Equal(ra.refs[0], []string{"x/y"}) || !slices.Equal(w.Refs, []string{"x/y"}) {
		t.Fatalf("refs: reads %d refReads %d refs %v work %v", ra.reads, ra.refReads, ra.refs, w.Refs)
	}
	if groupPolicy.EnvRemap != nil {
		t.Error("the environment remap reached a row other than sessions and sidebar-collapse")
	}
	if _, err := o.mergeRow(context.Background(), in, model.RowSessions, nil, prior, nil); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(sessPolicy.EnvRemap, remap) {
		t.Errorf("sessions remap = %v", sessPolicy.EnvRemap)
	}
	// Without an environments output the remap is nil, never a panic.
	if _, err := o.mergeRow(context.Background(), in, model.RowSessions, nil, nil, nil); err != nil || sessPolicy.EnvRemap != nil {
		t.Errorf("remap without prior = %v, %v", sessPolicy.EnvRemap, err)
	}
	// An unavailable source on either side is a skip, not a failure.
	fa := &flakyAdapter{fileAdapter: fileAdapter{model.RowGroupOrder}, fail: true, failIn: "Claude-b"}
	d.Adapters[model.RowGroupOrder] = fa
	_, err = New(d).mergeRow(context.Background(), in, model.RowGroupOrder, nil, prior, nil)
	var skip *errRowSkipped
	if !errors.As(err, &skip) || skip.Reason != reasonSourceUnavailable {
		t.Fatalf("err %v", err)
	}
}

func TestMergeSkipped(t *testing.T) {
	before := map[model.RowID]string{model.RowPins: "not covered by the compatibility manifest"}
	if got := mergeSkipped(before, nil); !maps.Equal(got, before) {
		t.Fatal(got)
	}
	got := mergeSkipped(before, map[model.RowID]string{model.RowGroupOrder: reasonSourceUnavailable})
	if len(got) != 2 || len(before) != 1 {
		t.Fatalf("got %v, before %v", got, before)
	}
	if got := mergeSkipped(nil, map[model.RowID]string{model.RowGroupOrder: "x"}); got[model.RowGroupOrder] != "x" {
		t.Fatal(got)
	}
}

// TestV02RowsListedAsSkipped: the v0.2 rows, which no manifest declares
// yet, are listed as skipped with the usual reasons.
func TestV02RowsListedAsSkipped(t *testing.T) {
	e := newEnv(t)
	o := e.orch()
	rs, skipped := o.selectRows(testManifest(), compat.Decision{Mode: compat.ModeFull}, nil, nil)
	for _, r := range []model.RowID{model.RowScratch, model.RowSidebarCollapse, model.RowSidebarSeed, model.RowPins} {
		if slices.Contains(rs, r) {
			t.Errorf("%s selected", r)
		}
		if skipped[r] != "not covered by the compatibility manifest" {
			t.Errorf("%s: %q", r, skipped[r])
		}
	}
	e.cfg.Sync.Pins = false
	e.cfg.Scratch.Enabled = false
	_, skipped = e.orch().selectRows(testManifest(), compat.Decision{Mode: compat.ModeFull}, nil, nil)
	if skipped[model.RowPins] != "disabled in the config" || skipped[model.RowScratch] != "disabled in the config" {
		t.Errorf("disabled rows: %v", skipped)
	}
}
