package compat_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
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
	if len(ms) != 1 || ms[0].Name != "6.x" {
		t.Fatalf("manifests %v", ms)
	}
	m := ms[0]
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
	for _, role := range []string{"per_field", "account_bound", "run_state", "max_time", "parent"} {
		if _, ok := sess.Fields[role]; !ok {
			t.Errorf("sessions fields.%s missing", role)
		}
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
	if !contains(m.TmpRecoveryGlobs, "local_*.json.tmp") || !contains(m.Tested, "6.28899.2") {
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
	for name, p := range map[string]string{"turn_started": sig.TurnStarted, "app_started": sig.AppStarted, "active_org": sig.ActiveOrg,
		"pairing_disabled": sig.PairingDisabled, "login_required": sig.LoginRequired, "native_multi_account": sig.NativeMultiAccount} {
		if p != "" {
			t.Errorf("%s must stay empty until observed", name)
		}
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
		{"new manifest", strings.Replace(strings.Replace(base, `name = "6.x"`, `name = "7.x"`, 1),
			`min_version = "6.0.0"`+"\n"+`max_version = "6.999999.999"`, `min_version = "7.0.0"`+"\n"+`max_version = "7.999999.999"`, 1), true,
			func(t *testing.T, ms []*compat.Manifest) {
				if len(ms) != 2 || ms[1].Name != "7.x" {
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
	if ms, err := compat.Load(filepath.Join(t.TempDir(), "absent")); err != nil || len(ms) != 1 {
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
		{"7.0.0", false, false, false},
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
