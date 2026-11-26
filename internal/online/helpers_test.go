package online

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/state"
)

// Fixtures use the fake id pattern only.
const (
	tQueue   = "run-0001"
	tSession = "00000000-0000-4000-8000-0000000000c1"
	tOther   = "00000000-0000-4000-8000-0000000000c2"
	tLocal   = "local_00000000-0000-4000-8000-0000000000d1"
	tBoot    = "boot-1"
)

var (
	t0       = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tReplica = model.Replica{Account: "00000000-0000-4000-8000-000000000001", Org: "00000000-0000-4000-8000-000000000002"}
)

// spec7 returns the embedded 7.x manifest's [online] table.
func spec7(t testing.TB) compat.OnlineSpec {
	t.Helper()
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "7.x" {
			return m.Online
		}
	}
	t.Fatal("no 7.x manifest")
	return compat.OnlineSpec{}
}

// atomicOps serializes RenameExcl, as the platform's RENAME_EXCL is
// atomic while fsxtest checks and renames in two steps.
type atomicOps struct {
	fsx.Ops
	mu *sync.Mutex
}

func (a atomicOps) RenameExcl(from, to string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Ops.RenameExcl(from, to)
}

// testStore returns a store over a temporary support directory.
func testStore(t testing.TB) *Store {
	t.Helper()
	sd := filepath.Join(t.TempDir(), "paritydesk")
	s := NewStore(sd, atomicOps{Ops: fsxtest.New(t), mu: &sync.Mutex{}})
	var mu sync.Mutex
	s.Lock = func(context.Context, time.Duration) (func(), error) { mu.Lock(); return mu.Unlock, nil }
	return s
}

// countReader is a deterministic byte source.
type countReader struct {
	mu sync.Mutex
	n  byte
}

func (r *countReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range p {
		r.n++
		p[i] = r.n
	}
	return len(p), nil
}

// testService returns a service over a test store with fake clocks.
func testService(t testing.TB, ps ...Provider) *Service {
	t.Helper()
	if len(ps) == 0 {
		ps = Registry()
	}
	cfg := config.Default()
	st := testStore(t)
	rnd := &countReader{}
	st.Rand = rnd
	return &Service{Store: st, Config: cfg.Online, Clock: clock.NewFake(t0), Mono: NewFakeMono(tBoot, 1e12),
		Providers: ps, Settings: NewSettings(spec7(t), cfg.Online), Rand: rnd}
}

// sectionsSnap builds a sidebar-view snapshot from section objects.
func sectionsSnap(t testing.TB, objs ...string) *model.RowSnapshot {
	t.Helper()
	s := &model.RowSnapshot{Row: model.RowSidebarView, Schema: 1, Items: map[string]model.Item{}}
	for _, o := range objs {
		var head struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(o), &head); err != nil {
			t.Fatal(err)
		}
		it, err := model.NewItem("section:"+head.ID, json.RawMessage(o), nil, model.Stamp{}, false)
		if err != nil {
			t.Fatal(err)
		}
		s.Items[it.Key] = it
	}
	return s
}

// sidebarInput is a BuildInput with the sidebar-view row only.
func sidebarInput(t testing.TB, desired, current *model.RowSnapshot) BuildInput {
	t.Helper()
	in := BuildInput{Spec: spec7(t), Config: config.Default().Online, From: "work", To: "home", Replica: tReplica,
		Outputs: map[model.RowID]*rows.Output{model.RowSidebarView: {Desired: desired}}, Phase: PhaseReconcile}
	if current != nil {
		in.Current = map[model.RowID]*model.RowSnapshot{model.RowSidebarView: current}
	}
	return in
}

// publish publishes d and returns the matching root record.
func publish(t testing.TB, s *Service, d Draft) *state.OnlineRecord {
	t.Helper()
	sha, err := s.Publish(context.Background(), d, QueueHeader{ID: tQueue, Profile: "home", From: "work", Replica: tReplica,
		Epoch: 4, Spec: spec7(t)})
	if err != nil {
		t.Fatal(err)
	}
	return &state.OnlineRecord{Queue: tQueue, Profile: "home", From: "work", Replica: tReplica, Epoch: 4, Created: t0,
		State: string(QueuePending), Counts: d.Counts, StepsSHA: sha}
}

// rootInfo is the root summary for a record.
func rootInfo(rec *state.OnlineRecord) RootInfo {
	return RootInfo{Record: rec, Epoch: 4, Active: "home"}
}

// result builds an ok result of a step for session.
func result(q string, s Step, session, parsed string) Result {
	return Result{V: 1, Queue: q, Step: s.ID, Tool: s.Tool, InputSHA: stepInputSHA(s), Session: session, At: t0,
		MonoNS: 1e12, BootID: tBoot, Outcome: OutcomeOK, Parsed: json.RawMessage(parsed)}
}

// textResp is a content-array tool response with one text block.
func textResp(text string, isErr bool) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": isErr})
	return b
}
