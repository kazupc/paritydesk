package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/legacy"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/configjson"
	"github.com/kazupc/paritydesk/internal/state"
)

// Fake ids of the legacy tests: Claude holds account W (org WO), the
// parked Claude-Home holds account H (org HO).
var (
	lgW, lgWO = ids.AccountID(fakeUUID(0x101)), ids.OrgID(fakeUUID(0x102))
	lgH, lgHO = ids.AccountID(fakeUUID(0x201)), ids.OrgID(fakeUUID(0x202))
	lgX, lgXO = ids.AccountID(fakeUUID(0x301)), ids.OrgID(fakeUUID(0x302))
)

// legacyManifest is the test manifest with the [legacy] section of the
// embedded manifests.
func legacyManifest() *compat.Manifest {
	m := testManifest()
	m.Legacy = compat.Legacy{
		IgnoreNames:        []string{".DS_Store"},
		AgreeIgnoringRoles: []string{"busy"},
		Stubs: []compat.LegacyStub{{File: "scheduled-tasks.json",
			EmptyMembers: []string{"scheduledTasks", "recordedSkips", "recordedPlaceSkips", "runRetries"},
			FlagMembers:  []string{"sundayAliasBoundaryStamped", "dayFieldsOrBoundaryStamped"}}},
		ScopedDirs: []compat.LegacyDir{
			{ID: "code-sessions", Root: "claude-code-sessions", Layout: compat.LayoutAcctOrg, Quarantine: true, UniqueCheck: true},
			{ID: "agent-sessions", Root: "local-agent-mode-sessions", Layout: compat.LayoutAcctOrg, SkipNames: []string{"skills-plugin"},
				Quarantine: true, UniqueCheck: true},
			{ID: "scratch-workspaces", Root: "scratch-workspaces", Layout: compat.LayoutAcctOrg},
		},
	}
	// Sessions are per org (session directories).
	for i := range m.Rows {
		if m.Rows[i].Row == model.RowSessions {
			m.Rows[i].PerOrg = true
		}
	}
	return m
}

// newLegacyEnv builds a fresh state (no profile) over Claude (W) and
// Claude-Home (H), with the real scanner, resolver and generations.
func newLegacyEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	for _, n := range []string{ids.DefaultFolder, "Claude-b"} {
		if err := os.RemoveAll(filepath.Join(e.root, n)); err != nil {
			t.Fatal(err)
		}
	}
	e.cfg.Profiles = nil
	e.st.committed, e.st.commits = nil, 0
	e.app.running = false
	e.app.bundleScoped = true
	e.compat.m = legacyManifest()
	e.legacyFolder(ids.DefaultFolder, lgW, lgWO, map[string]any{
		string(fakeSession(1)): map[string]any{"title": "shared"},
		string(fakeSession(2)): map[string]any{"title": "work only"},
		string(fakeSession(3)): map[string]any{"title": "one", "busy": true},
	})
	e.legacyFolder("Claude-Home", lgH, lgHO, map[string]any{
		string(fakeSession(1)): map[string]any{"title": "shared"},
		string(fakeSession(3)): map[string]any{"title": "one", "busy": false},
		string(fakeSession(4)): map[string]any{"title": "home only"},
	})
	genDir := filepath.Join(filepath.Dir(e.root), "support", "generations")
	e.tweak = func(d *Deps) {
		d.Resolve = profile.Resolve
		d.Folders = profile.Scanner{FS: e.fs, DataRoot: e.root, ReadIdentity: readIdentityFile, ListReplicas: sources.DiscoverReplicas}
		d.Gens = gen.Open(genDir, e.fs, e.clk)
		d.Paths.GenDir = genDir
		// The real merge modules: their union-key rule is what seeding
		// relies on.
		reg := rows.Registry()
		for _, r := range []model.RowID{model.RowSessions, model.RowEnvironments, model.RowGroupOrder} {
			d.Rows[r] = reg[r]
		}
	}
	return e
}

// legacyFolder makes a data folder with an identity, its session directory
// and a sessions row.
func (e *env) legacyFolder(name string, acct ids.AccountID, org ids.OrgID, sessions map[string]any) {
	e.t.Helper()
	e.makeFolder(name, model.Replica{Account: acct, Org: org}, map[model.RowID]map[string]any{
		model.RowSessions:     sessions,
		model.RowEnvironments: {"preset/p1": map[string]any{"name": "env"}},
	})
	if acct != "" {
		e.put(name+"/claude-code-sessions/"+string(acct)+"/"+string(org)+"/local_"+fakeUUID(0x77)+".json", "{}")
	}
}

func readIdentityFile(folder string) (configjson.Identity, error) {
	b, err := os.ReadFile(filepath.Join(folder, "identity.json"))
	if err != nil {
		return configjson.Identity{}, err
	}
	var id identityFile
	if err := json.Unmarshal(b, &id); err != nil {
		return configjson.Identity{}, err
	}
	if id.Account == "" {
		return configjson.Identity{}, configjson.ErrNoAccount
	}
	return configjson.Identity{Account: ids.AccountID(id.Account)}, nil
}

