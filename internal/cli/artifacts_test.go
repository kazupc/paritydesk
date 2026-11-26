package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/artifacts"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/state"
)

var workReplica = model.Replica{Account: "00000000-0000-4000-8000-000000000003", Org: "00000000-0000-4000-8000-000000000004"}

// artRounds is the run-lock side over the harness root.
type artRounds struct{ h *harness }

func (r artRounds) ArtifactsRound(ctx context.Context, f func(root *state.Root) (*online.Draft, *online.QueueHeader, error)) (string, error) {
	d, hd, err := f(r.h.root)
	if err != nil || d == nil {
		return "", err
	}
	sha, err := r.h.onlineEnv().svc.Publish(ctx, *d, *hd)
	if err != nil {
		return "", err
	}
	r.h.root.Online = &state.OnlineRecord{Queue: hd.ID, Kind: artifacts.KindArtifacts, Profile: hd.Profile, From: hd.From,
		Replica: hd.Replica, Epoch: r.h.root.Epoch, State: "pending", Counts: d.Counts, StepsSHA: sha}
	return sha, nil
}

func (r artRounds) CloseArtifactsQueue(_ context.Context, q string) (bool, error) {
	if r.h.root.Online != nil && r.h.root.Online.Queue == q {
		r.h.root.Online.State = "complete"
	}
	return true, nil
}

// artSpec is the 7.x [artifacts] table with transport files.
func artSpec(t testing.TB) compat.ArtifactsSpec {
	a := manifest7(t).Artifacts
	a.PublishVia = compat.ArtifactsPublishFiles
	return a
}

// artifactsService builds the harness's artifact service (once).
func (h *harness) artifactsService() *artifacts.Service {
	if h.art != nil {
		return h.art
	}
	o := h.onlineEnv()
	o.art = artSpec(h.t)
	o.svc.Providers = onlineRegistry(o.art)
	sd := filepath.Join(h.home, "Library", "Application Support", "paritydesk")
	arch := artifacts.NewArchive(sd, fsxtest.New(h.t), o.art)
	if h.root == nil {
		h.artRoot()
	}
	// The config names both profiles, as `profile add` writes them: the
	// data boundary treats an alias the config does not name as closed.
	cfg := *o.cfg
	cfg.Profiles = []config.Profile{{Alias: "home"}, {Alias: "work"}}
	h.art = &artifacts.Service{Archive: arch, Online: o.svc, OnlineSpec: o.spec, Spec: o.art, Config: &cfg,
		Root: func() (*state.Root, error) { return h.root, nil }, Rounds: artRounds{h},
		Replica: func(context.Context) online.ReplicaObs {
			if h.artReplica == nil {
				return online.ReplicaObs{}
			}
			return online.ReplicaObs{Known: true, Replica: *h.artReplica}
		},
		Getenv: h.sys.getenv, DefaultDir: true, AppVersion: "7.37095.0", Clock: o.svc.Clock, SupportDir: sd, DataRoot: "/data",
		Rand: &countReader{}}
	return h.art
}

// artRoot is a root with home active and work parked.
func (h *harness) artRoot() {
	h.root = state.NewRoot("/data")
	h.root.Active, h.root.Epoch = "home", 4
	h.root.Profiles["home"] = &state.ProfileState{Alias: "home", Replica: olReplica}
	h.root.Profiles["work"] = &state.ProfileState{Alias: "work", Replica: workReplica}
	rep := olReplica
	h.artReplica = &rep
}

