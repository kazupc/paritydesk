package artifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/state"
)

// Fake ids only (the fake pattern of the repository).
const (
	acctHome    = "00000000-0000-4000-8000-000000000001"
	orgHome     = "00000000-0000-4000-8000-000000000002"
	acctWork    = "00000000-0000-4000-8000-000000000003"
	orgWork     = "00000000-0000-4000-8000-000000000004"
	testSession = "00000000-0000-4000-8000-0000000000c1"
	testLocal   = "local_00000000-0000-4000-8000-0000000000d1"
	testTag     = "untrusted-document-0123abcd"
	appVer      = "7.37095.0"
)

var (
	repHome  = model.Replica{Account: acctHome, Org: orgHome}
	repWork  = model.Replica{Account: acctWork, Org: orgWork}
	baseTime = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
)

// testSpec is the embedded 7.x [artifacts] table with transport files.
func testSpec(t testing.TB) compat.ArtifactsSpec {
	t.Helper()
	m := manifest7(t)
	a := m.Artifacts
	a.PublishVia = compat.ArtifactsPublishFiles
	return a
}

var (
	manifestOnce sync.Once
	manifestM    *compat.Manifest
)

func manifest7(t testing.TB) *compat.Manifest {
	t.Helper()
	manifestOnce.Do(func() {
		ms, err := compat.Load("")
		if err != nil {
			panic(err)
		}
		for _, m := range ms {
			if m.Name == "7.x" {
				manifestM = m
			}
		}
	})
	if manifestM == nil {
		t.Fatal("no 7.x manifest")
	}
	return manifestM
}

// ---------------------------------------------------------------- fake server

// fakeArt is one artifact on a fake account.
type fakeArt struct {
	slug     Slug
	title    string
	content  string
	version  int
	updated  time.Time
	deleted  bool
	files    int // list_files entries (1 = index.html only)
	caps     []string
	others   bool
	read     string
	typeMade bool
}

// fakeServer answers the claude-artifacts tools for one account.
type fakeServer struct {
	arts     map[Slug]*fakeArt
	pageSize int
	nextID   int
	clock    *clock.Fake
	// wrapReadBack wraps read-backs in an extra tag (double wrapping).
	wrapReadBack bool
	// refuseGrant answers request_directory with an error.
	refuseGrant bool
	publishes   int
	creates     int
}

func newServer(clk *clock.Fake) *fakeServer {
	return &fakeServer{arts: map[Slug]*fakeArt{}, pageSize: 2, nextID: 0x100, clock: clk}
}

func (s *fakeServer) newSlug() Slug {
	s.nextID++
	return Slug(fmt.Sprintf("00000000-0000-4000-8000-%012x", s.nextID))
}

func (s *fakeServer) ver(a *fakeArt) string { return fmt.Sprintf("1700000000-%05d", a.version) }

func (s *fakeServer) add(title, content string) *fakeArt {
	a := &fakeArt{slug: s.newSlug(), title: title, content: content, version: 1, updated: s.clock.Now(), files: 1, read: ReadOwner}
	s.arts[a.slug] = a
	return a
}

func (s *fakeServer) edit(a *fakeArt, content string) {
	a.content, a.updated = content, s.clock.Now()
	a.version++
}

func (s *fakeServer) sorted() []*fakeArt {
	var out []*fakeArt
	for _, a := range s.arts {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].slug < out[j].slug })
	return out
}

func textResp(s string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": s}}})
	return b
}

func blocksResp(blocks ...string) json.RawMessage {
	var bs []map[string]string
	for _, b := range blocks {
		bs = append(bs, map[string]string{"type": "text", "text": b})
	}
	b, _ := json.Marshal(map[string]any{"content": bs})
	return b
}

