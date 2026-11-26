package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

func page(n int) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html><head><title>T</title></head><body>")
	for i := 0; i < n; i++ {
		b.WriteString("\n<p>line</p>")
	}
	b.WriteString("\n</body></html>")
	return b.String()
}

// TestSyncCarriesArtifacts: home's artifacts are saved, created on work
// (one verified first), an edit on work reaches home, a rename on home
// reaches work.
func TestSyncCarriesArtifacts(t *testing.T) {
	h := newHarness(t)
	home := h.servers["home"]
	a1 := home.add("Quarterly notes", page(3))
	home.add("Reading list", page(5))
	home.add("Plans", page(2000)) // two parts at 1500 lines
	out := h.sync("apply")
	if out.State != StateComplete {
		t.Fatalf("home sync: %+v", out)
	}
	m := h.manifest()
	if len(m.Pairs) != 3 {
		t.Fatalf("pairs %d", len(m.Pairs))
	}
	for id, p := range m.Pairs {
		if p.Content == nil || p.Copies["home"] == nil || p.Copies["home"].SyncedSHA != p.Content.SHA256 {
			t.Fatalf("pair %s not in step: %+v", id, p)
		}
		if _, err := h.svc.Archive.ReadBlob(p.Content.SHA256); err != nil {
			t.Fatalf("blob: %v", err)
		}
	}
	if pr := m.Profiles["home"]; pr == nil || pr.LastSyncAt.IsZero() || pr.LastSyncEpoch != 1 {
		t.Fatalf("profile record %+v", pr)
	}

	h.switchTo("work")
	if n, _ := h.svc.Due("work", repWork); n != 3 {
		t.Fatalf("due on work %d", n)
	}
	out = h.sync("apply")
	if out.State != StateComplete {
		t.Fatalf("work sync: %+v", out)
	}
	work := h.servers["work"]
	if len(work.arts) != 3 || work.creates != 3 {
		t.Fatalf("work has %d artifacts (%d creates)", len(work.arts), work.creates)
	}
	m = h.manifest()
	if m.Gate.Roundtrip != GateExact || m.Gate.AppVersion != appVer {
		t.Fatalf("gate %+v", m.Gate)
	}
	for _, p := range m.Pairs {
		c := p.Copies["work"]
		if c == nil || c.State != StateLive || c.Origin != OriginCreated || c.SyncedSHA != p.Content.SHA256 {
			t.Fatalf("work copy %+v", c)
		}
		if got := work.arts[c.Slug].content; shaHex([]byte(got)) != p.Content.SHA256 {
			t.Fatal("published content differs from the archive")
		}
	}
	if n, _ := h.svc.Due("work", repWork); n != 0 {
		t.Fatalf("due after sync %d", n)
	}

	// An edit on work is saved and published on home.
	id := PairIDFor(a1.slug)
	wslug := m.Pairs[id].Copies["work"].Slug
	h.clk.Advance(time.Hour)
	work.edit(work.arts[wslug], page(4))
	h.sync("apply")
	m = h.manifest()
	if m.Pairs[id].Content.SourceProfile != "work" || len(m.Pairs[id].History) != 1 {
		t.Fatalf("edit not saved: %+v", m.Pairs[id])
	}
	h.switchTo("home")
	h.sync("apply")
	if home.arts[a1.slug].content != page(4) {
		t.Fatal("edit not published on home")
	}
	// A rename on home reaches work.
	h.clk.Advance(time.Hour)
	home.arts[a1.slug].title = "Quarterly notes (v2)"
	home.arts[a1.slug].version++
	h.sync("apply")
	h.switchTo("work")
	h.sync("apply")
	if work.arts[wslug].title != "Quarterly notes (v2)" {
		t.Fatalf("rename not carried: %q", work.arts[wslug].title)
	}
}