func (e *env) put(rel, content string) {
	e.t.Helper()
	p := filepath.Join(e.root, filepath.FromSlash(rel))
	e.makeDir(filepath.Dir(p))
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func legacyFlags(aliases map[string]ids.Alias) LegacyOptions {
	if aliases == nil {
		aliases = map[string]ids.Alias{ids.DefaultFolder: "work"}
	}
	return LegacyOptions{Aliases: aliases}
}

func (e *env) dry(opts LegacyOptions) *LegacyResult {
	e.t.Helper()
	opts.Apply = false
	res, err := e.orch().AdoptLegacy(context.Background(), opts)
	if err != nil {
		e.t.Fatalf("dry run: %v", err)
	}
	return res
}

// applyLegacy runs the dry run, then the apply with its hash.
func (e *env) applyLegacy(opts LegacyOptions) (*LegacyResult, error) {
	e.t.Helper()
	res := e.dry(opts)
	opts.Apply, opts.PlanHash = true, res.PlanHash
	if opts.Managed == nil {
		opts.Managed = map[ids.Alias]bool{}
	}
	return e.orch().AdoptLegacy(context.Background(), opts)
}

func folderOf(res *LegacyResult, name string) LegacyFolder {
	for _, f := range res.Folders {
		if f.Folder == name {
			return f
		}
	}
	return LegacyFolder{}
}

// resolveNow resolves the committed config and root with the real
// resolver over a fresh scan.
func (e *env) resolveNow() *profile.Resolution {
	e.t.Helper()
	d := e.deps()
	folders, err := d.Folders.Scan(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	res, err := profile.Resolve(e.cfg, e.st.root(e.t), folders, profile.Hints{ActiveOrg: e.app.org})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

// dataRootDigest hashes every path, content and marker of the data root.
func (e *env) dataRootDigest() string {
	e.t.Helper()
	var lines []string
	err := filepath.WalkDir(filepath.Dir(e.root), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		line := p
		if d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			line += " " + model.HashBytes(b)
		}
		if d.IsDir() {
			if m, _ := readMarker(e.fs, p); m != nil {
				b, _ := json.Marshal(m)
				line += " marker " + string(b)
			}
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	sort.Strings(lines)
	return model.HashBytes([]byte(strings.Join(lines, "\n")))
}

func TestLegacyDryRunWritesNothing(t *testing.T) {
	e := newLegacyEnv(t)
	e.put("Claude-Home/claude-code-sessions/"+string(lgW)+"/"+string(lgWO)+"/local_x.json", "{}")
	before := e.dataRootDigest()
	e.fs.Calls = nil
	e.st.lockErr = pderr.New(pderr.RunLockBusy, "lock", "held elsewhere")
	res := e.dry(legacyFlags(nil))
	if res.Status != legacy.PlanOK || !res.DryRun || res.Applied || res.PlanHash == "" || !strings.Contains(res.ApplyCommand, "--legacy-plan "+res.PlanHash) {
		t.Fatalf("result %+v", res)
	}
	if e.st.commits != 0 || e.st.committed != nil || len(e.st.objects) != 0 || e.st.lockCalls != 0 {
		t.Fatalf("the dry run wrote state or took the lock: commits %d objects %d locks %d", e.st.commits, len(e.st.objects), e.st.lockCalls)
	}
	for _, c := range e.fs.Calls {
		if op := strings.SplitN(c, " ", 2)[0]; op != "ID" && op != "GetXattr" {
			t.Fatalf("the dry run called %s", c)
		}
	}
	if e.dataRootDigest() != before || len(e.cfg.Profiles) != 0 {
		t.Fatal("the dry run changed the data root or the config")
	}
	if !res.Seed.Seeded || len(folderOf(res, "Claude-Home").Leftovers) != 1 {
		t.Fatalf("seed %+v leftovers %+v", res.Seed, folderOf(res, "Claude-Home"))
	}
	// Read-only mode: the same plan.
	e.readOnly = true
	if r := e.dry(legacyFlags(nil)); r.PlanHash != res.PlanHash {
		t.Fatal("read-only dry run differs")
	}
	// A state bound to another data root: exit 2.
	e.readOnly = false
	root := state.NewRoot("/elsewhere")
	if err := e.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	if _, err := e.orch().AdoptLegacy(context.Background(), legacyFlags(nil)); pderr.CodeOf(err) != pderr.Usage {
		t.Fatalf("binding: %v", err)
	}
}

func TestLegacyUnknownFolderFlags(t *testing.T) {
	e := newLegacyEnv(t)
	for _, opts := range []LegacyOptions{
		{Aliases: map[string]ids.Alias{"Claude-Nowhere": "x"}},
		{Exclude: []string{"Claude-Nowhere"}},
	} {
		if _, err := e.orch().AdoptLegacy(context.Background(), opts); pderr.CodeOf(err) != pderr.Usage {
			t.Errorf("%+v: %v", opts, err)
		}
	}
}

func TestLegacyNoActive(t *testing.T) {
	e := newLegacyEnv(t)
	if err := os.Rename(filepath.Join(e.root, "Claude"), filepath.Join(e.root, "Claude-Work")); err != nil {
		t.Fatal(err)
	}
	res := e.dry(legacyFlags(nil))
	if res.Status != legacy.PlanNoActive || !slices.Contains(res.Warnings, LegacyWarnNoActive) || len(res.Folders) != 2 {
		t.Fatalf("dry run %+v", res)
	}
	opts := legacyFlags(nil)
	opts.Apply, opts.PlanHash, opts.Managed = true, res.PlanHash, map[ids.Alias]bool{}
	if _, err := e.orch().AdoptLegacy(context.Background(), opts); pderr.CodeOf(err) != pderr.NeedsRecover {
		t.Fatalf("apply: %v", err)
	}
	if e.st.committed != nil || len(e.cfg.Profiles) != 0 {
		t.Fatal("something was registered")
	}
	// Claude without an identity.
	e = newLegacyEnv(t)
	if err := os.WriteFile(filepath.Join(e.root, "Claude", "identity.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = e.dry(legacyFlags(nil))
	if res.Status != legacy.PlanNoActiveIdentity || !slices.Contains(res.Warnings, LegacyWarnNoActiveIdentity) {
		t.Fatalf("dry run %+v", res)
	}
	if _, err := e.applyLegacy(legacyFlags(nil)); pderr.CodeOf(err) != pderr.IdentityMismatch {
		t.Fatalf("apply: %v", err)
	}
	if len(e.cfg.Profiles) != 0 {
		t.Fatal("something was registered")
	}
}

func TestLegacyUnreadableFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	e := newLegacyEnv(t)
	e.legacyFolder("Claude-Locked", lgX, lgXO, map[string]any{})
	locked := filepath.Join(e.root, "Claude-Locked", "claude-code-sessions")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	res := e.dry(legacyFlags(nil))
	if f := folderOf(res, "Claude-Locked"); f.Status != "skip" || f.Reason != legacy.ReasonUnreadable {
		t.Fatalf("locked %+v", f)
	}
	if f := folderOf(res, "Claude-Home"); f.Status != "adopt" {
		t.Fatalf("home %+v", f)
	}
}

func TestLegacyApplyFresh(t *testing.T) {
	e := newLegacyEnv(t)
	res, err := e.applyLegacy(legacyFlags(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || !res.Seed.Seeded {
		t.Fatalf("result %+v", res)
	}
	if len(e.cfg.Profiles) != 2 || e.cfg.Profiles[0].Alias != "work" || e.cfg.Profiles[0].Folder != "" ||
		e.cfg.Profiles[1].Alias != "home" || e.cfg.Profiles[1].Folder != "Claude-Home" {
		t.Fatalf("config %+v", e.cfg.Profiles)
	}
	root := e.st.root(t)
	if root.Active != "work" || root.Epoch != 1 || root.Profiles["work"].LiveEpoch != 1 || root.Profiles["home"].LiveEpoch != 1 ||
		root.LastCommitAt.IsZero() {
		t.Fatalf("root %+v", root)
	}
	for _, a := range []ids.Alias{"work", "home"} {
		ps := root.Profiles[a]
		if ps.SeededFrom != state.SeededLegacy || ps.MarkerBase == "" {
			t.Fatalf("%s state %+v", a, ps)
		}
	}
	if root.Profiles["work"].MarkerBase != root.Profiles["home"].MarkerBase {
		t.Fatal("participants carry different seed hashes")
	}
	// Bases hold the agreed keys only: session 1 (equal) and session 3
	// (differs only in the ignored busy field); environments agree.
	k := state.BaseKey{Alias: "home", Replica: model.Replica{Account: lgH, Org: lgHO}, Row: model.RowSessions}.String()
	base, err := e.st.GetSnapshot(root.Bases[k])
	if err != nil {
		t.Fatal(err)
	}
	if got := base.Keys(); !slices.Equal(got, []string{string(fakeSession(1)), string(fakeSession(3))}) {
		t.Fatalf("home's sessions base %v", got)
	}
	for _, name := range []string{"Claude", "Claude-Home"} {
		m, _ := readMarker(e.fs, filepath.Join(e.root, name))
		if m == nil || m.BaseHash != root.Profiles["work"].MarkerBase || m.Generation != 0 {
			t.Fatalf("%s marker %+v", name, m)
		}
	}
	rz := e.resolveNow()
	for _, a := range []ids.Alias{"work", "home"} {
		if b := rz.Bindings[a]; b == nil || !b.BaseValid {
			t.Fatalf("%s binding %+v", a, b)
		}
	}
	// The first switch is confirmed and deletes nothing.
	pr, err := e.orch().Preview(context.Background(), PreviewOptions{To: "home"})
	if err != nil {
		t.Fatal(err)
	}
	if !pr.Plan.NeedsConfirmation || !pr.Plan.Seeded {
		t.Fatalf("plan %+v", pr.Plan)
	}
	for _, c := range pr.Plan.Changes {
		if c.Kind == model.ChangeDeleted {
			t.Fatalf("the first switch deletes %s", c.Key)
		}
	}
	sw, err := e.orch().Switch(context.Background(), yes("home"))
	if err != nil || sw.Code != pderr.OK {
		t.Fatalf("switch %+v %v", sw, err)
	}
	root = e.st.root(t)
	if root.Profiles["work"].SeededFrom != "" || root.Profiles["home"].SeededFrom != "" ||
		root.Profiles["work"].LiveEpoch != 1 || root.Profiles["home"].LiveEpoch != 2 || root.Epoch != 2 {
		t.Fatalf("after the first switch %+v %+v", root.Profiles["work"], root.Profiles["home"])
	}
	if !slices.Contains(e.keys("Claude", model.RowSessions), string(fakeSession(2))) {
		t.Fatal("work's own session was lost")
	}
	pr, err = e.orch().Preview(context.Background(), PreviewOptions{To: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Plan.Seeded {
		t.Fatal("the second switch still says first switch after adopting")
	}
}

func TestLegacyThreeFoldersAgreement(t *testing.T) {
	e := newLegacyEnv(t)
	e.legacyFolder("Claude-Lab", lgX, lgXO, map[string]any{
		string(fakeSession(1)): map[string]any{"title": "shared"},
		string(fakeSession(4)): map[string]any{"title": "home only"},
	})
	if _, err := e.applyLegacy(legacyFlags(nil)); err != nil {
		t.Fatal(err)
	}
	root := e.st.root(t)
	k := state.BaseKey{Alias: "lab", Replica: model.Replica{Account: lgX, Org: lgXO}, Row: model.RowSessions}.String()
	base, err := e.st.GetSnapshot(root.Bases[k])
	if err != nil {
		t.Fatal(err)
	}
	// Session 4 is held by two folders only: not agreed.
	if got := base.Keys(); !slices.Equal(got, []string{string(fakeSession(1))}) {
		t.Fatalf("lab's base %v", got)
	}
}

func TestLegacyAfterInit(t *testing.T) {
	e := newLegacyEnv(t)
	if err := e.orch().Init(context.Background(), InitOptions{Alias: "work"}); err != nil {
		t.Fatal(err)
	}
	res, err := e.applyLegacy(legacyFlags(map[string]ids.Alias{}))
	if err != nil {
		t.Fatal(err)
	}
	if folderOf(res, "Claude").Status != "registered" || folderOf(res, "Claude-Home").Status != "adopt" || !res.Seed.Seeded {
		t.Fatalf("result %+v", res)
	}
	if ps := e.st.root(t).Profiles["work"]; ps.SeededFrom != state.SeededLegacy {
		t.Fatalf("the active profile was not seeded: %+v", ps)
	}
	// With existing bases: has_bases; registration still done.
	e = newLegacyEnv(t)
	if err := e.orch().Init(context.Background(), InitOptions{Alias: "work"}); err != nil {
		t.Fatal(err)
	}
	root := e.st.root(t)
	root.Bases["work/x/y/sessions"] = "ref"
	if err := e.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	res, err = e.applyLegacy(legacyFlags(map[string]ids.Alias{}))
	if err != nil || res.Seed.Seeded || res.Seed.Reason != SeedHasBases || len(e.cfg.Profiles) != 2 {
		t.Fatalf("has_bases: %+v %v", res, err)
	}
	// With a canonical entry: has_canonical.
	e = newLegacyEnv(t)
	root = state.NewRoot(e.root)
	root.Canonical[model.RowSessions] = "ref"
	if err := e.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	res, err = e.applyLegacy(legacyFlags(nil))
	if err != nil || res.Seed.Reason != SeedHasCanonical || len(e.cfg.Profiles) != 2 {
		t.Fatalf("has_canonical: %+v %v", res, err)
	}
}

func TestLegacyStatusesAndSeedReasons(t *testing.T) {
	e := newLegacyEnv(t)
	e.legacyFolder("Claude-Old", lgH, lgHO, map[string]any{}) // duplicate of Claude-Home
	e.legacyFolder("Claude-NoID", "", "", map[string]any{})   // no identity
	e.legacyFolder("Claude-Skip", ids.AccountID(fakeUUID(0x401)), ids.OrgID(fakeUUID(0x402)), map[string]any{})
	e.legacyFolder("Claude-a.b", ids.AccountID(fakeUUID(0x501)), ids.OrgID(fakeUUID(0x502)), map[string]any{})
	e.legacyFolder("Claude-a_b", ids.AccountID(fakeUUID(0x601)), ids.OrgID(fakeUUID(0x602)), map[string]any{})
	e.legacyFolder("Claude-Work", ids.AccountID(fakeUUID(0x701)), ids.OrgID(fakeUUID(0x702)), map[string]any{})
	e.legacyFolder("Claude-3p", ids.AccountID(fakeUUID(0x801)), ids.OrgID(fakeUUID(0x802)), map[string]any{})
	opts := legacyFlags(map[string]ids.Alias{"Claude": "work", "Claude-Work": "w2"})
	opts.Exclude = []string{"Claude-Skip"}
	res := e.dry(opts)
	want := map[string]string{
		"Claude": legacy.ReasonParkCollision, "Claude-Home": legacy.ReasonDuplicate, "Claude-Old": legacy.ReasonDuplicate,
		"Claude-NoID": legacy.ReasonNoIdentity, "Claude-Skip": legacy.ReasonExcluded, "Claude-a.b": legacy.ReasonAliasCollision,
		"Claude-a_b": legacy.ReasonAliasCollision, "Claude-3p": legacy.ReasonReserved, "Claude-Work": "",
	}
	for name, reason := range want {
		if f := folderOf(res, name); f.Reason != reason {
			t.Errorf("%s: %+v, want %q", name, f, reason)
		}
	}
	if res.Seed.Reason != SeedDuplicateAccount && res.Seed.Reason != SeedTooFew {
		t.Errorf("seed %+v", res.Seed)
	}
	// Excluding the duplicate: seeding is possible again.
	opts = legacyFlags(nil)
	opts.Exclude = []string{"Claude-Old", "Claude-a.b", "Claude-Work", "Claude-3p"}
	res = e.dry(opts)
	if res.Seed.Reason == SeedDuplicateAccount || folderOf(res, "Claude-Home").Status != "adopt" || folderOf(res, "Claude-a_b").Alias != "a-b" {
		t.Fatalf("after excluding: %+v", res)
	}
	// A footprint of another tool: dry-run warning, apply exit 14.
	e.legacyFolder("Claude-Other", ids.AccountID(fakeUUID(0x901)), ids.OrgID(fakeUUID(0x902)), map[string]any{})
	res = e.dry(opts)
	if f := folderOf(res, "Claude-Other"); f.Reason != legacy.ReasonForeign || !hasPrefixed(res.Warnings, LegacyWarnForeign) {
		t.Fatalf("foreign: %+v %v", f, res.Warnings)
	}
	if _, err := e.applyLegacy(opts); pderr.CodeOf(err) != pderr.ForeignTool {
		t.Fatalf("apply with a foreign tool: %v", err)
	}
}

func TestLegacyHint(t *testing.T) {
	e := newLegacyEnv(t)
	hint := filepath.Join(t.TempDir(), "state.json")
	write := func(doc string) {
		if err := os.WriteFile(hint, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"active":"Labelx","in_default":"Labelx","layout_seeded":{}}`)
	opts := LegacyOptions{HintPath: hint}
	res, err := e.applyLegacy(opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Hint == nil || !res.Hint.Used || folderOf(res, "Claude").Alias != "labelx" || folderOf(res, "Claude").AliasSource != legacy.SourceHint {
		t.Fatalf("hint %+v %+v", res.Hint, folderOf(res, "Claude"))
	}
	if p, _ := e.cfg.Profile("labelx"); p.Folder != "Claude-Labelx" {
		t.Fatalf("config %+v", p)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "Labelx") || strings.Contains(string(b), hint) {
		t.Fatalf("the label or path leaked: %s", b)
	}
	// Stale: the label has a parked folder.
	e = newLegacyEnv(t)
	write(`{"in_default":"Home"}`)
	res = e.dry(LegacyOptions{HintPath: hint})
	if res.Hint.Used || !slices.Contains(res.Hint.Warnings, string(legacy.HintStale)) || folderOf(res, "Claude").Reason != legacy.ReasonNeedsAlias {
		t.Fatalf("stale %+v", res)
	}
	// Mid-switch: registration done, no seed.
	write(`{"active":"Home","in_default":"Work"}`)
	res, err = e.applyLegacy(LegacyOptions{HintPath: hint})
	if err != nil || res.Seed.Reason != SeedHintMidSwitch || len(e.cfg.Profiles) != 2 {
		t.Fatalf("mid-switch %+v %v", res, err)
	}
	// Malformed members: dropped.
	e = newLegacyEnv(t)
	write(`{"in_default":"a/b","active":7}`)
	res = e.dry(legacyFlags(nil))
	res = e.dry(LegacyOptions{HintPath: hint, Aliases: map[string]ids.Alias{"Claude": "work"}})
	if !slices.Contains(res.Hint.Warnings, string(legacy.HintLabel)) || !slices.Contains(res.Hint.Warnings, string(legacy.HintType)) {
		t.Fatalf("dropped %+v", res.Hint)
	}
	// A symlink: exit 2.
	link := hint + ".link"
	if err := os.Symlink(hint, link); err != nil {
		t.Fatal(err)
	}
	if _, err := e.orch().AdoptLegacy(context.Background(), LegacyOptions{HintPath: link}); pderr.CodeOf(err) != pderr.Usage {
		t.Fatalf("symlink: %v", err)
	}
}

func TestLegacyPlanHashChecks(t *testing.T) {
	e := newLegacyEnv(t)
	opts := legacyFlags(nil)
	res := e.dry(opts)
	apply := func(o LegacyOptions, hash string) error {
		o.Apply, o.PlanHash, o.Managed = true, hash, map[ids.Alias]bool{}
		_, err := e.orch().AdoptLegacy(context.Background(), o)
		return err
	}
	if err := apply(opts, ""); pderr.CodeOf(err) != pderr.Declined {
		t.Fatalf("no plan: %v", err)
	}
	if err := apply(opts, strings.Repeat("0", 32)+"."+strings.Repeat("0", 32)); pderr.CodeOf(err) != pderr.Declined ||
		!strings.Contains(err.Error(), "folders changed") {
		t.Fatalf("wrong hash: %v", err)
	}
	other := legacyFlags(map[string]ids.Alias{"Claude": "job"})
	if err := apply(other, res.PlanHash); pderr.CodeOf(err) != pderr.Declined || !strings.Contains(err.Error(), "flags differ") {
		t.Fatalf("other alias: %v", err)
	}
	// A run lock held elsewhere: 11; an open run: 41.
	e.st.lockErr = pderr.New(pderr.RunLockBusy, "lock", "busy")
	if err := apply(opts, res.PlanHash); pderr.CodeOf(err) != pderr.RunLockBusy {
		t.Fatalf("lock: %v", err)
	}
	e.st.lockErr = nil
	root := state.NewRoot(e.root)
	root.Run = &state.Run{ID: "r", Kind: state.RunSwitch}
	if err := e.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	if err := apply(opts, res.PlanHash); pderr.CodeOf(err) != pderr.NeedsRecover {
		t.Fatalf("open run: %v", err)
	}
	if r := e.dry(opts); r.Seed.Reason != SeedRunOpen || !slices.Contains(r.Warnings, LegacyWarnRunOpen) {
		t.Fatalf("dry run with an open run %+v", r)
	}
	e.st.committed = nil
	// A folder renamed between the dry run and the apply.
	if err := os.Rename(filepath.Join(e.root, "Claude-Home"), filepath.Join(e.root, "Claude-House")); err != nil {
		t.Fatal(err)
	}
	if err := apply(opts, res.PlanHash); pderr.CodeOf(err) != pderr.Declined || !strings.Contains(err.Error(), "folders changed") {
		t.Fatalf("renamed: %v", err)
	}
	if err := os.Rename(filepath.Join(e.root, "Claude-House"), filepath.Join(e.root, "Claude-Home")); err != nil {
		t.Fatal(err)
	}
	if err := apply(opts, res.PlanHash); err != nil {
		t.Fatalf("unchanged: %v", err)
	}
}

func TestLegacyIdempotent(t *testing.T) {
	e := newLegacyEnv(t)
	if _, err := e.applyLegacy(legacyFlags(nil)); err != nil {
		t.Fatal(err)
	}
	before, commits := string(e.st.committed), e.st.commits
	res, err := e.applyLegacy(legacyFlags(map[string]ids.Alias{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Folders {
		if f.Status != "registered" {
			t.Fatalf("second run: %+v", f)
		}
	}
	if string(e.st.committed) != before || e.st.commits != commits || len(e.cfg.Profiles) != 2 {
		t.Fatal("the second run changed the state")
	}
}

// failingCommits fails the n-th Commit (counting from 1).
type failingCommits struct {
	*fakeState
	n, calls int
}

func (f *failingCommits) Commit(r *state.Root) error {
	f.calls++
	if f.calls == f.n {
		return errors.New("injected commit failure")
	}
	return f.fakeState.Commit(r)
}

func TestLegacyCrashBetweenConfigAndRoot(t *testing.T) {
	e := newLegacyEnv(t)
	prev := e.tweak
	e.tweak = func(d *Deps) { prev(d); d.State = &failingCommits{fakeState: e.st, n: 1} }
	if _, err := e.applyLegacy(legacyFlags(nil)); pderr.CodeOf(err) != pderr.Internal {
		t.Fatalf("injected failure: %v", err)
	}
	if len(e.cfg.Profiles) != 2 || e.st.committed != nil {
		t.Fatalf("config %d, state %v", len(e.cfg.Profiles), e.st.committed != nil)
	}
	e.tweak = prev
	res := e.dry(LegacyOptions{})
	if folderOf(res, "Claude").Status != "finish" || folderOf(res, "Claude-Home").Status != "finish" {
		t.Fatalf("after the crash %+v", res.Folders)
	}
	if _, err := e.applyLegacy(LegacyOptions{}); err != nil {
		t.Fatal(err)
	}
	root := e.st.root(t)
	if root.Active != "work" || root.Profiles["home"] == nil || root.Profiles["home"].SeededFrom != state.SeededLegacy {
		t.Fatalf("not completed: %+v", root)
	}
	for _, a := range []ids.Alias{"work", "home"} {
		if b := e.resolveNow().Bindings[a]; !b.BaseValid {
			t.Fatalf("%s: %s", a, b.BaseInvalidWhy)
		}
	}
}

func TestLegacyOrgThroughResolver(t *testing.T) {
	// A parked folder with one real org and one stub: the real org is
	// chosen and the stub (under Claude's org) is listed.
	e := newLegacyEnv(t)
	e.put("Claude-Home/claude-code-sessions/"+string(lgH)+"/"+string(lgWO)+"/scheduled-tasks.json", `{"scheduledTasks":[]}`)
	e.put("Claude-Home/claude-code-sessions/"+string(lgH)+"/"+string(lgHO)+"/local_x.json", "{}")
	res, err := e.applyLegacy(legacyFlags(nil))
	if err != nil {
		t.Fatal(err)
	}
	home := folderOf(res, "Claude-Home")
	if home.Org != ids.Mask(string(lgHO)) || len(home.Leftovers) != 1 || home.Leftovers[0].Kind != string(legacy.KindMixedOrgStub) {
		t.Fatalf("home %+v", home)
	}
	for _, a := range []ids.Alias{"work", "home"} {
		if b := e.resolveNow().Bindings[a]; !b.BaseValid {
			t.Fatalf("%s: %s", a, b.BaseInvalidWhy)
		}
	}
	// Claude with one real org and one stub while the log names a third
	// org: org unknown, per-org rows not seeded, bases still valid.
	e = newLegacyEnv(t)
	e.put("Claude/claude-code-sessions/"+string(lgW)+"/"+string(lgHO)+"/scheduled-tasks.json", "")
	e.app.org = ids.OrgID(fakeUUID(0x999))
	res, err = e.applyLegacy(legacyFlags(nil))
	if err != nil {
		t.Fatal(err)
	}
	if f := folderOf(res, "Claude"); f.Org != "" {
		t.Fatalf("org %q", f.Org)
	}
	for _, r := range res.Seed.Rows {
		if r.Row == model.RowSessions && (r.Seeded || r.Reason != SeedOrgUnknown) {
			t.Fatalf("sessions seeded with an unknown org: %+v", r)
		}
	}
	for _, a := range []ids.Alias{"work", "home"} {
		if b := e.resolveNow().Bindings[a]; !b.BaseValid {
			t.Fatalf("%s: %s", a, b.BaseInvalidWhy)
		}
	}
	// Claude with the active_org hint among its orgs.
	e = newLegacyEnv(t)
	e.makeDir(filepath.Join(e.root, "Claude", "claude-code-sessions", string(lgW), string(lgXO)))
	e.put("Claude/claude-code-sessions/"+string(lgW)+"/"+string(lgXO)+"/local_y.json", "{}")
	e.app.org = lgWO
	res = e.dry(legacyFlags(nil))
	if f := folderOf(res, "Claude"); f.Org != ids.Mask(string(lgWO)) {
		t.Fatalf("hint org %+v", f)
	}
	// Ambiguous without a hint: unknown.
	e.app.org = ""
	res = e.dry(legacyFlags(nil))
	if f := folderOf(res, "Claude"); f.Org != "" {
		t.Fatalf("ambiguous org %+v", f)
	}
}

// quarantineEnv has leftovers of W inside Claude-Home: one eligible
// session directory, one with a session only it holds.
func quarantineEnv(t *testing.T) *env {
	e := newLegacyEnv(t)
	e.put("Claude/claude-code-sessions/"+string(lgW)+"/"+string(lgWO)+"/local_a.json", `{"a":1}`)
	e.put("Claude-Home/claude-code-sessions/"+string(lgW)+"/"+string(lgWO)+"/local_a.json", `{"a":1}`)
	e.put("Claude-Home/local-agent-mode-sessions/"+string(lgW)+"/"+string(lgWO)+"/only-here.json", `{}`)
	return e
}

func TestLegacyQuarantine(t *testing.T) {
	e := quarantineEnv(t)
	opts := legacyFlags(nil)
	opts.Quarantine = true
	res, err := e.applyLegacy(opts)
	if err != nil {
		t.Fatal(err)
	}
	home := folderOf(res, "Claude-Home")
	if len(home.Leftovers) != 2 || home.Leftovers[0].Action != ActionMoved || home.Leftovers[1].Why != legacy.WhyUniqueSessions ||
		res.Moves != 1 || len(res.Generations) != 1 {
		t.Fatalf("result %+v %+v", home, res)
	}
	n := res.Generations[0].N
	genDir := filepath.Join(filepath.Dir(e.root), "support", "generations")
	rel := "claude-code-sessions/" + string(lgW)
	if _, err := os.Stat(filepath.Join(genDir, fmt.Sprint(n), "quarantine", rel, string(lgWO), "local_a.json")); err != nil {
		t.Fatalf("not in the quarantine: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.root, "Claude-Home", rel)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("still in place")
	}
	gs := gen.Open(genDir, e.fs, e.clk)
	m, err := gs.Load(n)
	if err != nil || !m.Pinned || m.Purpose != gen.PurposeLegacyQuarantine {
		t.Fatalf("manifest %+v %v", m, err)
	}
	root := e.st.root(t)
	if g := root.Generations[len(root.Generations)-1]; g.N != n || g.Purpose != gen.PurposeLegacyQuarantine || g.Target != "home" || root.Run != nil {
		t.Fatalf("generation ref %+v", g)
	}
	if err := gs.Prune(gen.PrunePolicy{Keep: 1, MaxBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := gs.Load(n); err != nil {
		t.Fatal("the pinned generation was pruned")
	}
	// rollback n puts it back without quitting Claude (parked folder).
	e.app.running = true
	quits := e.app.quits
	epoch := root.Epoch
	bases := fmt.Sprint(root.Bases)
	e.ui.interactive, e.ui.answers[ConfirmRollback] = true, true
	rb, err := e.orch().Rollback(context.Background(), n, false)
	if err != nil || rb.Restored != 1 || len(rb.Conflicts) != 0 {
		t.Fatalf("rollback %+v %v", rb, err)
	}
	if e.app.quits != quits {
		t.Fatal("rollback of a parked quarantine quit Claude")
	}
	root = e.st.root(t)
	if root.Epoch != epoch || fmt.Sprint(root.Bases) != bases {
		t.Fatal("rollback of a quarantine changed the epoch or bases")
	}
	if _, err := os.Stat(filepath.Join(e.root, "Claude-Home", rel, string(lgWO), "local_a.json")); err != nil {
		t.Fatal("not put back")
	}
	if rb, err = e.orch().Rollback(context.Background(), n, false); err != nil || rb.Restored != 1 {
		t.Fatalf("second rollback %+v %v", rb, err)
	}
	// A bare rollback never picks the quarantine generation.
	if _, err := e.orch().Rollback(context.Background(), 0, false); pderr.CodeOf(err) != pderr.Usage {
		t.Fatalf("bare rollback: %v", err)
	}
}

func TestLegacyQuarantineRecreatedConflict(t *testing.T) {
	e := quarantineEnv(t)
	opts := legacyFlags(nil)
	opts.Quarantine = true
	res, err := e.applyLegacy(opts)
	if err != nil {
		t.Fatal(err)
	}
	e.put("Claude-Home/claude-code-sessions/"+string(lgW)+"/mine.json", "new")
	e.ui.interactive, e.ui.answers[ConfirmRollback] = true, true
	rb, err := e.orch().Rollback(context.Background(), res.Generations[0].N, false)
	if err != nil || len(rb.Conflicts) != 1 {
		t.Fatalf("rollback %+v %v", rb, err)
	}
	b, _ := os.ReadFile(filepath.Join(e.root, "Claude-Home", "claude-code-sessions", string(lgW), "mine.json"))
	if string(b) != "new" {
		t.Fatal("the recreated directory was overwritten")
	}
}

func TestLegacyQuarantineDeferred(t *testing.T) {
	// Leftovers inside Claude while the app runs: kept for now, exit 32.
	e := newLegacyEnv(t)
	e.put("Claude/claude-code-sessions/"+string(lgH)+"/"+string(lgHO)+"/local_h.json", "{}")
	e.put("Claude-Home/claude-code-sessions/"+string(lgH)+"/"+string(lgHO)+"/local_h.json", "{}")
	e.app.running = true
	opts := legacyFlags(nil)
	opts.Quarantine = true
	res, err := e.applyLegacy(opts)
	if pderr.CodeOf(err) != pderr.Degraded || res.MovesDeferred != 1 || res.Moves != 0 {
		t.Fatalf("app running: %+v %v", res, err)
	}
	if l := folderOf(res, "Claude").Leftovers[0]; l.Action != ActionDeferred || l.Why != legacy.WhyAppRunning {
		t.Fatalf("leftover %+v", l)
	}
	if len(e.st.root(t).Generations) != 0 {
		t.Fatal("a generation was recorded")
	}
	// A holder under a parked folder.
	e = quarantineEnv(t)
	e.app.inUse[filepath.Join(e.root, "Claude-Home")] = true
	res, err = e.applyLegacy(opts)
	if pderr.CodeOf(err) != pderr.Degraded || folderOf(res, "Claude-Home").Leftovers[0].Why != legacy.WhyInUse {
		t.Fatalf("holder: %+v %v", res, err)
	}
}

func TestLegacyQuarantineDenyFloor(t *testing.T) {
	e := quarantineEnv(t)
	m := legacyManifest()
	m.Legacy.ScopedDirs = append(m.Legacy.ScopedDirs, compat.LegacyDir{ID: "idb", Root: "IndexedDB", Layout: compat.LayoutAcctOrg, Quarantine: true})
	e.compat.m = m
	e.put("Claude-Home/IndexedDB/"+string(lgW)+"/x.ldb", "x")
	opts := legacyFlags(nil)
	opts.Quarantine = true
	res, err := e.applyLegacy(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range folderOf(res, "Claude-Home").Leftovers {
		if strings.HasPrefix(l.Path, "IndexedDB/") && (l.Action != ActionReportOnly || l.Why != legacy.WhyDenied) {
			t.Fatalf("deny floor %+v", l)
		}
	}
	if _, err := os.Stat(filepath.Join(e.root, "Claude-Home", "IndexedDB", string(lgW), "x.ldb")); err != nil {
		t.Fatal("a denied path was moved")
	}
}

func TestLegacyQuarantineIdentityRecheck(t *testing.T) {
	e := quarantineEnv(t)
	home := filepath.Join(e.root, "Claude-Home")
	swapped := false
	e.crash = func(p Phase) bool {
		if p == P7Apply && !swapped {
			swapped = true
			if err := os.Rename(home, home+"-moved"); err != nil {
				t.Fatal(err)
			}
			e.legacyFolder("Claude-Home", lgX, lgXO, map[string]any{})
		}
		return false
	}
	opts := legacyFlags(nil)
	opts.Quarantine = true
	res, err := e.applyLegacy(opts)
	if pderr.CodeOf(err) != pderr.Degraded || res.Moves != 0 || len(res.Generations) != 0 {
		t.Fatalf("result %+v %v", res, err)
	}
	if l := folderOf(res, "Claude-Home").Leftovers[0]; l.Action != ActionDeferred || l.Why != legacy.WhyFolderChanged {
		t.Fatalf("leftover %+v", l)
	}
	infos, _ := gen.Open(filepath.Join(filepath.Dir(e.root), "support", "generations"), e.fs, e.clk).List()
	if len(infos) != 0 {
		t.Fatalf("the empty generation was kept: %+v", infos)
	}
	if _, err := os.Stat(filepath.Join(home+"-moved", "claude-code-sessions", string(lgW))); err != nil {
		t.Fatal("something moved")
	}
}

// crashAt stops the run at the k-th crash point of phase p.
func crashAt(p Phase, k int) func(Phase) bool {
	n := 0
	return func(ph Phase) bool {
		if ph == p {
			n++
			return n == k
		}
		return false
	}
}

func TestLegacyQuarantineCrashes(t *testing.T) {
	opts := legacyFlags(nil)
	opts.Quarantine = true
	genDir := func(e *env) *gen.Store {
		return gen.Open(filepath.Join(filepath.Dir(e.root), "support", "generations"), e.fs, e.clk)
	}
	leftover := func(e *env) string {
		return filepath.Join(e.root, "Claude-Home", "claude-code-sessions", string(lgW))
	}
	cases := []struct {
		name  string
		crash func(Phase) bool
		moved bool
	}{
		{"after Begin, before Finish", crashAt(P6Backup, 2), false},
		{"after Finish, before the first move", crashAt(P7Apply, 1), false},
		{"after the first move", crashAt(P7Apply, 2), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := quarantineEnv(t)
			e.crash = tc.crash
			if _, err := e.applyLegacy(opts); !errors.Is(err, ErrCrashInjected) {
				t.Fatalf("no crash: %v", err)
			}
			e.crash = nil
			if e.st.root(t).Run == nil || e.st.root(t).Run.Kind != state.RunQuarantine {
				t.Fatal("no open quarantine run")
			}
			rr, err := e.orch().Recover(context.Background(), RecoverOptions{})
			if err != nil {
				t.Fatal(err)
			}
			root := e.st.root(t)
			infos, _ := genDir(e).List()
			_, statErr := os.Stat(leftover(e))
			switch {
			case root.Run != nil:
				t.Fatal("recover left the run open")
			case !tc.moved && (len(infos) != 0 || statErr != nil || len(root.Generations) != 0):
				t.Fatalf("nothing moved: gens %+v leftover %v refs %+v", infos, statErr, root.Generations)
			case tc.moved && (len(infos) != 1 || !errors.Is(statErr, fs.ErrNotExist) || len(root.Generations) != 1 ||
				root.Generations[0].Purpose != gen.PurposeLegacyQuarantine):
				t.Fatalf("moved: gens %+v refs %+v", infos, root.Generations)
			}
			if tc.moved {
				if !strings.Contains(strings.Join(i18n.Strings(i18n.English, rr.Actions), "\n"), "1 of 1") {
					t.Fatalf("actions %v", i18n.Strings(i18n.English, rr.Actions))
				}
				e.ui.interactive, e.ui.answers[ConfirmRollback] = true, true
				if _, err := e.orch().Rollback(context.Background(), root.Generations[0].N, false); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(leftover(e)); err != nil {
					t.Fatal("rollback did not restore it")
				}
			}
		})
	}
}

func TestLegacyBareRollbackPicksTheSwitch(t *testing.T) {
	e := quarantineEnv(t)
	if _, err := e.applyLegacy(legacyFlags(nil)); err != nil {
		t.Fatal(err)
	}
	sw, err := e.orch().Switch(context.Background(), yes("home"))
	if err != nil || sw.Generation == 0 {
		t.Fatalf("switch %+v %v", sw, err)
	}
	// Now quarantine W's leftovers in Claude-Home (parked again after
	// switching back? it is active now): put leftovers into Claude-Work.
	opts := LegacyOptions{Quarantine: true}
	res, err := e.applyLegacy(opts)
	if err != nil {
		t.Fatal(err)
	}
	root := e.st.root(t)
	if latestGeneration(root) != sw.Generation {
		t.Fatalf("latest generation %d, want the switch's %d (quarantine %+v)", latestGeneration(root), sw.Generation, res.Generations)
	}
}

// A canary in a leftover's session record and in the hint never reaches
// output, state or errors.
func TestLegacyCanary(t *testing.T) {
	const canary = "CANARY-7f3a"
	e := quarantineEnv(t)
	e.put("Claude-Home/claude-code-sessions/"+string(lgW)+"/"+string(lgWO)+"/local_c.json", `{"title":"`+canary+`"}`)
	hint := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(hint, []byte(`{"in_default":"`+canary+`","extra":"`+canary+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := LegacyOptions{HintPath: hint, Aliases: map[string]ids.Alias{"Claude": "work"}, Quarantine: true}
	res, err := e.applyLegacy(opts)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	all := string(b) + string(e.st.committed)
	for _, o := range e.st.objects {
		all += string(o)
	}
	for _, ev := range e.ui.events {
		all += ev.Message.English()
	}
	if strings.Contains(all, canary) {
		t.Fatal("the canary leaked")
	}
	write := func(doc string) {
		if err := os.WriteFile(hint, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`[` + canary)
	if _, err := e.orch().AdoptLegacy(context.Background(), opts); err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("error %v", err)
	}
}

// The policy forbids home-relative literals in the legacy code; the test
// manifest above names none either.
var _ = config.Default
