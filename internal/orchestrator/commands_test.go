package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/denylist"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/testkit"
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
	// The run that entered a has just finished, so a verify may record.
	r.Generations = append(r.Generations, state.GenerationRef{N: 1, RunID: "run-a", Target: "a", At: e.clk.Now()})
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

// TestVerifyLateOnlyReports: a verify that is not the first one after
// the last run, or comes long after it, reports a difference but never
// records it, so the user's own later edit is not taken for app drift.
func TestVerifyLateOnlyReports(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(e *env)
	}{
		{"already verified", func(e *env) {
			r := e.st.root(t)
			r.LastVerify = &state.VerifyRecord{RunID: "run-a", At: e.clk.Now()}
			if err := e.st.Commit(r); err != nil {
				t.Fatal(err)
			}
		}},
		{"long after the run", func(e *env) { e.clk.Advance(verifyRecordWindow + time.Minute) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			k := seedGroupOrderBase(t, e, `["x","y"]`)
			tc.setup(e)
			writeMirror(t, e, `["y","x"]`)
			res, err := e.orch().Verify(context.Background())
			if codeOf(t, err) != pderr.VerifyDrift || !slices.Equal(res.Drift, []model.RowID{model.RowGroupOrder}) {
				t.Fatalf("err = %v drift = %v", err, res.Drift)
			}
			root := e.st.root(t)
			if len(root.Contested) != 0 {
				t.Fatalf("contested recorded: %+v", root.Contested)
			}
			base, err := e.st.GetSnapshot(root.Bases[k.String()])
			intended, _ := model.ItemHash(json.RawMessage(`["x","y"]`))
			if err != nil || base.Items["order"].Hash != intended {
				t.Fatal("base moved to the observed value")
			}
		})
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
	if !st.NeedsRecover || !strings.Contains(strings.Join(i18n.Strings(st.Warnings), " "), "profile add of c") {
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
	for _, name := range []string{"unknown.bin", "cache-" + string(acctA) + ".db", "rows"} {
		if err := os.WriteFile(filepath.Join(e.root, ids.DefaultFolder, name), []byte("secret content"), 0o600); err != nil && name != "rows" {
			t.Fatal(err)
		}
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
	if _, ok := byID["lang"]; ok {
		t.Error("doctor still has a lang check")
	}
	for _, id := range []string{"state", "version", "probes", "identity", "foreign-tools", "unclassified", "automation", "update", "open-file-scan", "chrome-bridge", "busy-detection", "busy-now", "account-change", "chrome-pairing-org", "managed", "runner", "scratch", "scratch-orphans", "scratch-kept", "pins", "online", "artifacts"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("missing check %s", id)
		}
	}
	if byID["foreign-tools"].Status != "fail" || !strings.Contains(byID["unclassified"].Detail.String(), "unknown.bin") {
		t.Errorf("checks = %+v", checks)
	}
	un := byID["unclassified"]
	if un.Status != "warn" || !strings.Contains(un.Detail.String(), "cache-<id>.db") || strings.Contains(un.Detail.String(), "secret content") ||
		strings.Contains(un.Detail.String(), "identity.json") {
		t.Errorf("unclassified = %+v", un)
	}
	// No connection line in the log is normal: Claude in Chrome connects
	// on first use, so the check stays ok.
	if br := byID["chrome-bridge"]; br.Status != "ok" || !strings.Contains(br.Detail.String(), "one Chrome profile per account") {
		t.Errorf("bridge = %+v", byID["chrome-bridge"])
	}
	for _, c := range checks {
		if strings.Contains(c.Detail.String(), string(acctA)) {
			t.Errorf("unmasked account in %+v", c)
		}
	}
	if e.st.commits != 0 {
		t.Error("doctor wrote state")
	}
}

// TestObservedEntriesClassified: every top-level entry the read-only
// survey found on app 6.28899.2 (ADR 0004) is classified by the embedded
// manifest, credential-like ones as never touched; an unknown name is not.
func TestObservedEntriesClassified(t *testing.T) {
	m := testkit.Manifest(t)
	observed := []string{"DIPS", "DIPS-wal", "Extension State", "GPUPersistentCache", "GraphiteDawnCache", "InterestGroups",
		"Network Persistent State", "SharedStorage", "TransportSecurity", "VideoDecodeStats", "WebStorage", "buddy-tokens.json",
		"ca-bundle.pem", "claude-code-vm", "cowork-memory-resets.json", "declarative_performance_observer.db",
		"declarative_performance_observer.db-journal", "extensions-blocklist.json", "fcache", "git-shadow", "last-good-load.json",
		"preview-last-frames", "remote-file-download-parts.json", "scratch-workspaces", "sentry", "shared_proto_db",
		"side-session-notes", "terminal-shell-integration", "vm-support-probe.json", "worktree-dep-seed",
		"claude-code-sessions", "Local Storage", "claude_desktop_config.json", "ccd-session-presets.json", "git-worktrees.json",
		"window-state.json", "config.json"}
	for _, n := range observed {
		if !m.Classified(n) {
			t.Errorf("%s is not classified", n)
		}
	}
	never := append(append([]string(nil), m.Classification.NeverTouched...), m.Classification.NeverUntilClassified...)
	for _, n := range []string{"buddy-tokens.json", "TransportSecurity", "Network Persistent State", "SharedStorage", "InterestGroups", "DIPS", "DIPS-wal"} {
		hit := false
		for _, g := range never {
			if denylist.MatchGlob(g, n) {
				hit = true
			}
		}
		if !hit {
			t.Errorf("%s is not never touched", n)
		}
	}
	if m.Classified("surprise-new-store.json") {
		t.Error("an unknown name counts as classified")
	}
	// An unclassified entry is outside the allow list, so no op may write it.
	in := plan.ValidateInput{AllowPaths: allowedPaths(m, repA), DenyLSKeys: m.Classification.LSNeverTouched,
		Plan: &plan.Plan{Ops: []plan.Op{{Kind: plan.OpWriteFile, Path: "surprise-new-store.json", Content: []byte("{}"),
			PostHash: model.HashBytes([]byte("{}"))}}}}
	if err := plan.Validate(in); err == nil || !strings.Contains(err.Error(), "allow list") {
		t.Errorf("a write to an unclassified entry: %v", err)
	}
	in.Plan.Ops[0].Path = "claude_desktop_config.json"
	if err := plan.Validate(in); err != nil {
		t.Errorf("a write to a synced file: %v", err)
	}
	for _, p := range []string{"claude-code-sessions/" + string(testkit.DecoyReplica(1).Account) + "/" + string(testkit.DecoyReplica(1).Org) + "/x.json"} {
		in.Plan.Ops[0].Path = p
		if err := plan.Validate(in); err == nil {
			t.Errorf("a write to another account's session directory passed validation")
		}
	}
	in.Plan.Ops[0] = plan.Op{Kind: plan.OpLSPut, Key: testkit.StoreBackupKey, Content: testkit.LSValue("{}"),
		PostHash: model.HashBytes(testkit.LSValue("{}"))}
	in.AllowLSKeys = []string{testkit.StoreBackupKey}
	if err := plan.Validate(in); err == nil || !strings.Contains(err.Error(), "never touched") {
		t.Errorf("a write to the store backup: %v", err)
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
	// Claude-b holds an account no profile registers yet: init points at
	// profile adopt.
	adoptHint := false
	for _, ev := range e.ui.events {
		if strings.Contains(ev.Message.String(), "profile adopt <alias> Claude-b") {
			adoptHint = true
		}
	}
	if !adoptHint {
		t.Errorf("init did not point at profile adopt: %+v", e.ui.events)
	}
	if err := o.Init(ctx, InitOptions{Alias: "z"}); codeOf(t, err) != pderr.Usage {
		t.Errorf("second init: %v", err)
	}
	if err := o.ProfileAdopt(ctx, "b", "Claude-b", true); err != nil {
		t.Fatal(err)
	}
	if p := e.cfg.Profiles[len(e.cfg.Profiles)-1]; p.Alias != "b" || !p.ManagedByOrg {
		t.Errorf("adopted profile %+v is not managed", p)
	}
	if !e.st.root(t).LastCommitAt.Equal(e.clk.Now()) {
		t.Error("profile adopt did not move the last-switch mark")
	}
	if err := o.ProfileAdopt(ctx, "x", "Claude-b", false); codeOf(t, err) != pderr.IdentityMismatch {
		t.Errorf("adopting a registered folder: %v", err)
	}
	if err := o.ProfileAdopt(ctx, "x", "../etc", false); codeOf(t, err) != pderr.Usage {
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

// TestModeAcksFromPrefs: prefs.PermissionAcks returns preference names;
// the manifest table maps them to the permission modes the sessions row
// looks up.
func TestModeAcksFromPrefs(t *testing.T) {
	prefs := map[string]bool{"bypassPermissionsOptInByAccount": true, "otherPermissionAck": false}
	table := map[string][]string{"bypassPermissions": {"bypassPermissionsOptInByAccount"}, "acceptEdits": {"otherPermissionAck"}}
	got := modeAcks(prefs, table)
	if !got["bypassPermissions"] || got["acceptEdits"] || got["bypassPermissionsOptInByAccount"] {
		t.Fatalf("acks %v", got)
	}
	if len(modeAcks(prefs, testkit.Manifest(t).PermissionAcks)) != 0 {
		t.Error("the shipped manifest acknowledges a mode without a confirmed preference")
	}
}

// TestDowngradeAndSQLiteLSReachDecide: a switch and doctor both pass a
// downgrade (the installed version is older than the last run's) and a
// SQLite Local Storage backend to the mode decision, and doctor prints
// the decision's reasons and skipped rows.
func TestDowngradeAndSQLiteLSReachDecide(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.orch().Switch(ctx, yes("b")); err != nil {
		t.Fatal(err)
	}
	root := e.st.root(t)
	if root.LastAppVersion != "6.1" {
		t.Fatalf("last app version %q", root.LastAppVersion)
	}
	root.LastAppVersion = "6.2"
	if err := e.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.root, ids.DefaultFolder, "LocalStorage"), 0o700); err != nil {
		t.Fatal(err)
	}
	e.app.running = true
	_, _ = e.orch().Switch(ctx, yes("a"))
	if !e.compat.lastIn.Downgraded || !e.compat.lastIn.SQLiteLS {
		t.Fatalf("switch decide input %+v", e.compat.lastIn)
	}
	root = e.st.root(t)
	root.LastAppVersion = "6.2"
	if err := e.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	e.compat.decide = func(in compat.DecideInput) compat.Decision {
		return compat.Decision{Mode: compat.ModeRefuse, Reasons: []string{"the app was downgraded"},
			SkippedRows: []model.RowID{model.RowSessions}}
	}
	checks, err := e.orch().Doctor(ctx, DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !e.compat.lastIn.Downgraded || !e.compat.lastIn.SQLiteLS {
		t.Fatalf("doctor decide input %+v", e.compat.lastIn)
	}
	var reason, skipped bool
	for _, c := range checks {
		reason = reason || (c.ID == "mode-reason" && c.Detail.String() == "the app was downgraded")
		skipped = skipped || (c.ID == "skipped-rows" && strings.Contains(c.Detail.String(), "sessions"))
	}
	if !reason || !skipped {
		t.Fatalf("doctor did not explain the mode: %+v", checks)
	}
}

// TestLSAllowListNeverEmpty: a run whose rows name no Local Storage item
// (for example --only sessions) allows none, so an adapter bug that emits
// a Local Storage op is refused by plan validation.
func TestLSAllowListNeverEmpty(t *testing.T) {
	m := testkit.Manifest(t)
	allow := expandedLSKeys(m, []model.RowID{model.RowSessions}, repA)
	in := plan.ValidateInput{AllowPaths: allowedPaths(m, repA), AllowLSKeys: allow,
		Plan: &plan.Plan{Ops: []plan.Op{{Kind: plan.OpLSPut, Key: "persisted.some-other-store", Content: testkit.LSValue("{}"),
			PostHash: model.HashBytes(testkit.LSValue("{}"))}}}}
	if err := plan.Validate(in); err == nil || !strings.Contains(err.Error(), "allow list") {
		t.Fatalf("a Local Storage op in a run without Local Storage rows: %v", err)
	}
}