// TestNextPreconditions: off, outside Claude, another support dir,
// read-only, open run, a waiting switch queue, an unknown or other
// replica.
func TestNextPreconditions(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(h *harness)
		state string
		line  i18n.Msg
		code  pderr.Code
	}{
		{"off", func(h *harness) { h.svc.Config.Sync.Artifacts = false }, online.StateNone, i18n.ArtifactsOff, 0},
		{"outside", func(h *harness) { h.env = map[string]string{} }, online.StateNone, i18n.ArtifactsErrOutsideApp, 0},
		{"support dir", func(h *harness) { h.svc.DefaultDir = false }, online.StateNone, i18n.OnlineErrSupportDir, 0},
		{"read-only", func(h *harness) { h.svc.ReadOnly = true }, online.StateNone, i18n.ArtifactsErrReadOnly, 0},
		{"open run", func(h *harness) { h.root.Run = &state.Run{ID: "run-1"} }, "", "", pderr.NeedsRecover},
		{"switch queue", func(h *harness) {
			d := online.Draft{Steps: []online.Step{online.ProbeStep()}}
			_ = d
			h.root.Online = &state.OnlineRecord{Queue: "run-20261126T100000Z-00000001", Profile: "home", Epoch: 1, State: "pending"}
			q := &online.Queue{V: 1, ID: "run-20261126T100000Z-00000001", Phase: online.PhaseReconcile, State: online.QueuePending,
				Profile: "home", Epoch: 1, Steps: []online.Step{online.ProbeStep()}}
			if err := h.ol.Store.WriteQueue(q); err != nil {
				t.Fatal(err)
			}
		}, StateBlocked, i18n.ArtifactsReconcileFirst, 0},
		{"replica unknown", func(h *harness) { h.known = false }, StateBlocked, i18n.ArtifactsReplicaUnknown, 0},
		{"replica changed", func(h *harness) { h.logRep = repWork }, StateBlocked, i18n.ArtifactsReplicaChanged, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.setup(h)
			out, err := h.svc.Next(ctx, NextArgs{Session: testSession})
			if tc.code != 0 {
				if pderr.CodeOf(err) != tc.code {
					t.Fatalf("err %v", err)
				}
				return
			}
			if err != nil || out.State != tc.state || len(out.Lines) != 1 || out.Lines[0] != i18n.T(tc.line) {
				t.Fatalf("%+v %v", out, err)
			}
			if h.root.Online != nil && h.root.Online.Kind == KindArtifacts {
				t.Fatal("a round was published")
			}
		})
	}
}

// TestReplicaChangeStalesChain: an account change between rounds stales
// the chain and cancels its queue; nothing more is recorded.
func TestReplicaChangeStalesChain(t *testing.T) {
	h := newHarness(t)
	h.servers["home"].add("A", page(2))
	ctx := context.Background()
	out, err := h.svc.Next(ctx, NextArgs{Session: testSession})
	if err != nil || out.State != online.StateCalls || out.Token == "" || out.Calls[0].ID != online.ProbeStepID {
		t.Fatalf("first answer %+v %v", out, err)
	}
	h.logRep = repWork
	out, err = h.svc.Next(ctx, NextArgs{Session: testSession, Token: out.Token})
	if err != nil || out.State != StateBlocked {
		t.Fatalf("%+v %v", out, err)
	}
	c, _ := h.svc.Archive.LoadChain()
	if c.State != ChainStale || c.StaleWhy != "replica_changed" {
		t.Fatalf("chain %+v", c)
	}
	q, _ := h.ol.Store.ReadQueue(online.PhaseReconcile)
	if q == nil || q.State != online.QueueCancelled {
		t.Fatalf("queue %+v", q)
	}
	if l, _ := h.ol.Store.ReadLease(); l != nil {
		t.Fatal("lease kept")
	}
}

// TestUnavailableAndRefused: an artifact tool missing stales the chain;
// --refused fails only offered steps without a result.
func TestUnavailableAndRefused(t *testing.T) {
	h := newHarness(t)
	h.servers["home"].add("A", page(2))
	ctx := context.Background()
	out, _ := h.svc.Next(ctx, NextArgs{Session: testSession})
	h.record(out.Calls[0], h.servers["home"].call(out.Calls[0].Tool, out.Calls[0].Input))
	out, err := h.svc.Next(ctx, NextArgs{Session: testSession, Token: out.Token})
	if err != nil || out.State != online.StateCalls || out.Calls[0].Tool != ToolListArtifacts {
		t.Fatalf("%+v %v", out, err)
	}
	// Refusing a step that has a result changes nothing; an offered one
	// without a result fails user_refused.
	if _, err := h.svc.Next(ctx, NextArgs{Session: testSession, Refused: []online.StepID{out.Calls[0].ID, "a999"}}); err != nil {
		t.Fatal(err)
	}
	q, _ := h.ol.Store.ReadQueue(online.PhaseReconcile)
	st := q.Step(out.Calls[0].ID)
	if st.Status != online.StatusFailed || st.Reason != "user_refused" {
		t.Fatalf("step %+v", st)
	}
	out, err = h.svc.Next(ctx, NextArgs{Session: testSession, Unavailable: []string{ToolReadArtifact}})
	if err != nil || out.State != online.StateNone || out.Lines[0] != i18n.T(i18n.ArtifactsErrToolsMissing) {
		t.Fatalf("%+v %v", out, err)
	}
	if c, _ := h.svc.Archive.LoadChain(); c.State != ChainStale || c.StaleWhy != "tools_missing" {
		t.Fatalf("chain %+v", c)
	}
}

