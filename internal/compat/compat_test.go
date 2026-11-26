package compat_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/testkit"
)

func embeddedText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("manifests", "6.x.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEmbeddedManifest(t *testing.T) {
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Name != "6.x" || ms[1].Name != "7.x" {
		t.Fatalf("manifests %v", ms)
	}
	for _, m := range ms {
		t.Run(m.Name, func(t *testing.T) { checkEmbedded(t, m) })
	}
	// 7.x session records gained titleSetAtMs and writtenRepositories.
	sess, _ := ms[1].Row(model.RowSessions)
	if !contains(sess.Fields["max_time"], "titleSetAtMs") || !contains(sess.Fields["per_field"], "writtenRepositories") {
		t.Errorf("7.x sessions fields %v", sess.Fields)
	}
	if !contains(ms[1].Tested, "7.37095.0") {
		t.Errorf("7.x tested %v", ms[1].Tested)
	}
}

// checkEmbedded checks what every embedded manifest must hold.
func checkEmbedded(t *testing.T, m *compat.Manifest) {
	for _, r := range model.AllRows {
		s, ok := m.Row(r)
		if !ok {
			t.Errorf("row %s missing", r)
			continue
		}
		switch r {
		case model.RowGroupOrder, model.RowPinnedOrder, model.RowSidebarView:
			if s.WritePath != compat.PathOffVerify || !s.Contested || len(s.LSKeys) == 0 || len(s.MirrorPaths) != 1 || s.MirrorFile == "" {
				t.Errorf("%s: %+v", r, s)
			}
			for _, p := range s.MirrorPaths {
				if p[0] != "preferences" || p[1] != "epitaxyPrefs" {
					t.Errorf("%s: mirror path %v", r, p)
				}
			}
		default:
			if s.WritePath != compat.PathOff {
				t.Errorf("%s: write path %s", r, s.WritePath)
			}
		}
	}
	env, _ := m.Row(model.RowEnvironments)
	if env.Limits["max_presets"] != 200 || env.Limits["max_bytes"] != 512*1024 {
		t.Errorf("preset limits %v", env.Limits)
	}
	sess, _ := m.Row(model.RowSessions)
	for _, role := range []string{"per_field", "account_bound", "grant", "run_state", "busy", "max_time", "parent",
		"worktree", "worktree_path", "project_path", "tombstone_time"} {
		if _, ok := sess.Fields[role]; !ok {
			t.Errorf("sessions fields.%s missing", role)
		}
	}
	if len(sess.Fields["tombstone_time"]) != 0 {
		t.Error("a tombstone's content is a bare number; tombstone_time must be empty")
	}
	// Spike S15: the app stamps these at quit and resumes the turn by
	// itself on next open, so they must never be carried to an account.
	for _, f := range []string{"interruptedByQuitAt", "interruptedUnseenResume", "interruptedByQuitFor",
		"cycleClosedAtQuit", "armedWorkAtQuit", "cutTurn", "cutTurnChain"} {
		if !contains(sess.Fields["resume_mark"], f) {
			t.Errorf("resume_mark lacks %s", f)
		}
	}
	for _, f := range []string{"interruptedAwaitingPerson", "remoteControlLiveAtUpdateQuit", "stoppedUntilPersonSends",
		"autoSendsPausedAfterDelete", "isStarred"} {
		if !contains(sess.Fields["run_state"], f) {
			t.Errorf("run_state lacks %s", f)
		}
	}
	for _, f := range sess.Fields["resume_mark"] {
		for role, l := range sess.Fields {
			if role != "resume_mark" && contains(l, f) {
				t.Errorf("resume mark %s is also listed in %s", f, role)
			}
		}
	}
	for _, f := range []string{"chromePermissionMode", "cuAllowedApps", "cuGrantFlags", "sessionPermissionUpdates", "alwaysAllowedReasons",
		"remoteControlUserEnabled", "artifactsMcpVouchedCalls"} {
		if !contains(sess.Fields["grant"], f) {
			t.Errorf("grant lacks %s", f)
		}
		for role, l := range sess.Fields {
			if role != "grant" && contains(l, f) {
				t.Errorf("security grant %s is also listed in %s", f, role)
			}
		}
	}
	for _, f := range []string{"publishedArtifacts", "toolSurfaceSnapshot"} {
		if !contains(sess.Fields["account_bound"], f) {
			t.Errorf("account_bound lacks %s", f)
		}
	}
	for _, f := range []string{"forkedFromSessionId", "spawnedFrom.sessionId", "startChain.firstSessionId"} {
		if !contains(sess.Fields["parent"], f) {
			t.Errorf("parent lacks %s", f)
		}
	}
	if sess.Fields["project_path"][0] != "cwd" {
		t.Error("project_path must be cwd")
	}
	if !contains(sess.Fields["worktree"], "cwd") {
		t.Error("cwd must move with the worktree group")
	}
	wt, _ := m.Row(model.RowWorktrees)
	for _, f := range []string{"autoTrusted", "creationGrant"} {
		if !contains(wt.Fields["grant"], f) {
			t.Errorf("worktrees grant lacks %s", f)
		}
	}
	tasks, _ := m.Row(model.RowTasks)
	if !contains(tasks.Fields["project_path"], "cwd") || !contains(tasks.Fields["session_ref"], "originSessionId") {
		t.Errorf("tasks fields %v", tasks.Fields)
	}
	for _, want := range []string{"dframe-store.backup.*", "persisted.ccd-session-presets.*.*"} {
		if !contains(m.Classification.LSNeverTouched, want) {
			t.Errorf("ls_never_touched lacks %s", want)
		}
	}
	r := testkit.Replica(1)
	synced := m.SyncedFor(r)
	if !contains(synced, "claude-code-sessions/"+string(r.Account)+"/"+string(r.Org)+"/**") || contains(synced, "claude-code-sessions/**") {
		t.Errorf("synced must be limited to the replica's own session directory: %v", synced)
	}
	versions := map[string]int{}
	for _, p := range m.Probes {
		if p.Kind == compat.ProbePersistVersion || p.Kind == compat.ProbeJSONVersion {
			versions[p.ID] = p.Version
			if p.Critical {
				t.Errorf("version probe %s is critical; a failed one skips its row", p.ID)
			}
		}
	}
	for id, v := range map[string]int{"sidebar-store-version": 1, "pinned-store-version": 1, "started-environments-version": 1,
		"presets-version": 4, "tasks-backlog-version": 1, "worktrees-schema": 2} {
		if versions[id] != v {
			t.Errorf("probe %s version %d, want %d", id, versions[id], v)
		}
	}
	for _, rid := range []model.RowID{model.RowSidebarView, model.RowPinnedOrder} {
		if s, _ := m.Row(rid); s.PersistVersion != 1 {
			t.Errorf("%s persist_version %d", rid, s.PersistVersion)
		}
	}
	if env, _ := m.Row(model.RowEnvironments); env.PersistVersion != 4 {
		t.Errorf("environments persist_version %d", env.PersistVersion)
	}
	if !sess.PerOrg {
		t.Error("sessions must be per org")
	}
	var idx bool
	for _, p := range m.Probes {
		if p.Kind == compat.ProbeIdxVersion && p.Critical && p.Version == 1 {
			idx = true
		}
	}
	if !idx {
		t.Error("no critical idx-version probe")
	}
	for _, want := range []string{"Cookies", "Network/**", "Login Data", "Local State", "Session Storage/**", "IndexedDB/**", "config.json"} {
		if !contains(m.Classification.NeverTouched, want) {
			t.Errorf("never_touched lacks %s", want)
		}
	}
	if len(m.Classification.Cache) == 0 || len(m.Classification.Synced) == 0 {
		t.Error("cache or synced classification empty")
	}
	if !contains(m.TmpRecoveryGlobs, "local_*.json.tmp") || len(m.Tested) == 0 {
		t.Error("tmp globs or tested list")
	}
	for k, v := range m.PrefTags {
		if v != compat.TagUIState && v != compat.TagOther && v != compat.TagSecurity && v != compat.TagInternalState {
			t.Errorf("pref tag %s=%s", k, v)
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestSignals(t *testing.T) {
	m := testkit.Manifest(t)
	sig := m.Signals
	session := testkit.FakeSession(1)
	compile := func(p string) *regexp.Regexp {
		return regexp.MustCompile(strings.ReplaceAll(p, "{session}", regexp.QuoteMeta(session)))
	}
	lines := map[string]string{
		sig.QuitVetoed:      "[info] beforeQuit: vetoed by before-quit interceptor",
		sig.TurnEnded:       "[info] [Stop hook] Query completed for session " + session,
		sig.FocusedSession:  "[info] setFocusedSession: sessionId=" + session + ", other",
		sig.BridgeConnected: "[info] Chrome extension connected to bridge",
		sig.UncleanExit:     "[warn] Previous run did not exit cleanly",
	}
	for p, line := range lines {
		if p == "" || !compile(p).MatchString(line) {
			t.Errorf("pattern %q does not match %q", p, line)
		}
	}
	if compile(sig.TurnEnded).MatchString("[Stop hook] Query completed for session " + testkit.FakeSession(2)) {
		t.Error("turn_ended matches another session")
	}
	for name, p := range map[string]string{"turn_started": sig.TurnStarted,
		"pairing_disabled": sig.PairingDisabled, "login_required": sig.LoginRequired, "native_multi_account": sig.NativeMultiAccount} {
		if p != "" {
			t.Errorf("%s must stay empty until observed", name)
		}
	}
	started := regexp.MustCompile(sig.AppStarted)
	for line, want := range map[string]bool{
		"[2026-11-22 10:00:00.000] [info] [startup-perf] main window shown 1234 ms after launch": true,
		"[info] [startup-perf] main window shown 7 ms after launch":                              true,
		"[info] [startup-perf] main window shown soon ms after launch":                           false,
		"[info] main window shown 12 ms after launch":                                            false,
	} {
		if started.MatchString(line) != want {
			t.Errorf("app_started on %q: want %v", line, want)
		}
	}
}

// TestActiveOrgSignal checks the active_org pattern on synthetic lines:
// the org is captured only from well-formed lowercase UUIDs, in the named
// group the app package reads.
func TestActiveOrgSignal(t *testing.T) {
	re := regexp.MustCompile(testkit.Manifest(t).Signals.ActiveOrg)
	org := re.SubexpIndex("org")
	if org < 0 || re.SubexpIndex("account") < 0 {
		t.Fatal("active_org needs the named groups org and account")
	}
	acct, o := testkit.FakeUUID(0xa1), testkit.FakeUUID(0xb2)
	line := func(tag, a, b string) string {
		return "[2026-11-22 10:00:00.000] [info] [" + tag + "] Initialized { accountId: '" + a + "', orgId: '" + b + "' }"
	}
	tests := []struct {
		name, line, want string
	}{
		{"ccd tag", line("CCDScheduledTasks", acct, o), o},
		{"plain tag", line("ScheduledTasks", acct, o), o},
		{"upper-case org", line("CCDScheduledTasks", acct, strings.ToUpper(o)), ""},
		{"short org", line("CCDScheduledTasks", acct, o[:35]), ""},
		{"org not quoted", strings.Replace(line("CCDScheduledTasks", acct, o), "'"+o+"'", o, 1), ""},
		{"bad account", line("CCDScheduledTasks", "x", o), ""},
		{"other tag", line("OtherTasks", acct, o), ""},
		{"trailing text inside the id", line("CCDScheduledTasks", acct, o+"0"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := re.FindStringSubmatch(tc.line)
			got := ""
			if m != nil {
				got = m[org]
			}
			if got != tc.want {
				t.Fatalf("captured %q, want %q", got, tc.want)
			}
		})
	}
}

// nopRunner refuses every command: the log checks below run none.
type nopRunner struct{}

func (nopRunner) Run(context.Context, app.Template, ...string) ([]byte, error) {
	return nil, errors.New("no commands in this test")
}

// TestSignalsReadByApp feeds a synthetic main.log in a temporary home to
// the app package with the embedded manifest's signals: the active org is
// the last Initialized line's, and busy detection reports the signal as
// unknown (turn_started is empty), which callers treat as "fall back".
func TestSignalsReadByApp(t *testing.T) {
	m := testkit.Manifest(t)
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Logs", "Claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	acct, first, last := testkit.FakeUUID(0xa1), testkit.FakeUUID(0xb2), testkit.FakeUUID(0xc3)
	log := strings.Join([]string{
		"[2026-11-22 10:00:00.000] [info] [startup-perf] main window shown 900 ms after launch",
		"[2026-11-22 10:00:01.000] [info] [CCDScheduledTasks] Initialized { accountId: '" + acct + "', orgId: '" + first + "' }",
		"[2026-11-22 11:00:00.000] [info] [startup-perf] main window shown 800 ms after launch",
		"[2026-11-22 11:00:01.000] [info] [ScheduledTasks] Initialized { accountId: '" + acct + "', orgId: '" + last + "' }",
		"[2026-11-22 11:00:02.000] [info] [Stop hook] Query completed for session " + testkit.FakeSession(1),
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "main.log"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := app.NewDarwin(app.DarwinConfig{Home: home, Exec: nopRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	org, found, err := p.ActiveOrg(context.Background(), m.Signals, ids.AccountID(acct))
	if err != nil || !found || string(org) != last {
		t.Fatalf("active org %q found %v err %v", org, found, err)
	}
	if _, err := p.BusySessions(context.Background(), 0, m.Signals); !errors.Is(err, app.ErrSignalUnknown) {
		t.Fatalf("busy sessions: %v, want ErrSignalUnknown", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.log"), []byte("[info] [CCDScheduledTasks] Initialized { accountId: 'x', orgId: 'y' }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := p.ActiveOrg(context.Background(), m.Signals, ids.AccountID(acct)); err != nil || found {
		t.Fatalf("malformed ids gave found %v err %v", found, err)
	}
}

func writeOverride(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOverrides(t *testing.T) {
	base := embeddedText(t)
	tests := []struct {
		name string
		text string
		ok   bool
		want func(*testing.T, []*compat.Manifest)
	}{
		{"adds never_touched", strings.Replace(base, `never_touched = [`, `never_touched = [
  "Extra Secret Store",`, 1), true, func(t *testing.T, ms []*compat.Manifest) {
			if !contains(ms[0].Classification.NeverTouched, "Extra Secret Store") || !contains(ms[0].Classification.NeverTouched, "Cookies") {
				t.Error("override not applied")
			}
		}},
		{"removes never_touched", strings.Replace(base, `  "Cookies",`+"\n", "", 1), false, nil},
		{"unknown key", base + "\nsurprise = 1\n", false, nil},
		{"unknown row key", strings.Replace(base, `row = "tasks"`, "row = \"tasks\"\nbogus = true", 1), false, nil},
		{"bad signal", strings.Replace(base, `unclean_exit = 'Previous run did not exit cleanly'`, `unclean_exit = '('`, 1), false, nil},
		{"removes ls_never_touched", strings.Replace(base, `  "dframe-store.backup.*",`+"\n", "", 1), false, nil},
		{"removes never_until_classified", strings.Replace(base, `  "scratch-workspaces/**",`+"\n", "", 1), false, nil},
		{"widens synced", strings.Replace(base, `synced = [`, `synced = [
  "**",`, 1), false, nil},
		{"other name over the same range", strings.Replace(strings.Replace(strings.Replace(base, `name = "6.x"`, `name = "local"`, 1),
			`min_version = "6.0.0"`, `min_version = "5.0.0"`, 1), `  "Cookies",`+"\n", "", 1), false, nil},
		{"bad json-version probe", strings.Replace(base, "path = [\"schemaVersion\"]\nversion = 2", "path = [\"schemaVersion\"]\nversion = 0", 1), false, nil},
		{"new manifest", strings.Replace(strings.Replace(base, `name = "6.x"`, `name = "8.x"`, 1),
			`min_version = "6.0.0"`+"\n"+`max_version = "6.999999.999"`, `min_version = "8.0.0"`+"\n"+`max_version = "8.999999.999"`, 1), true,
			func(t *testing.T, ms []*compat.Manifest) {
				if len(ms) != 3 || ms[2].Name != "8.x" {
					t.Errorf("manifests %d", len(ms))
				}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeOverride(t, dir, "override.toml", tc.text)
			writeOverride(t, dir, "notes.txt", "ignored")
			ms, err := compat.Load(dir)
			if (err == nil) != tc.ok {
				t.Fatalf("err %v", err)
			}
			if tc.want != nil {
				tc.want(t, ms)
			}
		})
	}
	if ms, err := compat.Load(filepath.Join(t.TempDir(), "absent")); err != nil || len(ms) != 2 {
		t.Fatalf("missing override dir: %v", err)
	}
}

func TestSelect(t *testing.T) {
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		version string
		found   bool
		tested  bool
		err     bool
	}{
		{"6.28899.2", true, true, false},
		{"6.28899.2.0", true, true, false},
		{"6.28899.10", true, false, false},
		{"6.0.0", true, false, false},
		{"6", true, false, false},
		{"6.999999.999", true, false, false},
		{"6.1000000.0", false, false, false},
		{"7.0.0", true, false, false},
		{"7.37095.0", true, true, false},
		{"7.1000000.0", false, false, false},
		{"8.0.0", false, false, false},
		{"5.99.99", false, false, false},
		{"6.x", false, false, true},
		{"06.1", false, false, true},
		{"", false, false, true},
	}
	for _, tc := range tests {
		m, tested, err := compat.Select(ms, tc.version)
		if (err != nil) != tc.err || (m != nil) != tc.found || tested != tc.tested {
			t.Errorf("%q: manifest %v tested %v err %v", tc.version, m != nil, tested, err)
		}
	}
}

func probeEnv(t *testing.T, folder string, r model.Replica) compat.ProbeEnv {
	return compat.ProbeEnv{Folder: folder, Replica: r, LS: testkit.LS(t, folder)}
}

func failed(t *testing.T, m *compat.Manifest, env compat.ProbeEnv) map[string]compat.ProbeResult {
	t.Helper()
	res, err := compat.RunProbes(context.Background(), m, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != len(m.Probes) {
		t.Fatalf("%d results for %d probes", len(res), len(m.Probes))
	}
	out := map[string]compat.ProbeResult{}
	for _, r := range res {
		if strings.Contains(r.Detail, testkit.Canary) || strings.Contains(r.Detail, "00000000-") {
			t.Errorf("probe %s detail carries a value: %q", r.ProbeID, r.Detail)
		}
		if !r.OK {
			out[r.ProbeID] = r
		}
	}
	return out
}

func TestProbesPassOnFixtures(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	rich := root.AddFolder(t, testkit.Rich("Claude", 1))
	if f := failed(t, m, probeEnv(t, rich, testkit.Replica(1))); len(f) != 0 {
		t.Fatalf("failed on a rich folder: %v", f)
	}
}

func TestProbesFailOnMutatedShapes(t *testing.T) {
	m := testkit.Manifest(t)
	r := testkit.Replica(1)
	sess := "claude-code-sessions/" + string(r.Account) + "/" + string(r.Org)
	tests := []struct {
		name   string
		mutate func(t *testing.T, folder string)
		probe  string
	}{
		{"idx version", func(t *testing.T, f string) {
			writeOverride(t, filepath.Join(f, sess), "archived-sessions.idx", `{"v":2,"archived":[]}`)
		}, "sessions-idx-version"},
		{"no config", func(t *testing.T, f string) { _ = os.Remove(filepath.Join(f, "config.json")) }, "identity-config"},
		{"account not a string", func(t *testing.T, f string) {
			writeOverride(t, f, "config.json", `{"oauth:tokenCacheV2":"`+testkit.Canary+`","lastKnownAccountUuid":1}`)
		}, "identity-account"},
		{"record not an object", func(t *testing.T, f string) {
			writeOverride(t, filepath.Join(f, sess), testkit.FakeSession(1)+".json", `[]`)
		}, "sessions-records"},
		{"no session dir", func(t *testing.T, f string) { _ = os.RemoveAll(filepath.Join(f, sess)) }, "sessions-dir"},
		{"presets wrapper", func(t *testing.T, f string) {
			testkit.WriteLS(t, f, map[string]string{"persisted.ccd-session-presets." + string(r.Account): `{"state":{"presets":{}},"version":0}`})
		}, "presets-wrapper"},
		{"store missing", func(t *testing.T, f string) {
			_ = os.RemoveAll(filepath.Join(f, testkit.LevelDBRel))
			testkit.WriteLS(t, f, map[string]string{"x": "1"})
		}, "sidebar-store-key"},
		{"store without version", func(t *testing.T, f string) {
			testkit.WriteLS(t, f, map[string]string{"dframe-store": `{"state":{}}`})
		}, "sidebar-store-version"},
		{"mirror type", func(t *testing.T, f string) {
			writeOverride(t, f, "claude_desktop_config.json", `{"preferences":{"epitaxyPrefs":{"code-projects-order.`+string(r.Account)+`":{}}}}`)
		}, "group-order-mirror"},
		{"worktrees type", func(t *testing.T, f string) { writeOverride(t, f, "git-worktrees.json", `{"worktrees":"x"}`) }, "worktrees"},
		{"worktrees schema", func(t *testing.T, f string) {
			writeOverride(t, f, "git-worktrees.json", `{"schemaVersion":3,"worktrees":{}}`)
		}, "worktrees-schema"},
		{"worktrees schema missing", func(t *testing.T, f string) { writeOverride(t, f, "git-worktrees.json", `{"worktrees":{}}`) }, "worktrees-schema"},
		{"store version", func(t *testing.T, f string) {
			testkit.WriteLS(t, f, map[string]string{"dframe-store": `{"version":2,"shared":1,"state":{}}`})
		}, "sidebar-store-version"},
		{"pinned store version", func(t *testing.T, f string) {
			testkit.WriteLS(t, f, map[string]string{"dframe-store": `{"version":0,"state":{}}`})
		}, "pinned-store-version"},
		{"presets version", func(t *testing.T, f string) {
			testkit.WriteLS(t, f, map[string]string{"persisted.ccd-session-presets." + string(r.Account): `{"state":{"presets":[]},"version":3}`})
		}, "presets-version"},
		{"started version", func(t *testing.T, f string) {
			testkit.WriteLS(t, f, map[string]string{"persisted.ccd-started-environments": `{"state":{"bySessionId":{}},"version":0}`})
		}, "started-environments-version"},
		{"presets file account", func(t *testing.T, f string) {
			writeOverride(t, f, "ccd-session-presets.json", `{"byAccount":{"`+string(r.Account)+`":{"presets":{}}}}`)
		}, "presets-file-account"},
		{"backlog version", func(t *testing.T, f string) {
			writeOverride(t, filepath.Join(f, sess, "backlog"), "tasks.json", `{"version":2,"items":[]}`)
		}, "tasks-backlog-version"},
		{"backlog version not a number", func(t *testing.T, f string) {
			writeOverride(t, filepath.Join(f, sess, "backlog"), "tasks.json", `{"version":"1","items":[]}`)
		}, "tasks-backlog-version"},
		{"scheduled skips type", func(t *testing.T, f string) {
			writeOverride(t, filepath.Join(f, sess), "scheduled-tasks.json", `{"scheduledTasks":[],"recordedSkips":[]}`)
		}, "tasks-scheduled-skips"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			folder := testkit.NewDataRoot(t).AddFolder(t, testkit.Rich("Claude", 1))
			tc.mutate(t, folder)
			f := failed(t, m, probeEnv(t, folder, r))
			if _, ok := f[tc.probe]; !ok {
				t.Fatalf("probe %s did not fail (failed: %v)", tc.probe, f)
			}
		})
	}
}

// TestProbesPassWhenOptionalPartsAbsent: mcpServers is absent from the
// config on the surveyed Mac, and a folder may lack the backlog, the
// worktree registry or the presets; none of that fails a probe.
func TestProbesPassWhenOptionalPartsAbsent(t *testing.T) {
	m := testkit.Manifest(t)
	r := testkit.Replica(1)
	spec := testkit.Rich("Claude", 1)
	spec.MCPServers = nil
	spec.Worktrees = nil
	spec.Backlog = nil
	folder := testkit.NewDataRoot(t).AddFolder(t, spec)
	b, err := os.ReadFile(filepath.Join(folder, "claude_desktop_config.json"))
	if err != nil || strings.Contains(string(b), "mcpServers") {
		t.Fatalf("fixture still has mcpServers: %v", err)
	}
	if f := failed(t, m, probeEnv(t, folder, r)); len(f) != 0 {
		t.Fatalf("failed: %v", f)
	}
}

func TestDecide(t *testing.T) {
	m := testkit.Manifest(t)
	ok := compat.DecideInput{Manifest: m, Version: "6.28899.2", Tested: true, IdentityOK: true, CompressionOK: true,
		Probes: []compat.ProbeResult{{ProbeID: "x", Row: model.RowTasks, OK: true}}}
	with := func(f func(*compat.DecideInput)) compat.DecideInput {
		in := ok
		in.Probes = append([]compat.ProbeResult(nil), ok.Probes...)
		f(&in)
		return in
	}
	km := *m
	km.KnownBad = []string{"6.28899.2"}
	var perOrg []model.RowID
	for _, r := range model.AllRows {
		if s, _ := m.Row(r); s.PerOrg {
			perOrg = append(perOrg, r)
		}
	}
	tests := []struct {
		name    string
		in      compat.DecideInput
		mode    compat.Mode
		skipped []model.RowID
	}{
		{"known version, probes pass", ok, compat.ModeFull, nil},
		{"unknown version", with(func(i *compat.DecideInput) { i.Tested = false }), compat.ModeFullWarn, nil},
		{"unknown version, require tested", with(func(i *compat.DecideInput) { i.Tested, i.RequireTested = false, true }), compat.ModePreviewOnly, nil},
		{"known bad", with(func(i *compat.DecideInput) { i.Manifest = &km }), compat.ModeSwapOnly, nil},
		{"no manifest", with(func(i *compat.DecideInput) { i.Manifest = nil }), compat.ModeSwapOnly, nil},
		{"row probe failed", with(func(i *compat.DecideInput) {
			i.Probes = append(i.Probes, compat.ProbeResult{ProbeID: "w", Row: model.RowWorktrees}, compat.ProbeResult{ProbeID: "e", Row: model.RowEnvironments})
		}), compat.ModeDegraded, []model.RowID{model.RowEnvironments, model.RowWorktrees}},
		{"critical row probe failed", with(func(i *compat.DecideInput) {
			i.Probes = append(i.Probes, compat.ProbeResult{ProbeID: "w", Row: model.RowWorktrees, Critical: true})
		}), compat.ModeSwapOnly, nil},
		{"identity probe failed", with(func(i *compat.DecideInput) {
			i.Probes = append(i.Probes, compat.ProbeResult{ProbeID: "identity-account"})
		}), compat.ModeSwapOnly, nil},
		{"sqlite", with(func(i *compat.DecideInput) { i.SQLiteLS = true }), compat.ModeSwapOnly, nil},
		{"unknown compression", with(func(i *compat.DecideInput) { i.CompressionOK = false }), compat.ModeSwapOnly, nil},
		{"native multi-account", with(func(i *compat.DecideInput) { i.NativeMultiAccount = true }), compat.ModeSwapOnly, nil},
		{"org unknown", with(func(i *compat.DecideInput) { i.OrgUnknown = true }), compat.ModeDegraded, perOrg},
		{"identity unreadable", with(func(i *compat.DecideInput) { i.IdentityOK = false }), compat.ModeRefuse, nil},
		{"downgrade", with(func(i *compat.DecideInput) { i.Downgraded = true; i.SQLiteLS = true }), compat.ModeRefuse, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := compat.Decide(tc.in)
			if d.Mode != tc.mode {
				t.Fatalf("mode %s, want %s (%v)", d.Mode, tc.mode, d.Reasons)
			}
			if len(d.SkippedRows) != len(tc.skipped) {
				t.Fatalf("skipped %v, want %v", d.SkippedRows, tc.skipped)
			}
			for i := range tc.skipped {
				if d.SkippedRows[i] != tc.skipped[i] {
					t.Fatalf("skipped %v, want %v", d.SkippedRows, tc.skipped)
				}
			}
			if d.Mode != compat.ModeFull && len(d.Reasons) == 0 {
				t.Error("no reason given")
			}
		})
	}
	if len(perOrg) < 3 {
		t.Fatalf("per-org rows %v", perOrg)
	}
	g := compat.Gate{Manifests: []*compat.Manifest{m}}
	if d := g.Decide(ok); d.Mode != compat.ModeFull {
		t.Fatal("gate")
	}
}

// TestBridgeSignal7x: with 7.x the app logs either line when Claude in
// Chrome connects, depending on which side opened the connection.
func TestBridgeSignal7x(t *testing.T) {
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := compat.Select(ms, "7.37095.0")
	if err != nil || m == nil {
		t.Fatalf("select: %v", err)
	}
	re := regexp.MustCompile(m.Signals.BridgeConnected)
	for line, want := range map[string]bool{
		"2026-11-26 11:59:10 [info] [claude-in-chrome] Paired with Chrome extension (duration: 990ms)": true,
		"2026-11-25 08:06:04 [info] [claude-in-chrome] Chrome extension connected to bridge":           true,
		"2026-11-26 11:59:09 [info] [claude-in-chrome] Connecting to bridge: wss://example.invalid":    false,
		"2026-11-26 11:59:09 [info] [claude-in-chrome] Not connecting, starting connection...":         false,
	} {
		if got := re.MatchString(line); got != want {
			t.Errorf("%q: match %v, want %v", line, got, want)
		}
	}
}
