package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

func TestSyncRefusedInsideApp(t *testing.T) {
	e := newEnv(t)
	e.det.inside = true
	_, err := e.orch().Sync(context.Background(), SyncOptions{Yes: true})
	if codeOf(t, err) != pderr.Usage || e.st.lockCalls != 0 || e.app.quits != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestSyncReapplies(t *testing.T) {
	e := newEnv(t)
	afterSwitch(t, e)
	e.markBases()
	e.invalid["b"] = true
	e.writeRow(ids.DefaultFolder, model.RowSessions, map[string]any{string(fakeSession(2)): map[string]any{"title": "two"}})
	e.ui.interactive = true
	res, err := e.orch().Sync(context.Background(), SyncOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.keys(ids.DefaultFolder, model.RowSessions)) != 2 || res.From != "b" || res.To != "b" {
		t.Errorf("sync did not re-apply: %v", e.keys(ids.DefaultFolder, model.RowSessions))
	}
	if slices.Contains(e.st.log, "begin:swapping") {
		t.Error("sync swapped")
	}
	e.checkInvariants("sync")
}

// seedGroupOrderBase stores a group-order base for a.
func seedGroupOrderBase(t *testing.T, e *env, value string) state.BaseKey {
	t.Helper()
	snap, err := snapshotOf(model.RowGroupOrder, []byte(`{"order":`+value+`}`), true)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := e.st.PutSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	r := e.st.root(t)
	k := state.BaseKey{Alias: "a", Replica: repA, Row: model.RowGroupOrder}
	r.Bases[k.String()] = ref
	if err := e.st.Commit(r); err != nil {
		t.Fatal(err)
	}
	return k
}

func writeMirror(t *testing.T, e *env, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.root, ids.DefaultFolder, "mirror.json"), []byte(`{"order":`+value+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyDriftSetsContested(t *testing.T) {
	e := newEnv(t)
	k := seedGroupOrderBase(t, e, `["x","y"]`)
	writeMirror(t, e, `["y","x"]`)
	res, err := e.orch().Verify(context.Background())
	if codeOf(t, err) != pderr.VerifyDrift {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(res.Drift, []model.RowID{model.RowGroupOrder}) {
		t.Fatalf("drift = %v", res.Drift)
	}
	root := e.st.root(t)
	c, ok := root.Contested[state.ContestedKey(k, "order")]
	server, _ := model.ItemHash(json.RawMessage(`["y","x"]`))
	intended, _ := model.ItemHash(json.RawMessage(`["x","y"]`))
	if !ok || c.ServerHash != server || c.IntendedHash != intended || c.Row != model.RowGroupOrder || c.Replica != repA {
		t.Fatalf("contested = %+v", c)
	}
	base, err := e.st.GetSnapshot(root.Bases[k.String()])
	if err != nil || base.Items["order"].Hash != server {
		t.Errorf("base not set to the observed value")
	}
	if root.LastVerify == nil || !slices.Equal(root.LastVerify.Drift, res.Drift) {
		t.Error("verify record missing")
	}
	// No drift once the base matches what the app shows.
	if _, err := e.orch().Verify(context.Background()); err != nil {
		t.Errorf("second verify: %v", err)
	}
}

func TestVerifySignInNamesAlias(t *testing.T) {
	e := newEnv(t)
	seedGroupOrderBase(t, e, `["x"]`)
	writeMirror(t, e, `["x"]`)
	e.app.events = []app.LogEvent{app.EvLoginRequired, app.EvBridgeConnected, app.EvFocusedSession}
	res, err := e.orch().Verify(context.Background())
	if codeOf(t, err) != pderr.NeedsSignIn || !strings.Contains(err.Error(), "with a's account only") {
		t.Fatalf("err = %v", err)
	}
	if !res.SignInNeeded || !res.BridgeSeen || !res.SessionFocused || !res.PairingOK {
		t.Errorf("result = %+v", res)
	}
}

func TestVerifyIdentityMismatch(t *testing.T) {
	e := newEnv(t)
	e.makeFolder(ids.DefaultFolder, repB, nil)
	if _, err := e.orch().Verify(context.Background()); codeOf(t, err) != pderr.IdentityMismatch {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusShortNeverReadsKV(t *testing.T) {
	e := newEnv(t)
	r := e.st.root(t)
	r.PendingAdd = &state.PendingAdd{Alias: "c", ParkedFrom: "a"}
	_ = e.st.Commit(r)
	st, err := e.orch().Status(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if e.kv.calls != 0 || e.scans != 0 {
		t.Errorf("short status read KV %d times, scanned %d times", e.kv.calls, e.scans)
	}
	if !st.NeedsRecover || !strings.Contains(strings.Join(st.Warnings, " "), "profile add of c") {
		t.Errorf("pending add not shown: %+v", st)
	}
	if len(st.Profiles) != 2 || !st.Profiles[0].Active || st.Profiles[0].Folder != "Claude" || st.Profiles[1].Folder != "Claude-b" {
		t.Errorf("profiles = %+v", st.Profiles)
	}
	full, err := e.orch().Status(context.Background(), false)
	if err != nil || !full.AppRunning || full.Profiles[1].Account == "" {
		t.Errorf("full status = %+v %v", full, err)
	}
}

func TestDoctor(t *testing.T) {
	e := newEnv(t)
	e.makeFolder("Claude-Other1", model.Replica{}, nil)
	if err := os.WriteFile(filepath.Join(e.root, ids.DefaultFolder, "unknown.bin"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	checks, err := e.orch().Doctor(context.Background(), DoctorOptions{GrantAutomation: true})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Check{}
	for _, c := range checks {
		if _, seen := byID[c.ID]; !seen {
			byID[c.ID] = c
		}
	}
	for _, id := range []string{"state", "version", "probes", "identity", "foreign-tools", "unclassified", "automation", "update", "chrome-bridge", "managed", "runner"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("missing check %s", id)
		}
	}
	if byID["foreign-tools"].Status != "fail" || !strings.Contains(byID["unclassified"].Detail, "unknown.bin") {
		t.Errorf("checks = %+v", checks)
	}
	if !strings.Contains(byID["chrome-bridge"].Detail, "one Chrome profile per account") {
		t.Errorf("bridge = %+v", byID["chrome-bridge"])
	}
	for _, c := range checks {
		if strings.Contains(c.Detail, string(acctA)) {
			t.Errorf("unmasked account in %+v", c)
		}
	}
	if e.st.commits != 0 {
		t.Error("doctor wrote state")
	}
}

func TestInitAndProfiles(t *testing.T) {
	e := newEnv(t)
	e.st.committed = nil
	e.cfg.Profiles = nil
	e.ui.interactive = true
	ctx := context.Background()
	o := e.orch()
	if err := o.Init(ctx, InitOptions{Alias: "a", Label: "first"}); err != nil {
		t.Fatal(err)
	}
	root := e.st.root(t)
	if root.Active != "a" || root.Profiles["a"].Replica != repA || len(e.cfg.Profiles) != 1 {
		t.Fatalf("init: %+v", root)
	}
	if err := o.Init(ctx, InitOptions{Alias: "z"}); codeOf(t, err) != pderr.Usage {
		t.Errorf("second init: %v", err)
	}
	if err := o.ProfileAdopt(ctx, "b", "Claude-b"); err != nil {
		t.Fatal(err)
	}
	if err := o.ProfileAdopt(ctx, "x", "Claude-b"); codeOf(t, err) != pderr.IdentityMismatch {
		t.Errorf("adopting a registered folder: %v", err)
	}
	if err := o.ProfileAdopt(ctx, "x", "../etc"); codeOf(t, err) != pderr.Usage {
		t.Errorf("bad folder name: %v", err)
	}

	// Rename b to bb while the app is stopped: the default folder follows.
	e.app.running = false
	k := state.BaseKey{Alias: "b", Replica: repB, Row: model.RowSessions}
	root = e.st.root(t)
	root.Bases[k.String()] = "ref1"
	root.Previous = "b"
	_ = e.st.Commit(root)
	if err := o.ProfileRename(ctx, "b", "bb"); err != nil {
		t.Fatal(err)
	}
	root = e.st.root(t)
	if root.Profiles["bb"] == nil || root.Profiles["b"] != nil || root.Previous != "bb" {
		t.Errorf("state not renamed: %+v", root.Profiles)
	}
	if _, ok := root.Bases[state.BaseKey{Alias: "bb", Replica: repB, Row: model.RowSessions}.String()]; !ok {
		t.Error("bases not rekeyed")
	}
	if e.account("Claude-bb") != acctB || e.account("Claude-b") != "" {
		t.Error("folder not renamed")
	}
	if _, ok := e.cfg.Profile("bb"); !ok {
		t.Error("config not renamed")
	}

	if err := o.ProfileRemove(ctx, "a"); codeOf(t, err) != pderr.Usage {
		t.Errorf("removing the active profile: %v", err)
	}
	if err := o.ProfileRemove(ctx, "bb"); err != nil {
		t.Fatal(err)
	}
	root = e.st.root(t)
	if root.Profiles["bb"] != nil || len(root.Bases) != 0 || root.Previous != "" || e.account("Claude-bb") != acctB {
		t.Errorf("remove: %+v", root)
	}
	if slices.ContainsFunc(e.cfg.Profiles, func(p config.Profile) bool { return p.Alias == "bb" }) {
		t.Error("config still lists bb")
	}
}

func TestRebaseDropsBases(t *testing.T) {
	e := newEnv(t)
	afterSwitch(t, e)
	if len(e.st.root(t).Bases) == 0 {
		t.Fatal("no bases to drop")
	}
	if err := e.orch().Rebase(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.st.root(t).Bases); n != 0 {
		t.Errorf("%d bases left", n)
	}
}

func TestPreviewSavesPlan(t *testing.T) {
	e := newEnv(t)
	res, err := e.orch().Preview(context.Background(), PreviewOptions{To: "b", Save: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.PlanHash == "" || !res.Plan.Approximate || e.plans.saves != 1 || e.st.lockCalls != 0 || e.app.quits != 0 {
		t.Errorf("preview = %+v", res)
	}
	if res.Summary == nil || res.Summary.Categories[model.CatSessions].Added != 1 {
		t.Errorf("summary = %+v", res.Summary)
	}
}

func TestSanitizeTitle(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"  a \t b\n", "a b"},
		{"\x1b[2Jx\u200f", "[2Jx"},
		{"", "(untitled)"},
		{strings.Repeat("\u00e9", 60), strings.Repeat("\u00e9", 48) + "\u2026"},
	}
	for _, tc := range tests {
		if got := sanitizeTitle(tc.in); got != tc.want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{{"2GiB", 2 << 30}, {"500MB", 500e6}, {"1024", 1024}, {"x", 0}, {"-1KiB", 0}}
	for _, tc := range tests {
		if got := parseSize(tc.in); got != tc.want {
			t.Errorf("parseSize(%q) = %d", tc.in, got)
		}
	}
}