// TestPauseAndDone: pause removes armed and the lease and keeps the
// chain; done on an open chain disarms like pause.
func TestPauseAndDone(t *testing.T) {
	h := newHarness(t)
	h.servers["home"].add("A", page(2))
	ctx := context.Background()
	out, _ := h.svc.Next(ctx, NextArgs{Session: testSession})
	if armed, _ := h.ol.Store.ArmedUntil(h.clk.Now()); !armed {
		t.Fatal("not armed")
	}
	p, err := h.svc.Pause(ctx, online.Caller{Session: testSession})
	if err != nil || p.State != StatePaused || p.Lines[0] != i18n.T(i18n.ArtifactsPaused) {
		t.Fatalf("%+v %v", p, err)
	}
	if armed, _ := h.ol.Store.ArmedUntil(h.clk.Now()); armed {
		t.Fatal("still armed")
	}
	c, _ := h.svc.Archive.LoadChain()
	if c.State != ChainPaused {
		t.Fatalf("chain %+v", c)
	}
	// Resume in the same session without the token.
	out2, err := h.svc.Next(ctx, NextArgs{Session: testSession})
	if err != nil || out2.State != online.StateCalls || out2.Chain != out.Chain {
		t.Fatalf("resume %+v %v", out2, err)
	}
	d, err := h.svc.Done(ctx, online.Caller{Session: testSession})
	if err != nil || d.State != StatePaused {
		t.Fatalf("done on an open chain %+v %v", d, err)
	}
	if l, _ := h.ol.Store.ReadLease(); l != nil {
		t.Fatal("done left the lease")
	}
}

// TestRunLockBusy: a busy run lock is exit 11 and nothing is published.
func TestRunLockBusy(t *testing.T) {
	h := newHarness(t)
	h.rounds.busy = true
	_, err := h.svc.Next(context.Background(), NextArgs{Session: testSession})
	if !errors.Is(err, errBusyRun) {
		t.Fatalf("err %v", err)
	}
	if h.root.Online != nil {
		t.Fatal("published")
	}
}