func errResp(s string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": s}}, "isError": true})
	return b
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// call answers one tool call.
func (s *fakeServer) call(tool string, input json.RawMessage) json.RawMessage {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	slug := Slug(fmt.Sprint(in["slug"]))
	switch tool {
	case online.ToolGetSession:
		return textResp(jsonText(map[string]any{"id": testLocal}))
	case ToolListArtifacts:
		all := s.sorted()
		start := 0
		if c, ok := in["cursor"].(string); ok {
			fmt.Sscanf(c, "c%d", &start)
		}
		end := min(start+s.pageSize, len(all))
		var items []map[string]any
		for _, a := range all[start:end] {
			items = append(items, map[string]any{"slug": string(a.slug), "rel": "mine", "title": a.title,
				"url": "https://claude.ai/artifact/" + string(a.slug), "version": s.ver(a),
				"updated_at": a.updated.Format(time.RFC3339), "soft_deleted": a.deleted})
		}
		page := map[string]any{"artifacts": items, "next_cursor": nil}
		if end < len(all) {
			page["next_cursor"] = fmt.Sprintf("c%d", end)
		}
		if items == nil {
			page["artifacts"] = []any{}
		}
		return textResp(jsonText(page))
	case ToolGetArtifact:
		a := s.arts[slug]
		if a == nil {
			return errResp("Artifact not found")
		}
		if a.typeMade {
			return errResp("This artifact was made from a type")
		}
		text := jsonText(map[string]any{"slug": string(a.slug), "version": s.ver(a), "title": a.title, "icon": "notes", "read": a.read,
			"url": "https://claude.ai/artifact/" + string(a.slug)})
		if a.others {
			return blocksResp("<artifact-content-authored-by-others/>", text)
		}
		return textResp(text)
	case ToolListFiles:
		a := s.arts[slug]
		var files []map[string]any
		files = append(files, map[string]any{"path": "index.html"})
		for i := 1; i < a.files; i++ {
			files = append(files, map[string]any{"path": fmt.Sprintf("asset%d.css", i), "size": 10})
		}
		return textResp(jsonText(map[string]any{"files": files}))
	case ToolReadDeclaration:
		a := s.arts[slug]
		caps := []string{}
		caps = append(caps, a.caps...)
		return textResp(jsonText(map[string]any{"capabilities": caps}))
	case ToolReadArtifact:
		a := s.arts[slug]
		lines := strings.Split(a.content, "\n")
		limit := len(lines)
		if l, ok := in["limit"].(float64); ok {
			limit = int(l)
		}
		off := 1
		if o, ok := in["offset"].(float64); ok {
			off = int(o)
		}
		end := min(off-1+limit, len(lines))
		body := strings.Join(lines[off-1:end], "\n")
		sum := map[string]any{"slug": string(a.slug), "version": s.ver(a), "title": "<" + testTag + ">" + a.title + "</" + testTag + ">",
			"total_lines": len(lines), "offset": off, "next_offset": nil, "owned_by_user": true,
			"notice": "The HTML is wrapped in an <" + testTag + "> tag that is not part of it."}
		if end < len(lines) {
			sum["next_offset"] = end + 1
		}
		html := "<" + testTag + ">" + body + "</" + testTag + ">"
		if s.wrapReadBack {
			html = "<" + testTag + "><untrusted-document-ffffffff>" + body + "</untrusted-document-ffffffff></" + testTag + ">"
		}
		return blocksResp(jsonText(sum), html)
	case ToolRequestDir:
		if s.refuseGrant {
			return errResp("The user declined")
		}
		return textResp("Access granted.")
	case ToolPublishFiles:
		files, _ := in["files"].([]any)
		f0, _ := files[0].(map[string]any)
		b, err := os.ReadFile(fmt.Sprint(f0["path"]))
		if err != nil {
			return errResp("The client refused the file")
		}
		s.publishes++
		if sl, ok := in["slug"].(string); ok {
			a := s.arts[Slug(sl)]
			if a == nil {
				return errResp("Artifact not found")
			}
			if in["base_version"] != s.ver(a) {
				return errResp("The base version is not the current version")
			}
			s.edit(a, string(b))
			if t, ok := in["title"].(string); ok {
				a.title = t
			}
			return textResp(jsonText(map[string]any{"slug": string(a.slug), "version": s.ver(a), "url": "https://claude.ai/artifact/" + string(a.slug)}))
		}
		s.creates++
		a := s.add(fmt.Sprint(in["title"]), string(b))
		return textResp(jsonText(map[string]any{"slug": string(a.slug), "version": s.ver(a), "url": "https://claude.ai/artifact/" + string(a.slug)}))
	}
	return errResp("unknown tool")
}

// ---------------------------------------------------------------- harness

// fakeRounds is the run-lock side over an in-memory root.
type fakeRounds struct {
	h    *harness
	busy bool
}

func (r *fakeRounds) ArtifactsRound(ctx context.Context, f func(root *state.Root) (*online.Draft, *online.QueueHeader, error)) (string, error) {
	if r.busy {
		return "", errBusyRun
	}
	d, hd, err := f(r.h.root)
	if err != nil || d == nil {
		return "", err
	}
	sha, err := r.h.ol.Publish(ctx, *d, *hd)
	if err != nil {
		return "", err
	}
	r.h.root.Online = &state.OnlineRecord{Queue: hd.ID, Kind: KindArtifacts, Profile: hd.Profile, From: hd.From, Replica: hd.Replica,
		Epoch: r.h.root.Epoch, State: string(online.QueuePending), Counts: d.Counts, StepsSHA: sha}
	return sha, nil
}

func (r *fakeRounds) CloseArtifactsQueue(_ context.Context, q string) (bool, error) {
	if r.busy {
		return false, nil
	}
	if rec := r.h.root.Online; rec != nil && rec.Queue == q {
		rec.State = string(online.QueueComplete)
	}
	return true, nil
}

var errBusyRun = fmt.Errorf("run lock busy")

// harness is one Mac: the archive, the online folder, root and two
// fake accounts.
type harness struct {
	t       *testing.T
	dir     string
	clk     *clock.Fake
	mono    *online.FakeMono
	ol      *online.Service
	svc     *Service
	root    *state.Root
	rounds  *fakeRounds
	servers map[ids.Alias]*fakeServer
	known   bool
	logRep  model.Replica
	env     map[string]string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	fs := fsxtest.New(t)
	dir := filepath.Join(t.TempDir(), "support")
	clk := clock.NewFake(baseTime)
	mono := online.NewFakeMono("boot-1", 1_000_000_000_000)
	cfg := config.Default()
	cfg.Profiles = []config.Profile{{Alias: "home"}, {Alias: "work"}}
	m := manifest7(t)
	store := online.NewStore(dir, fs)
	spec := testSpec(t)
	ol := &online.Service{Store: store, Config: cfg.Online, Clock: clk, Mono: mono,
		Providers: append(online.Registry(), Provider{Spec: spec, Guard: true}), Settings: online.NewSettings(m.Online, cfg.Online)}
	h := &harness{t: t, dir: dir, clk: clk, mono: mono, ol: ol, known: true, logRep: repHome,
		env:     map[string]string{"CLAUDECODE": "1"},
		servers: map[ids.Alias]*fakeServer{"home": newServer(clk), "work": newServer(clk)}}
	h.servers["work"].nextID = 0x900
	h.root = state.NewRoot("/data")
	h.root.Active, h.root.Epoch = "home", 1
	h.root.Profiles["home"] = &state.ProfileState{Alias: "home", Replica: repHome}
	h.root.Profiles["work"] = &state.ProfileState{Alias: "work", Replica: repWork}
	h.rounds = &fakeRounds{h: h}
	arch := NewArchive(dir, fs, spec)
	h.svc = &Service{Archive: arch, Online: ol, OnlineSpec: m.Online, Spec: spec, Config: cfg,
		Root: func() (*state.Root, error) { return h.root, nil }, Rounds: h.rounds,
		Replica:    func(context.Context) online.ReplicaObs { return online.ReplicaObs{Known: h.known, Replica: h.logRep} },
		Getenv:     func(k string) string { return h.env[k] },
		DefaultDir: true, AppVersion: appVer, Clock: clk, SupportDir: dir, DataRoot: "/data", StrictLocks: true}
	return h
}

// switchTo makes alias active (a switch: epoch + 1, the switch queue
// superseded).
func (h *harness) switchTo(a ids.Alias) {
	h.root.Active = a
	h.root.Epoch++
	h.root.Online = nil
	_ = h.ol.Invalidate(context.Background(), "switch")
	h.logRep = h.root.Profiles[a].Replica
}

// record runs the PostToolUse recording of a call: the step whose tool
// and canonical input match (after the provider's normalisation) gets
// the parsed result.
func (h *harness) record(c online.Call, resp json.RawMessage) {
	h.t.Helper()
	q, err := h.ol.Store.ReadQueue(online.PhaseReconcile)
	if err != nil || q == nil {
		h.t.Fatalf("no queue: %v", err)
	}
	in, _ := model.Canonical(c.Input)
	for _, st := range q.Steps {
		if st.Tool != c.Tool {
			continue
		}
		if st.InputSHA256 == "" && string(st.Input) != string(in) {
			continue
		}
		if st.InputSHA256 != "" && online.InputSHA(in) != st.InputSHA256 {
			continue
		}
		r := online.Result{V: 1, Queue: q.ID, Step: st.ID, Tool: st.Tool, InputSHA: online.InputSHA(in), Session: testSession,
			At: h.clk.Now(), MonoNS: online.Read(h.mono).NS, BootID: "boot-1"}
		if st.Kind == Kind {
			r.Outcome, r.Parsed = Provider{}.ParseResult(st, resp)
		} else {
			r.Outcome, r.Parsed = online.OutcomeOK, json.RawMessage(`{"id":"`+testLocal+`"}`)
		}
		if err := h.ol.Store.CreateResult(r); err != nil {
			h.t.Fatal(err)
		}
		return
	}
	h.t.Fatalf("no step for %s %s", c.Tool, c.Input)
}

// sync runs the skill's loop on the active profile: next, calls,
// questions (answer), and done. It returns the last next answer.
func (h *harness) sync(answer string) NextOut {
	h.t.Helper()
	ctx := context.Background()
	srv := h.servers[h.root.Active]
	token := ""
	var out NextOut
	for i := 0; i < 400; i++ {
		var err error
		out, err = h.svc.Next(ctx, NextArgs{Session: testSession, Token: token})
		if err != nil {
			h.t.Fatalf("next: %v", err)
		}
		if out.Token != "" {
			token = out.Token
		}
		arts := 0
		for _, c := range out.Calls {
			if c.Tool != online.ToolGetSession {
				arts++
			}
		}
		if arts > h.svc.Spec.BatchMax {
			h.t.Fatalf("answer holds %d artifact calls", arts)
		}
		switch out.State {
		case online.StateCalls:
			for _, c := range out.Calls {
				h.record(c, srv.call(c.Tool, c.Input))
			}
		case online.StateQuestion:
			if _, err := h.svc.Decide(ctx, online.Caller{Session: testSession, Token: token}, out.Question.ID, answer); err != nil {
				h.t.Fatalf("decide: %v", err)
			}
		case online.StateWait:
			h.clk.Advance(time.Minute)
			h.mono.Advance(int64(time.Minute))
		case StateComplete:
			if _, err := h.svc.Done(ctx, online.Caller{Session: testSession}); err != nil {
				h.t.Fatalf("done: %v", err)
			}
			return out
		default:
			return out
		}
	}
	h.t.Fatal("sync did not finish")
	return out
}

// manifest reads the archive's manifest.
func (h *harness) manifest() *Manifest {
	h.t.Helper()
	m, err := h.svc.Archive.LoadManifest()
	if err != nil || m == nil {
		h.t.Fatalf("manifest: %v", err)
	}
	return m
}