// artPair seeds the archive with one pair from work, not on home yet.
func (h *harness) artPair() artifacts.PairID {
	h.t.Helper()
	s := h.artifactsService()
	content := "<!DOCTYPE html>\n<p>fictional</p>"
	sum := shaHex(content)
	if err := s.Archive.PutBlob(sum, []byte(content)); err != nil {
		h.t.Fatal(err)
	}
	m := artifacts.NewManifest()
	slug := artifacts.Slug("00000000-0000-4000-8000-000000000910")
	id := artifacts.PairIDFor(slug)
	m.Profiles["work"] = &artifacts.ProfileRecord{Replica: workReplica, LastSyncAt: fixedTime, LastSyncEpoch: 3}
	m.Pairs[id] = &artifacts.Pair{Title: "Fictional page", TitleSHA: artifacts.TitleKey("Fictional page", ""), CreatedAt: fixedTime,
		Content: &artifacts.Content{SHA256: sum, Bytes: int64(len(content)), Lines: 2, SavedAt: fixedTime, SourceProfile: "work",
			SourceVersion: "1700000000-0000a", ChangedAt: fixedTime},
		Copies: map[ids.Alias]*artifacts.Copy{"work": {Replica: workReplica, Slug: slug, State: artifacts.StateLive,
			Origin: artifacts.OriginRecorded, Read: artifacts.ReadOwner, SyncedVersion: "1700000000-0000a", SyncedSHA: sum,
			URL: "https://claude.ai/artifact/" + string(slug)}},
		Flags: artifacts.Flags{Capabilities: artifacts.CapFlags{State: artifacts.CapNone, Names: []string{}},
			AllowCreate: []ids.Alias{}, Restore: []ids.Alias{}}}
	m.Ignored = map[artifacts.Slug]*artifacts.IgnoredItem{"00000000-0000-4000-8000-000000000911": {Alias: "work", Reason: "multi_file",
		Version: "1700000000-0000b", At: fixedTime}}
	if err := s.Archive.CommitManifest(m, fixedTime); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func shaHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// artNext runs `artifacts next --json` with args.
func (h *harness) artNext(args ...string) artifacts.NextOut {
	h.t.Helper()
	code, out, errOut := h.run("", append([]string{"artifacts", "next", "--json", "--session", olSession}, args...)...)
	var v artifacts.NextOut
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil {
		h.t.Fatalf("next: %d %s %s", code, out, errOut)
	}
	return v
}

// artRecord answers a call through the PostToolUse hook.
func (h *harness) artRecord(c online.Call, text string) {
	h.t.Helper()
	if c.Tool == online.ToolGetSession {
		h.recordProbeFor(c)
		return
	}
	h.recordTool(c, text)
}

// recordProbeFor records the probe of the current queue.
func (h *harness) recordProbeFor(c online.Call) {
	h.t.Helper()
	q, _ := h.onlineEnv().svc.Store.ReadQueue(online.PhaseReconcile)
	r := online.Result{V: 1, Queue: q.ID, Step: online.ProbeStepID, Tool: online.ToolGetSession,
		InputSHA: online.InputSHA(json.RawMessage(`{"session_id":"self"}`)), Session: olSession, At: fixedTime,
		MonoNS: 1_000_000_000_000, BootID: "boot-1", Outcome: online.OutcomeOK, Parsed: json.RawMessage(`{"id":"` + olLocal + `"}`)}
	if err := h.onlineEnv().svc.Store.CreateResult(r); err != nil {
		h.t.Fatal(err)
	}
}

var artifactsCases = []struct {
	name  string
	args  []string
	setup func(h *harness)
	code  int
}{
	{name: "artifacts_status_empty", args: []string{"artifacts", "status"}},
	{name: "artifacts_status", args: []string{"artifacts", "status"}, setup: func(h *harness) { h.artPair() }},
	{name: "artifacts_plan", args: []string{"artifacts", "plan", "--profile", "home"}, setup: func(h *harness) { h.artPair() }},
	{name: "artifacts_next_inventory", args: []string{"artifacts", "next", "--session", olSession}, setup: func(h *harness) {
		h.env["CLAUDECODE"] = "1"
		h.artPair()
	}},
	{name: "artifacts_next_question", args: []string{"artifacts", "next", "--session", olSession}, setup: func(h *harness) {
		h.env["CLAUDECODE"] = "1"
		h.artPair()
		for i := 0; i < 10; i++ {
			v := h.artNext()
			if v.State != online.StateCalls {
				break
			}
			for _, c := range v.Calls {
				h.artRecord(c, `{"artifacts":[],"next_cursor":null}`)
			}
		}
	}},
	{name: "artifacts_next_blocked", args: []string{"artifacts", "next", "--session", olSession}, setup: func(h *harness) {
		h.env["CLAUDECODE"] = "1"
		h.artifactsService()
		h.publish(sidebarDraft(h.t, manifest7(h.t)))
	}},
	{name: "artifacts_next_replica_unknown", args: []string{"artifacts", "next", "--session", olSession}, setup: func(h *harness) {
		h.env["CLAUDECODE"] = "1"
		h.artifactsService()
		h.artReplica = nil
	}},
	{name: "artifacts_pause", args: []string{"artifacts", "pause", "--session", olSession}, setup: func(h *harness) {
		h.env["CLAUDECODE"] = "1"
		h.artPair()
		h.artNext()
	}},
	{name: "artifacts_done", args: []string{"artifacts", "done", "--session", olSession}, setup: func(h *harness) {
		h.env["CLAUDECODE"] = "1"
		h.artPair()
		h.root.Profiles["work"].Replica = workReplica
		for i := 0; i < 20; i++ {
			v := h.artNext()
			if v.State == online.StateQuestion {
				h.run("", "artifacts", "decide", "--session", olSession, "--question", "artifacts_publish", "--answer", "skip")
				continue
			}
			if v.State != online.StateCalls {
				break
			}
			for _, c := range v.Calls {
				h.artRecord(c, `{"artifacts":[],"next_cursor":null}`)
			}
		}
	}},
	{name: "artifacts_import_dry", args: []string{"artifacts", "import-legacy", "--from", "LEGACY"}, setup: func(h *harness) {
		h.artifactsService()
	}},
	{name: "artifacts_import_applied", args: []string{"artifacts", "import-legacy", "--from", "LEGACY", "--apply", "--yes"}, setup: func(h *harness) {
		h.artifactsService()
	}},
	{name: "plan_artifacts", args: []string{"plan", "home"}, setup: func(h *harness) {
		r := h.svc.runResult(fakePlan())
		r.Artifacts = &orchestrator.ArtifactsPreview{Unsynced: "work", Due: 2, To: "home"}
		h.svc.result = r
	}},
	{name: "online_done_artifacts_next", args: []string{"online", "done", "--session", olSession}, setup: func(h *harness) {
		h.publish(sidebarDraft(h.t, manifest7(h.t)))
		h.svc.confirm2 = &orchestrator.OnlineConfirmResult{State: "complete", Queue: olQueue, Profile: "home",
			Counts: online.Counts{Sidebar: 1, Done: 1}}
		sd := filepath.Join(h.home, "Library", "Application Support", "paritydesk")
		a := artifacts.NewArchive(sd, fsxtest.New(h.t), compat.ArtifactsSpec{})
		if err := a.SaveDue(artifacts.DueFile{Seq: 2, Profile: "home", Epoch: 4, Count: 1}); err != nil {
			h.t.Fatal(err)
		}
	}},
}

func TestArtifactsJSONGolden(t *testing.T) {
	legacy, err := filepath.Abs(filepath.Join("..", "artifacts", "testdata", "legacy_v2"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range artifactsCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if tc.setup != nil {
				tc.setup(h)
			}
			args := append([]string{"--json"}, tc.args...)
			for i, a := range args {
				if a == "LEGACY" {
					args[i] = legacy
				}
			}
			code, out, errOut := h.run("", args...)
			if code != tc.code {
				t.Fatalf("code %d, want %d; stdout %s stderr %s", code, tc.code, out, errOut)
			}
			if !strings.HasPrefix(out, `{"v":1`) || strings.Count(out, "\n") != 1 {
				t.Fatalf("not one JSON document with v first: %q", out)
			}
			golden(t, "json/"+tc.name, h.normalize(out))
		})
	}
}