// TestDoneRootBusy: done reports a busy run lock and still completes.
func TestDoneRootBusy(t *testing.T) {
	h := newHarness(t)
	h.servers["home"].add("A", page(2))
	ctx := context.Background()
	h.rounds.busy = false
	// Run the sync up to complete without done.
	token := ""
	for i := 0; i < 50; i++ {
		out, err := h.svc.Next(ctx, NextArgs{Session: testSession, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		if out.Token != "" {
			token = out.Token
		}
		if out.State == StateComplete {
			break
		}
		for _, c := range out.Calls {
			h.record(c, h.servers["home"].call(c.Tool, c.Input))
		}
	}
	h.rounds.busy = true
	d, err := h.svc.Done(ctx, online.Caller{Session: testSession})
	if err != nil || d.State != StateComplete || !strings.Contains(strings.Join(d.Lines, "\n"), i18n.T(i18n.ArtifactsDoneRootBusy)) {
		t.Fatalf("%+v %v", d, err)
	}
	if d.Counts.Saved != 1 {
		t.Fatalf("counts %+v", d.Counts)
	}
}

// TestLockOrder: taking the online lock while holding the artifacts
// lock panics under StrictLocks.
func TestLockOrder(t *testing.T) {
	h := newHarness(t)
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	_ = h.svc.withArchive(context.Background(), LockWait, func() error {
		return h.svc.withOnline(context.Background(), func() error { return nil })
	})
}

// TestQuestionSkip: answering "Not now" publishes nothing; due stays.
func TestQuestionSkip(t *testing.T) {
	h := newHarness(t)
	h.servers["home"].add("A", page(2))
	h.sync("apply")
	h.switchTo("work")
	h.sync("skip")
	if len(h.servers["work"].arts) != 0 {
		t.Fatal("published after skip")
	}
	if n, _ := h.svc.Due("work", repWork); n != 1 {
		t.Fatalf("due %d", n)
	}
	due := h.svc.Archive.LoadDue()
	if due == nil || due.Profile != "work" || due.Count != 1 || due.Epoch != 2 {
		t.Fatalf("due.json %+v", due)
	}
}

// TestGrantDeclined: a refused outbox grant stops publishing for the
// chain; capture still works.
func TestGrantDeclined(t *testing.T) {
	h := newHarness(t)
	h.servers["home"].add("A", page(2))
	h.sync("apply")
	h.switchTo("work")
	h.servers["work"].refuseGrant = true
	h.servers["work"].add("B", page(3))
	h.sync("apply")
	if h.servers["work"].publishes != 0 {
		t.Fatal("published without the grant")
	}
	if len(h.manifest().Pairs) != 2 {
		t.Fatal("capture did not happen")
	}
	c, _ := h.svc.Archive.LoadChain()
	if c.NoPublish != "grant_declined" {
		t.Fatalf("chain %+v", c.NoPublish)
	}
	// Sticky until the next successful publish (9.1): a later chain that
	// publishes nothing does not reset it.
	fresh := *c
	fresh.ID, fresh.NoPublish = "art-20261126T110000Z-00000002", ""
	if err := h.svc.Archive.SaveChain(&fresh); err != nil {
		t.Fatal(err)
	}
	st, err := h.svc.Status(StatusInput{})
	if err != nil || st.Publishing != "grant_declined" || !slices.Contains(st.Lines, i18n.T(i18n.ArtifactsGrantDeclined)) {
		t.Fatalf("status after a new chain: %q %q %v", st.Publishing, st.Lines, err)
	}
	if err := h.svc.Archive.SaveChain(c); err != nil {
		t.Fatal(err)
	}
	h.servers["work"].refuseGrant = false
	h.sync("apply")
	if h.servers["work"].publishes == 0 {
		t.Fatal("nothing published after the grant")
	}
	if st, _ := h.svc.Status(StatusInput{}); st.Publishing != "available" {
		t.Fatalf("still %q after a successful publish", st.Publishing)
	}
}

// TestDoubleWrappingClosesGate: a read-back in an extra wrapper closes
// the gate; no further publish on this app version.
func TestDoubleWrappingClosesGate(t *testing.T) {
	h := newHarness(t)
	h.servers["home"].add("A", page(2))
	h.servers["home"].add("B", page(3))
	h.sync("apply")
	h.switchTo("work")
	h.servers["work"].wrapReadBack = true
	h.sync("apply")
	m := h.manifest()
	if m.Gate.Roundtrip != GateMismatch || m.Gate.Class != ClassWrapped {
		t.Fatalf("gate %+v", m.Gate)
	}
	if h.servers["work"].creates != 1 {
		t.Fatalf("creates %d (only the first may be published)", h.servers["work"].creates)
	}
	st, err := h.svc.Status(StatusInput{})
	if err != nil || st.Publishing != "gate_closed" {
		t.Fatalf("status %+v %v", st.Publishing, err)
	}
	// allow --roundtrip reopens it.
	if _, err := h.svc.Allow(context.Background(), AllowInput{What: "roundtrip"}, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if g := h.manifest().Gate; g.Roundtrip != GateUnverified {
		t.Fatalf("gate %+v", g)
	}
}

// TestRoundJSONShape: a `next` answer is the documented shape.
func TestRoundJSONShape(t *testing.T) {
	h := newHarness(t)
	out, err := h.svc.Next(context.Background(), NextArgs{Session: testSession})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	for _, k := range []string{`"chain":"art-`, `"round":1`, `"phase":"inventory"`, `"state":"calls"`, `"token":"`, `"remaining_batches":`, `"counts":{`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("missing %s in %s", k, b)
		}
	}
}
