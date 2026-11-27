package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/pins"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/idb"
	"github.com/kazupc/paritydesk/internal/store/idb/idbtest"
	"github.com/kazupc/paritydesk/internal/testkit"
)

// Row pins over the real dependencies with the 7.x manifest. The pin
// lists are synthetic IndexedDB directories (idbtest) in the fixture
// folders; session ids are the fixture's fake ids.

// writePins replaces folder's pin list with the sessions ns.
func (w *realWorld) writePins(folder string, ns ...int) {
	w.t.Helper()
	ids := []string{}
	for _, n := range ns {
		ids = append(ids, testkit.FakeSession(n))
	}
	w.writePinsValue(folder, idbtest.PinValue(ids))
}

func (w *realWorld) writePinsValue(folder string, value []byte) {
	w.t.Helper()
	dir := filepath.Join(w.folder(folder), idbtest.Dir)
	if err := os.RemoveAll(dir); err != nil {
		w.t.Fatal(err)
	}
	if _, err := idbtest.Write(dir, idbtest.PinDB(value)); err != nil {
		w.t.Fatal(err)
	}
}

// breakPins makes folder's pin list unreadable: a damaged log record
// (unclean), which the backup list never stands in for.
func (w *realWorld) breakPins(folder string) {
	w.t.Helper()
	w.writePins(folder, 1)
	p := filepath.Join(w.folder(folder), idbtest.Dir, "000003.log")
	data, err := os.ReadFile(p)
	if err != nil {
		w.t.Fatal(err)
	}
	data[10] ^= 0xff
	if err := os.WriteFile(p, data, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func pinSteps(q *online.Queue) []online.Step {
	var out []online.Step
	if q == nil {
		return nil
	}
	for _, s := range q.Steps {
		if s.Kind == online.KindPin {
			out = append(out, s)
		}
	}
	return out
}

func stepSession(s online.Step) string {
	var in struct {
		SessionID string `json:"session_id"`
		Pinned    bool   `json:"pinned"`
	}
	_ = json.Unmarshal(s.Input, &in)
	if in.Pinned {
		return "+" + in.SessionID
	}
	return "-" + in.SessionID
}

func basesOf(root *state.Root, skip model.RowID) map[string]state.Ref {
	out := map[string]state.Ref{}
	for k, v := range root.Bases {
		if !strings.HasSuffix(k, "/"+string(skip)) {
			out[k] = v
		}
	}
	return out
}

func TestPinsSwitchQueuesSteps(t *testing.T) {
	w := newOnlineWorld(t)
	w.writePins("Claude", 1)
	w.writePins("Claude-b")
	res := w.switchTo(yes("b"))
	if res.Online == nil || res.Online.PinsAdd != 1 || res.Online.PinsSource != "idb" || res.Online.PinsReason != "" {
		t.Fatalf("online %+v", res.Online)
	}
	steps := pinSteps(w.queue())
	if len(steps) != 1 || stepSession(steps[0]) != "+"+testkit.FakeSession(1) {
		t.Fatalf("steps %+v", steps)
	}
	if _, skipped := res.Plan.Skipped[model.RowPins]; skipped {
		t.Fatal("pins skipped")
	}
	for _, c := range res.Plan.Changes {
		if c.Row == model.RowPins {
			t.Fatal("pins changes counted in the plan")
		}
	}
	// Both pins bases exist after the run: S's from P5, T's from P10.
	root := w.root()
	for _, a := range []string{"a", "b"} {
		found := false
		for k := range root.Bases {
			if strings.HasPrefix(k, a+"/") && strings.HasSuffix(k, "/pins") {
				found = true
			}
		}
		if !found {
			t.Fatalf("no pins base for %s: %v", a, root.Bases)
		}
	}
}

func TestPinsUnavailableSkipsOnlyThePinsRow(t *testing.T) {
	for _, side := range []string{"Claude", "Claude-b"} {
		t.Run(side, func(t *testing.T) {
			ref := newOnlineWorld(t)
			ref.cfg.Sync.Pins = false
			ref.writePins("Claude", 1)
			ref.writePins("Claude-b")
			refRes := ref.switchTo(yes("b"))

			w := newOnlineWorld(t)
			w.writePins("Claude", 1)
			w.writePins("Claude-b")
			w.breakPins(side)
			before := w.root()
			res := w.switchTo(yes("b"))
			if res.Plan.Skipped[model.RowPins] != reasonPinsUnreadable {
				t.Fatalf("skipped %v online %+v warnings %v", res.Plan.Skipped, res.Online, i18n.Strings(res.Warnings))
			}
			if res.Online == nil || res.Online.PinsSource != "none" || res.Online.PinsReason != "unclean" {
				t.Fatalf("online %+v", res.Online)
			}
			alias := "a"
			if side == "Claude-b" {
				alias = "b"
			}
			if !slices.ContainsFunc(i18n.Strings(res.Warnings), func(s string) bool { return strings.Contains(s, "pin list in "+alias) }) ||
				!slices.ContainsFunc(i18n.Strings(res.Warnings), func(s string) bool { return strings.Contains(s, "Reason: Claude did not close it cleanly") }) {
				t.Fatalf("warnings %v", i18n.Strings(res.Warnings))
			}
			after := w.root()
			if after.Canonical[model.RowPins] != before.Canonical[model.RowPins] {
				t.Fatal("pins canonical changed")
			}
			for k := range after.Bases {
				if strings.HasSuffix(k, "/pins") {
					t.Fatalf("a pins base was stored: %s", k)
				}
			}
			for k := range after.Overlays {
				if strings.HasSuffix(k, "/pins") {
					t.Fatalf("a pins overlay was stored: %s", k)
				}
			}
			// Every other row keeps the bases a run without pins stores (the
			// same keys; the worlds' contents differ in paths and times).
			got, want := basesOf(after, model.RowPins), basesOf(ref.root(), model.RowPins)
			if len(got) != len(want) {
				t.Fatalf("bases %d, want %d", len(got), len(want))
			}
			for k := range want {
				if _, ok := got[k]; !ok {
					t.Fatalf("base %s missing", k)
				}
			}
			if refRes.Plan.Skipped[model.RowPins] == "" {
				t.Fatal("reference run planned pins")
			}
		})
	}
}

func TestPinsOnlineOffSkipsWithReason(t *testing.T) {
	w := newOnlineWorld(t)
	w.cfg.Online.Reconcile = "off"
	w.writePins("Claude", 1)
	res := w.switchTo(yes("b"))
	if res.Plan.Skipped[model.RowPins] != reasonOnlineOff {
		t.Fatalf("skipped %v", res.Plan.Skipped)
	}
}

func TestPinsOnlyPinsReadsSessions(t *testing.T) {
	for _, opts := range []SwitchOptions{
		{To: "b", Yes: true, ConfirmFirstSync: true, LogOffset: -1, Only: []model.Category{model.CatPins}},
		{To: "b", Yes: true, ConfirmFirstSync: true, LogOffset: -1, Skip: []model.Category{model.CatSessions}},
	} {
		w := newOnlineWorld(t)
		w.writePins("Claude", 1, 3)
		w.writePins("Claude-b")
		res := w.switchTo(opts)
		if res.Plan.Skipped[model.RowSessions] == "" {
			t.Fatal("sessions planned")
		}
		steps := pinSteps(w.queue())
		if len(steps) != 2 || res.Online.PinsAdd != 2 {
			t.Fatalf("%v: steps %+v online %+v", opts.Only, steps, res.Online)
		}
	}
}

// unavailableAt is the pins adapter, unavailable for reads of one
// replica in one folder (T's folder after the swap: its P10 re-read).
type unavailableAt struct {
	sources.Adapter
	folder string
	rep    model.Replica
	hits   *int
}

func (u unavailableAt) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if rc.Folder == u.folder && rc.Replica == u.rep {
		*u.hits++
		return nil, &pins.Unavailable{Reason: idb.ReasonTorn}
	}
	return u.Adapter.Read(ctx, rc)
}

func TestPinsP10FallsBackToP5Read(t *testing.T) {
	w := newOnlineWorld(t)
	w.writePins("Claude", 1)
	w.writePins("Claude-b", 2)
	// b's P10 re-read (in Claude after the swap) is unavailable: its base
	// is its P5 read, without the "does not read back" warning.
	d := w.deps()
	d.Adapters = maps.Clone(d.Adapters)
	hits := 0
	d.Adapters[model.RowPins] = unavailableAt{Adapter: d.Adapters[model.RowPins], folder: w.folder("Claude"), rep: testkit.Replica(2), hits: &hits}
	w.app.running = true
	res, err := New(d).Switch(context.Background(), yes("b"))
	if err != nil || res.Plan.Skipped[model.RowPins] != "" || hits == 0 {
		t.Fatalf("switch: %v skipped %v re-reads failed %d", err, res.Plan.Skipped, hits)
	}
	if got := strings.Join(w.pinsBase("b", 2), ","); got != testkit.FakeSession(2) {
		t.Fatalf("T base %q, want its P5 read", got)
	}
	for _, warn := range i18n.Strings((res.Warnings)) {
		if strings.Contains(warn, "pins: the entering folder does not read back") {
			t.Fatal(warn)
		}
	}
}

func TestPinsProbeLifecycle(t *testing.T) {
	w := newOnlineWorld(t)
	w.writePins("Claude", 1)
	w.writePins("Claude-b")
	w.switchTo(yes("b"))
	// A confirmation with changed steps starts the probe at the queue's
	// creation.
	root := w.root()
	o := w.orch()
	rec := &state.OnlineRecord{Profile: "b", Replica: testkit.Replica(2), Created: w.clk.Now()}
	base := &model.RowSnapshot{Row: model.RowPins, Schema: 1, Items: map[string]model.Item{}}
	if moved, err := o.pinsConfirmed(root, rec, online.PinsConfirm{Base: base, Changed: 1, Moved: true}); err != nil || !moved {
		t.Fatalf("confirmed: %v %v", moved, err)
	}
	if root.PinsProbes["b"] != w.clk.Now().UnixMilli() {
		t.Fatalf("probes %v", root.PinsProbes)
	}
	// b's store was written before the probe: stale, and no mirror.
	if err := w.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	checks, err := w.orch().Doctor(context.Background(), DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// b is parked: doctor reads the active folder (Claude, profile b
	// after the switch) and finds the stale store.
	found := false
	for _, c := range checks {
		if c.ID == "pins" {
			found = true
			if c.Status != "fail" || !strings.Contains(c.Detail.String(), "no longer saves pins") {
				t.Fatalf("doctor pins %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("no pins check")
	}
	res := w.switchTo(yes("a"))
	if res.Plan.Skipped[model.RowPins] != reasonPinsUnreadable || res.Online.PinsReason != "stale_store" {
		t.Fatalf("stale: %v %+v", res.Plan.Skipped, res.Online)
	}
	if w.root().PinsProbes["b"] == 0 {
		t.Fatal("probe cleared by a failing read")
	}
	// A probe the store passes is cleared with the base it becomes.
	root = w.root()
	root.PinsProbes["b"] = idbtest.UpdatedAt
	if err := w.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	w.switchTo(yes("b"))
	if _, set := w.root().PinsProbes["b"]; set {
		t.Fatalf("probe not cleared: %v", w.root().PinsProbes)
	}
}

func TestPinsRebaseClearsProbes(t *testing.T) {
	w := newOnlineWorld(t)
	root := w.root()
	root.PinsProbes = map[ids.Alias]int64{"a": 1}
	if err := w.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	if err := w.orch().Rebase(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(w.root().PinsProbes) != 0 {
		t.Fatalf("probes %v", w.root().PinsProbes)
	}
}

// resolveQueue sets the pin steps of the published queue (as hooks and
// `online next` would have resolved them) and returns it.
func (w *onlineWorld) resolveQueue(set func(s *online.Step)) *online.Queue {
	w.t.Helper()
	q := w.queue()
	for i := range q.Steps {
		if q.Steps[i].Kind == online.KindPin {
			set(&q.Steps[i])
		}
	}
	if err := w.svc.Store.WriteQueue(q); err != nil {
		w.t.Fatal(err)
	}
	return q
}

func (w *onlineWorld) confirm() {
	w.t.Helper()
	root := w.root()
	o := w.orch()
	res, err := o.confirmPending(context.Background(), root, o.currentManifest(context.Background()))
	if err != nil {
		w.t.Fatal(err)
	}
	if res != nil && res.changed {
		if err := w.st.Commit(root); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *onlineWorld) pinsBase(alias string, n int) []string {
	w.t.Helper()
	k := state.BaseKey{Alias: ids.Alias(alias), Replica: testkit.Replica(n), Row: model.RowPins}.String()
	ref, ok := w.root().Bases[k]
	if !ok {
		return nil
	}
	snap, err := w.st.GetSnapshot(ref)
	if err != nil {
		w.t.Fatal(err)
	}
	return snap.Keys()
}

// TestPinsDeniedRemovalsStayRemoved is the review's B2 sequence: a
// unpins sessions 1 and 3; on b both unpin cards are denied. After the
// confirmation and the next harvest of b, canonical still lacks both and
// a is not offered to pin them again.
func TestPinsDeniedRemovalsStayRemoved(t *testing.T) {
	w := newOnlineWorld(t)
	w.writePins("Claude", 1, 3)
	w.writePins("Claude-b", 1, 3)
	w.switchTo(yes("b"))
	w.switchTo(yes("a")) // both pins bases are valid now
	w.writePins("Claude")
	w.switchTo(yes("b"))
	var removals []string
	for _, s := range pinSteps(w.queue()) {
		removals = append(removals, stepSession(s))
	}
	slices.Sort(removals)
	if strings.Join(removals, ",") != "-"+testkit.FakeSession(1)+",-"+testkit.FakeSession(3) {
		t.Fatalf("removals %v", removals)
	}
	w.resolveQueue(func(s *online.Step) {
		s.Status, s.Reason, s.EverUnknown = online.StatusFailed, online.ReasonNoResult, true
	})
	w.confirm()
	if got := strings.Join(w.pinsBase("b", 2), ","); got != testkit.FakeSession(1)+","+testkit.FakeSession(3) {
		t.Fatalf("b's base %s", got)
	}
	res := w.switchTo(yes("a"))
	canon, err := w.st.GetSnapshot(w.root().Canonical[model.RowPins])
	if err != nil {
		t.Fatal(err)
	}
	if len(canon.Items) != 0 {
		t.Fatalf("canonical %v", canon.Keys())
	}
	if res.Online != nil && res.Online.PinsAdd != 0 {
		t.Fatalf("a is offered pins again: %+v", res.Online)
	}
}

// TestPinsConfirmationMovesBaseAndStartsProbe: done adds move T's pins
// base and start the freshness probe at the queue's creation.
func TestPinsConfirmationMovesBaseAndStartsProbe(t *testing.T) {
	w := newOnlineWorld(t)
	w.writePins("Claude", 1)
	w.writePins("Claude-b")
	w.switchTo(yes("b"))
	if len(w.pinsBase("b", 2)) != 0 {
		t.Fatal("T base not empty before the confirmation")
	}
	w.resolveQueue(func(s *online.Step) { s.Status = online.StatusDone })
	w.confirm()
	if got := strings.Join(w.pinsBase("b", 2), ","); got != testkit.FakeSession(1) {
		t.Fatalf("T base %s", got)
	}
	root := w.root()
	if root.PinsProbes["b"] != root.Online.Created.UnixMilli() || !slices.Contains(root.Online.Confirmed, "p000") {
		t.Fatalf("probes %v confirmed %v", root.PinsProbes, root.Online.Confirmed)
	}
	// A second confirmation changes nothing.
	before := w.root()
	w.confirm()
	if after := w.root(); after.Bases[state.BaseKey{Alias: "b", Replica: testkit.Replica(2), Row: model.RowPins}.String()] !=
		before.Bases[state.BaseKey{Alias: "b", Replica: testkit.Replica(2), Row: model.RowPins}.String()] {
		t.Fatal("second confirmation moved the base")
	}
}

// TestPinsRecoverDropsOnlyThePinsBase: recover re-reads T after a crash
// between P9 and P10; an unreadable pin list drops only the pins base.
func TestPinsRereadToleratesUnavailable(t *testing.T) {
	w := newOnlineWorld(t)
	w.writePins("Claude", 1)
	w.breakPins("Claude-b")
	o := w.orch()
	m := o.currentManifest(context.Background())
	got, err := o.rereadRows(context.Background(), m, w.folder("Claude-b"), testkit.Replica(2),
		[]model.RowID{model.RowSessions, model.RowPins}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[model.RowPins]; ok {
		t.Fatal("unavailable pins read returned")
	}
	if _, ok := got[model.RowSessions]; !ok {
		t.Fatal("sessions not re-read")
	}
}

func TestPinsDoctorChecks(t *testing.T) {
	w := newOnlineWorld(t)
	checks := func() map[string]Check {
		cs, err := w.orch().Doctor(context.Background(), DoctorOptions{})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]Check{}
		for _, c := range cs {
			out[c.ID] = c
		}
		return out
	}
	w.writePins("Claude", 1, 3)
	if c := checks()["pins"]; c.Status != "ok" || !strings.Contains(c.Detail.String(), "(2 pinned session(s))") || strings.Contains(c.Detail.String(), "local_") {
		t.Fatalf("ok: %+v", c)
	}
	// The fixture's backup list is empty: it differs from the main list.
	if c := checks()["pins-mirror"]; c.Status != "warn" {
		t.Fatalf("mirror differs: %+v", c)
	}
	w.breakPins("Claude")
	if c := checks()["pins"]; c.Status != "warn" || !strings.Contains(c.Detail.String(), "cannot be read") {
		t.Fatalf("unreadable: %+v", c)
	}
	w.writePinsValue("Claude", idbtest.BlobWrapped())
	if c := checks()["pins"]; c.Status != "warn" || !strings.Contains(c.Detail.String(), "backup list") || !strings.Contains(c.Detail.String(), "(0 pinned session(s))") {
		t.Fatalf("mirror: %+v", c)
	}
	w.cfg.Sync.Pins = false
	if c := checks()["pins"]; c.Status != "ok" || !strings.Contains(c.Detail.String(), "turned off") {
		t.Fatalf("off: %+v", c)
	}
}

// writeFreshMirror puts the sessions ns into folder's backup list (the
// desktop config and its Local Storage copy), the Local Storage copy
// written at lsTime (ms).
func (w *realWorld) writeFreshMirror(folder string, lsTime int64, ns ...int) {
	w.t.Helper()
	list := []string{}
	for _, n := range ns {
		list = append(list, testkit.FakeSession(n))
	}
	raw, err := json.Marshal(list)
	if err != nil {
		w.t.Fatal(err)
	}
	cfgPath := filepath.Join(w.folder(folder), sources.DesktopConfigFile)
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := jsonx.Set(cfg, jsonx.Path{"preferences", "epitaxyPrefs", "starred-local-code-sessions"}, raw)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, out, 0o600); err != nil {
		w.t.Fatal(err)
	}
	testkit.WriteLS(w.t, w.folder(folder), map[string]string{
		"LSS-persisted.starred-local-code-sessions": fmt.Sprintf(`{"value":%s,"tabId":"x","timestamp":%d}`, raw, lsTime),
	})
}

func (w *onlineWorld) setProbe(alias ids.Alias, since int64) {
	w.t.Helper()
	root := w.root()
	if root.PinsProbes == nil {
		root.PinsProbes = map[ids.Alias]int64{}
	}
	root.PinsProbes[alias] = since
	if err := w.st.Commit(root); err != nil {
		w.t.Fatal(err)
	}
}

// TestPinsP10KeepsTheReadTheProbePassed: T's IndexedDB value is older than
// the probe and its fresh backup list passes it at P5. The P10 re-read
// gets the same side facts: T's base is the list the probe accepted (not
// the stale store's), and the probe is cleared only with that read.
func TestPinsP10KeepsTheReadTheProbePassed(t *testing.T) {
	w := newOnlineWorld(t)
	w.writePins("Claude", 1)
	w.writePins("Claude-b") // empty list, updatedAt = idbtest.UpdatedAt
	w.setProbe("b", idbtest.UpdatedAt+10)
	w.writeFreshMirror("Claude-b", idbtest.UpdatedAt+100, 2)
	res := w.switchTo(yes("b"))
	if res.Online == nil || res.Online.PinsSource != "mirror" {
		t.Fatalf("online %+v skipped %v", res.Online, res.Plan.Skipped)
	}
	if got := strings.Join(w.pinsBase("b", 2), ","); got != testkit.FakeSession(2) {
		t.Fatalf("T's pins base %q is not the list the probe passed", got)
	}
	if _, set := w.root().PinsProbes["b"]; set {
		t.Fatalf("probe not cleared with the read that passed it: %v", w.root().PinsProbes)
	}
}

// TestPinsRereadChecksTheProbe: a re-read outside a run (recover) with
// the profile's probe leaves a stale store out (the pins base is dropped)
// instead of returning the stale list, and takes a fresh backup list.
func TestPinsRereadChecksTheProbe(t *testing.T) {
	w := newOnlineWorld(t)
	w.writePins("Claude-b", 1)
	w.setProbe("b", idbtest.UpdatedAt+10)
	o := w.orch()
	ctx := context.Background()
	m := o.currentManifest(ctx)
	rows := []model.RowID{model.RowSessions, model.RowPins}
	got, err := o.rereadRowsPins(ctx, m, w.folder("Claude-b"), testkit.Replica(2), rows, nil, o.pinsRereadFacts(w.root(), "b", testkit.Replica(2)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[model.RowPins]; ok {
		t.Fatal("a stale store was re-read as the pin list")
	}
	w.writeFreshMirror("Claude-b", idbtest.UpdatedAt+100, 1)
	got, err = o.rereadRowsPins(ctx, m, w.folder("Claude-b"), testkit.Replica(2), rows[1:], nil, o.pinsRereadFacts(w.root(), "b", testkit.Replica(2)))
	if err != nil {
		t.Fatal(err)
	}
	if snap := got[model.RowPins]; snap == nil || strings.Join(snap.Keys(), ",") != testkit.FakeSession(1) {
		t.Fatalf("fresh backup list not taken: %+v", snap)
	}
}

// TestPinsProbeFollowsTheProfile: rename moves the probe with the bases;
// remove drops it, so a profile added later under the alias does not
// inherit it.
func TestPinsProbeFollowsTheProfile(t *testing.T) {
	w := newOnlineWorld(t)
	w.setProbe("b", 1)
	ctx := context.Background()
	if err := w.orch().ProfileRename(ctx, "b", "c"); err != nil {
		t.Fatal(err)
	}
	if p := w.root().PinsProbes; p["c"] != 1 || len(p) != 1 {
		t.Fatalf("probes after rename: %v", p)
	}
	if err := w.orch().ProfileRemove(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if p := w.root().PinsProbes; len(p) != 0 {
		t.Fatalf("probes after remove: %v", p)
	}
}

// warnPin is alwaysPin with the warnings the real provider gives for
// removals it does not queue.
type warnPin struct{ alwaysPin }

func (warnPin) Build(in online.BuildInput) ([]online.Step, []online.Warning, error) {
	steps, _, err := alwaysPin{}.Build(in)
	return steps, []online.Warning{
		{Msg: string(i18n.OnlineWarnPinsFamilyBlocked), Text: i18n.Tx(i18n.OnlineWarnPinsFamilyBlocked, 1)},
		{Msg: string(i18n.OnlineWarnPinsHidden), Text: i18n.Tx(i18n.OnlineWarnPinsHidden, 1)},
		{Msg: string(i18n.OnlineWarnPinsCapped), Text: i18n.Tx(i18n.OnlineWarnPinsCapped, 50)},
	}, err
}

// TestPinsWarningsReachTheResult: the pin provider's reasons reach
// i18n.Strings((Result.Warnings)) (plan --json carries them), once, at P2 and at P5.
func TestPinsWarningsReachTheResult(t *testing.T) {
	want := []string{i18n.T(i18n.OnlineWarnPinsFamilyBlocked, 1), i18n.T(i18n.OnlineWarnPinsHidden, 1), i18n.T(i18n.OnlineWarnPinsCapped, 50)}
	check := func(name string, ws []string) {
		t.Helper()
		for _, x := range want {
			if n := slices.Index(ws, x); n < 0 || slices.Index(ws[n+1:], x) >= 0 {
				t.Fatalf("%s: warning %q not exactly once in %q", name, x, ws)
			}
		}
	}
	w := newOnlineWorld(t)
	w.svc.Providers = []online.Provider{online.ProbeProvider(), online.SidebarProvider(), warnPin{}}
	prev, err := w.orch().Preview(context.Background(), PreviewOptions{To: "b"})
	if err != nil {
		t.Fatal(err)
	}
	check("plan", i18n.Strings(prev.Warnings))
	check("switch", i18n.Strings(w.switchTo(yes("b")).Warnings))
}

// TestPinsNeedAPinProvider: with an online layer whose registry has no
// provider of pin steps, row pins is not planned (spec-pins 6.4 item 1).
func TestPinsNeedAPinProvider(t *testing.T) {
	w := newOnlineWorld(t)
	w.svc.Providers = []online.Provider{online.ProbeProvider(), online.SidebarProvider()}
	w.writePins("Claude", 1)
	res := w.switchTo(yes("b"))
	if res.Plan.Skipped[model.RowPins] != reasonOnlineOff {
		t.Fatalf("skipped %v", res.Plan.Skipped)
	}
	if _, ok := w.root().Bases[state.BaseKey{Alias: "b", Replica: testkit.Replica(2), Row: model.RowPins}.String()]; ok {
		t.Fatal("pins base moved without a provider")
	}
}

// TestPinsConfirmKeepsPinCounts: `online done` and the confirmation verify
// applies record the pin counts (only the pins provider knows them).
func TestPinsConfirmKeepsPinCounts(t *testing.T) {
	w := newOnlineWorld(t)
	w.writePins("Claude", 1)
	w.writePins("Claude-b")
	w.switchTo(yes("b"))
	q := w.queue()
	l, err := w.st.Lock(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.orch().OnlineConfirm(context.Background(), OnlineConfirmInput{Queue: q.ID})
	_ = l.Release()
	if err != nil || !out.RootBusy || out.Counts.PinsAdd != 1 {
		t.Fatalf("done: %+v %v", out, err)
	}
	w.app.running = true
	if _, err := w.orch().Verify(context.Background()); err != nil && pderr.CodeOf(err) != pderr.VerifyDrift {
		t.Fatal(err)
	}
	if rec := w.root().Online; rec.State != string(online.QueueComplete) || rec.Counts.PinsAdd != 1 {
		t.Fatalf("record %+v", rec)
	}
	// Without the busy lock, done writes the counts itself.
	w2 := newOnlineWorld(t)
	w2.writePins("Claude", 1)
	w2.writePins("Claude-b")
	w2.switchTo(yes("b"))
	out, err = w2.orch().OnlineConfirm(context.Background(), OnlineConfirmInput{Queue: w2.queue().ID})
	if err != nil || out.Counts.PinsAdd != 1 || w2.root().Online.Counts.PinsAdd != 1 {
		t.Fatalf("done: %+v %v record %+v", out, err, w2.root().Online)
	}
}