// TestArtifactsTerminalOnly: decision leaves refuse inside Claude even
// with --yes, before anything changes (9.3).
func TestArtifactsTerminalOnly(t *testing.T) {
	for _, args := range [][]string{
		{"artifacts", "allow", "0123456789abcdef", "--capabilities", "--yes"},
		{"artifacts", "allow", "0123456789abcdef", "--public", "--yes"},
		{"artifacts", "allow", "--roundtrip", "--yes"},
		{"artifacts", "forget", "0123456789abcdef", "--yes"},
		{"artifacts", "import-legacy", "--from", "/x", "--apply", "--yes"},
	} {
		h := newHarness(t)
		h.env["CLAUDECODE"] = "1"
		id := h.artPair()
		before, _ := os.ReadFile(filepath.Join(h.art.Archive.Root, "manifest.json"))
		args = append([]string(nil), args...)
		for i, a := range args {
			if a == "0123456789abcdef" {
				args[i] = string(id)
			}
		}
		code, _, errOut := h.run("", args...)
		if code != 2 || !strings.Contains(errOut, i18n.T(i18n.ArtifactsErrTerminalOnly)) {
			t.Fatalf("%v: %d %s", args, code, errOut)
		}
		after, _ := os.ReadFile(filepath.Join(h.art.Archive.Root, "manifest.json"))
		if string(before) != string(after) {
			t.Fatalf("%v changed the manifest", args)
		}
	}
	// Outside Claude without --yes and without a terminal: exit 3.
	h := newHarness(t)
	id := h.artPair()
	if code, _, errOut := h.run("", "artifacts", "allow", string(id), "--shared"); code != 3 || !strings.Contains(errOut, i18n.T(i18n.ArtifactsErrNeedsYes)) {
		t.Fatalf("needs yes: %d %s", code, errOut)
	}
	if code, out, _ := h.run("", "artifacts", "allow", string(id), "--shared", "--yes", "--json"); code != 0 || !strings.Contains(out, `"state":"allowed"`) {
		t.Fatalf("allow: %d %s", code, out)
	}
	// The hidden leaves answer none outside Claude.
	if code, out, _ := h.run("", "artifacts", "next", "--json"); code != 0 || !strings.Contains(out, i18n.T(i18n.ArtifactsErrOutsideApp)) {
		t.Fatalf("next outside: %d %s", code, out)
	}
}

func TestArtifactsUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"artifacts", "allow", "x", "--shared", "--public"},
		{"artifacts", "allow", "x"},
		{"artifacts", "allow", "x", "--roundtrip"},
		{"artifacts", "import-legacy"},
		{"artifacts", "import-legacy", "--from", "/x", "--map", "nope"},
		{"artifacts", "next", "--refused", "r000"},
		{"artifacts", "next", "--unavailable", "rm -rf"},
		{"artifacts", "next", "--session", "x"},
		{"artifacts", "decide", "--question", "q", "--answer", "Publish now"},
		{"artifacts", "skip"},
		{"switch", "--only", "artifacts"},
	} {
		h := newHarness(t)
		if code, _, _ := h.run("", args...); code != 2 {
			t.Errorf("%v: code %d", args, code)
		}
	}
	h := newHarness(t)
	_, _, errOut := h.run("", "switch", "--only", "artifacts")
	if !strings.Contains(errOut, "artifacts are synced by /paritydesk:artifacts, not by switch") {
		t.Fatalf("message %q", errOut)
	}
}

func TestArtifactsReadOnly(t *testing.T) {
	for _, args := range [][]string{
		{"artifacts", "skip", "x"},
		{"artifacts", "ingest"},
		{"artifacts", "allow", "x", "--shared", "--yes"},
	} {
		h := newHarness(t)
		if code, _, _ := h.run("", append(args, "--read-only")...); code != 2 {
			t.Errorf("%v: code %d", args, code)
		}
	}
	h := newHarness(t)
	h.env["CLAUDECODE"] = "1"
	if code, out, _ := h.run("", "artifacts", "next", "--json", "--read-only"); code != 0 || !strings.Contains(out, i18n.T(i18n.ArtifactsErrReadOnly)) {
		t.Fatalf("next read-only: %d %s", code, out)
	}
}

// TestArtifactsSyncThroughHooks runs a whole sync through the CLI and
// the PostToolUse hook: the page saved from list and read results.
func TestArtifactsSyncThroughHooks(t *testing.T) {
	h := newHarness(t)
	h.env["CLAUDECODE"] = "1"
	h.artifactsService()
	slug := "00000000-0000-4000-8000-000000000920"
	tag := "untrusted-document-0123abcd"
	list := `{"artifacts":[{"slug":"` + slug + `","rel":"mine","version":"1700000000-0000a","title":"Fictional"}],"next_cursor":null}`
	get := `{"slug":"` + slug + `","version":"1700000000-0000a","read":"owner","title":"Fictional"}`
	read := `{"slug":"` + slug + `","version":"1700000000-0000a","total_lines":2,"offset":1,"next_offset":null,"notice":"in <` + tag + `>"}` +
		"\n<" + tag + ">a\nb</" + tag + ">"
	answers := map[string]string{artifacts.ToolListArtifacts: list, artifacts.ToolGetArtifact: get,
		artifacts.ToolListFiles: `{"files":[{"path":"index.html"}]}`, artifacts.ToolReadDeclaration: `{"capabilities":[]}`,
		artifacts.ToolReadArtifact: read}
	state := ""
	for i := 0; i < 30; i++ {
		v := h.artNext()
		state = v.State
		if v.State != online.StateCalls {
			break
		}
		for _, c := range v.Calls {
			h.artRecord(c, answers[c.Tool])
		}
	}
	if state != artifacts.StateComplete {
		t.Fatalf("state %s", state)
	}
	m, _ := h.art.Archive.LoadManifest()
	if m == nil || len(m.Pairs) != 1 {
		t.Fatalf("manifest %+v", m)
	}
	code, out, _ := h.run("", "artifacts", "done", "--json", "--session", olSession)
	if code != 0 || !strings.Contains(out, `"saved":1`) {
		t.Fatalf("done: %d %s", code, out)
	}
}

// TestUninstallKeepsArtifacts: the archive is kept and listed by default;
// --keep-artifacts=false removes it (m8).
func TestUninstallKeepsArtifacts(t *testing.T) {
	h := newHarness(t)
	writeFile(h, "Library/Application Support/paritydesk/artifacts/manifest.json", "{}")
	arch := filepath.Join(h.home, "Library", "Application Support", "paritydesk", "artifacts")
	code, out, errOut := h.run("", "uninstall", "--yes", "--json")
	if code != 0 || !strings.Contains(out, `"kept":[`) || !strings.Contains(h.normalize(out), "paritydesk/artifacts") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if _, err := os.Stat(arch); err != nil {
		t.Fatal("archive removed by default")
	}
	if code, _, errOut := h.run("", "uninstall", "--yes", "--keep-artifacts=false"); code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	if _, err := os.Stat(arch); err == nil {
		t.Fatal("archive kept with --keep-artifacts=false")
	}
}

// TestUninstallRefusesStateDirNamedLikeOwnFolders: a --state-dir that is a
// user's own folder named "artifacts" or "online" is not removed; only
// paritydesk's folders at their exact place are removed by name.
func TestUninstallRefusesStateDirNamedLikeOwnFolders(t *testing.T) {
	for _, name := range []string{"artifacts", "online"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			writeFile(h, "Documents/"+name+"/my-thesis.txt", "keep me")
			foreign := filepath.Join(h.home, "Documents", name)
			if code, _, _ := h.run("", "uninstall", "--yes", "--state-dir", foreign); code == 0 {
				t.Fatal("uninstall removed a folder it does not own")
			}
			if _, err := os.Stat(filepath.Join(foreign, "my-thesis.txt")); err != nil {
				t.Fatal("user file removed")
			}
		})
	}
}
