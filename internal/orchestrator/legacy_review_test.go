package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/legacy"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// commandOptions parses the command a dry run prints into the apply
// options it stands for.
func commandOptions(t *testing.T, cmd string) LegacyOptions {
	t.Helper()
	parts := strings.Fields(cmd)
	o := LegacyOptions{Apply: true, Aliases: map[string]ids.Alias{}, Managed: map[ids.Alias]bool{}}
	for i := 0; i < len(parts); i++ {
		switch parts[i] {
		case "--legacy-alias":
			f, a, _ := strings.Cut(parts[i+1], "=")
			o.Aliases[f] = ids.Alias(a)
			i++
		case "--legacy-exclude":
			o.Exclude = append(o.Exclude, parts[i+1])
			i++
		case "--legacy-no-seed":
			o.NoSeed = true
		case "--legacy-plan":
			o.PlanHash = parts[i+1]
			i++
		case "--legacy-state", "--quarantine":
			t.Fatalf("the printed command carries %s: %s", parts[i], cmd)
		}
	}
	return o
}

func (e *env) writeHint(doc string) string {
	e.t.Helper()
	p := filepath.Join(e.t.TempDir(), "state.json")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// A hint saying the earlier switch did not finish refuses seeding; the
// printed command (without the hint) says --legacy-no-seed, so applying it
// seeds nothing, and leaving that flag out is a flags difference.
func TestLegacyPrintedCommandAfterMidSwitchHint(t *testing.T) {
	e := newLegacyEnv(t)
	hint := e.writeHint(`{"active":"Other","in_default":"Work"}`)
	res := e.dry(LegacyOptions{HintPath: hint})
	if res.Seed.Reason != SeedHintMidSwitch || !strings.Contains(res.ApplyCommand, "--legacy-no-seed") {
		t.Fatalf("dry run %+v command %q", res.Seed, res.ApplyCommand)
	}
	opts := commandOptions(t, res.ApplyCommand)
	without := opts
	without.NoSeed = false
	if _, err := e.orch().AdoptLegacy(context.Background(), without); pderr.CodeOf(err) != pderr.Declined ||
		!strings.Contains(err.Error(), "flags differ") {
		t.Fatalf("without --legacy-no-seed: %v", err)
	}
	out, err := e.orch().AdoptLegacy(context.Background(), opts)
	if err != nil {
		t.Fatalf("printed command: %v", err)
	}
	if out.Seed.Seeded || len(e.st.root(t).Bases) != 0 || len(e.cfg.Profiles) != 2 {
		t.Fatalf("seeded after a mid-switch hint: %+v bases %d", out.Seed, len(e.st.root(t).Bases))
	}
}

// A consistent hint gives Claude the earlier script's parked name. The
// printed command cannot carry it: its hash is the plan it really makes,
// the dry run says so, and applying it works. With the hint (Terminal) the
// name is kept.
func TestLegacyPrintedCommandWithHintParkedName(t *testing.T) {
	e := newLegacyEnv(t)
	hint := e.writeHint(`{"active":"Work","in_default":"Work"}`)
	res := e.dry(LegacyOptions{HintPath: hint})
	if !slices.Contains(res.Warnings, LegacyWarnPrintedDiffers) || strings.Contains(res.ApplyCommand, "Work") ||
		!strings.Contains(res.ApplyCommand, "--legacy-alias Claude=work") {
		t.Fatalf("dry run warnings %v command %q", res.Warnings, res.ApplyCommand)
	}
	if legacy.CompareHash(commandOptions(t, res.ApplyCommand).PlanHash, res.PlanHash) != legacy.HashInputsDiffer {
		t.Fatal("the printed hash should differ from the hint plan's in the inputs half only")
	}
	if _, err := e.orch().AdoptLegacy(context.Background(), commandOptions(t, res.ApplyCommand)); err != nil {
		t.Fatalf("printed command: %v", err)
	}
	if p, _ := e.cfg.Profile("work"); p.Folder != "" {
		t.Fatalf("printed command kept a parked name it never showed: %+v", p)
	}
	// In Terminal with the hint: the parked name is kept.
	e = newLegacyEnv(t)
	if _, err := e.applyLegacy(LegacyOptions{HintPath: hint}); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.cfg.Profile("work"); p.Folder != "Claude-Work" {
		t.Fatalf("hint parked name lost: %+v", p)
	}
	// Without a hint the printed command is the plan's own: no warning.
	e = newLegacyEnv(t)
	if r := e.dry(legacyFlags(nil)); slices.Contains(r.Warnings, LegacyWarnPrintedDiffers) {
		t.Fatalf("warning without a hint: %v", r.Warnings)
	}
}

// Flag aliases that end in a collision are still printed, so the printed
// command reproduces the plan.
func TestLegacyPrintedCommandKeepsCollidingAliases(t *testing.T) {
	e := newLegacyEnv(t)
	e.legacyFolder("Claude-Work", lgX, lgXO, map[string]any{})
	opts := legacyFlags(map[string]ids.Alias{"Claude": "main", "Claude-Work": "home"})
	res := e.dry(opts)
	if folderOf(res, "Claude-Home").Reason != legacy.ReasonAliasCollision || folderOf(res, "Claude-Work").Reason != legacy.ReasonAliasCollision {
		t.Fatalf("collisions %+v", res.Folders)
	}
	if !strings.Contains(res.ApplyCommand, "--legacy-alias Claude-Work=home") {
		t.Fatalf("command %q", res.ApplyCommand)
	}
	if _, err := e.orch().AdoptLegacy(context.Background(), commandOptions(t, res.ApplyCommand)); err != nil {
		t.Fatalf("printed command: %v", err)
	}
}

// --quarantine is part of the inputs half: the printed hash differs from a
// --quarantine dry run's only there, so the flags text applies.
func TestLegacyQuarantineHashIsAFlagsDifference(t *testing.T) {
	e := quarantineEnv(t)
	res := e.dry(legacyFlags(nil))
	q := legacyFlags(nil)
	q.Quarantine = true
	resQ := e.dry(q)
	if legacy.CompareHash(commandOptions(t, res.ApplyCommand).PlanHash, resQ.PlanHash) != legacy.HashInputsDiffer {
		t.Fatal("quarantine changed the folders half")
	}
	q.Apply, q.PlanHash, q.Managed = true, res.PlanHash, map[ids.Alias]bool{}
	if _, err := e.orch().AdoptLegacy(context.Background(), q); pderr.CodeOf(err) != pderr.Declined || !strings.Contains(err.Error(), "flags differ") {
		t.Fatalf("apply: %v", err)
	}
}

// An exclude added after the dry run is a flags difference, not "the
// folders changed".
func TestLegacyExcludeIsFlagsDifference(t *testing.T) {
	e := quarantineEnv(t)
	res := e.dry(legacyFlags(nil))
	other := legacyFlags(nil)
	other.Exclude = []string{"Claude-Home"}
	resX := e.dry(other)
	fa, _, _ := legacy.SplitHash(res.PlanHash)
	fb, _, _ := legacy.SplitHash(resX.PlanHash)
	if fa != fb {
		t.Fatal("an exclude changed the folders half")
	}
	if len(folderOf(resX, "Claude-Home").Leftovers) != 0 {
		t.Fatal("an excluded folder reports leftovers")
	}
	other.Apply, other.PlanHash, other.Managed = true, res.PlanHash, map[ids.Alias]bool{}
	if _, err := e.orch().AdoptLegacy(context.Background(), other); pderr.CodeOf(err) != pderr.Declined || !strings.Contains(err.Error(), "flags differ") {
		t.Fatalf("exclude after the dry run: %v", err)
	}
}

// With a stale duplicate of W (Claude-Old, excluded), a W leftover in
// Claude-Home is compared with Claude, which will own W: a session only
// the stale copy also has is unique, so nothing moves.
func TestLegacyUniqueAgainstTheOwnerNotADuplicate(t *testing.T) {
	e := newLegacyEnv(t)
	e.legacyFolder("Claude-Old", lgW, lgWO, map[string]any{})
	s := "local_" + fakeUUID(0x555) + ".json"
	e.put("Claude-Home/claude-code-sessions/"+string(lgW)+"/"+string(lgWO)+"/"+s, "{}")
	e.put("Claude-Old/claude-code-sessions/"+string(lgW)+"/"+string(lgWO)+"/"+s, "{}")
	opts := legacyFlags(nil)
	opts.Exclude = []string{"Claude-Old"}
	res := e.dry(opts)
	home := folderOf(res, "Claude-Home")
	if len(home.Leftovers) != 1 {
		t.Fatalf("leftovers %+v", home.Leftovers)
	}
	l := home.Leftovers[0]
	if l.Owner != "work" || l.OwnerFolder != "Claude" || l.Unique != 1 || l.Action != ActionReportOnly || l.Why != legacy.WhyUniqueSessions {
		t.Fatalf("leftover %+v", l)
	}
}

// Q0 computes the leftovers again before anything moves: a session that
// appears in a leftover after the plan (here while the markers are
// written) keeps it in place.
func TestLegacyQuarantineRecomputedBeforeMoves(t *testing.T) {
	e := quarantineEnv(t)
	prev := e.tweak
	e.tweak = func(d *Deps) {
		prev(d)
		write := d.WriteMarker
		d.WriteMarker = func(fs fsx.Ops, folder string, m model.Marker) error {
			e.put("Claude-Home/claude-code-sessions/"+string(lgW)+"/"+string(lgWO)+"/local_new.json", "{}")
			return write(fs, folder, m)
		}
	}
	opts := legacyFlags(nil)
	opts.Quarantine = true
	res, err := e.applyLegacy(opts)
	if pderr.CodeOf(err) != pderr.Degraded || res.Moves != 0 {
		t.Fatalf("result %+v %v", res, err)
	}
	if l := folderOf(res, "Claude-Home").Leftovers[0]; l.Action != ActionDeferred || l.Why != legacy.WhyFolderChanged {
		t.Fatalf("leftover %+v", l)
	}
	if _, err := os.Stat(filepath.Join(e.root, "Claude-Home", "claude-code-sessions", string(lgW), string(lgWO), "local_new.json")); err != nil {
		t.Fatal("moved after it changed")
	}
}

// SaveTree refusing one item (it is no longer a directory) keeps that item
// in place and stops nothing else: no internal error after registration.
func TestLegacyQuarantineSaveTreeRefusal(t *testing.T) {
	e := quarantineEnv(t)
	dir := filepath.Join(e.root, "Claude-Home", "claude-code-sessions", string(lgW))
	swapped := false
	e.crash = func(p Phase) bool {
		if p == P6Backup && !swapped {
			swapped = true
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dir, []byte("now a file"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return false
	}
	opts := legacyFlags(nil)
	opts.Quarantine = true
	res, err := e.applyLegacy(opts)
	if pderr.CodeOf(err) != pderr.Degraded || res.MovesDeferred != 1 || len(res.Generations) != 0 {
		t.Fatalf("result %+v %v", res, err)
	}
	if l := folderOf(res, "Claude-Home").Leftovers[0]; l.Action != ActionDeferred || l.Why != legacy.WhyFolderChanged {
		t.Fatalf("leftover %+v", l)
	}
	root := e.st.root(t)
	if root.Run != nil || len(root.Generations) != 0 || root.Profiles["home"] == nil {
		t.Fatalf("root %+v", root)
	}
}

// A failed rename marks that item failed; the later ones are kept for now
// with their own reason (not "the folder changed").
func TestLegacyQuarantineMoveFailure(t *testing.T) {
	e := quarantineEnv(t)
	e.put("Claude/local-agent-mode-sessions/"+string(lgW)+"/"+string(lgWO)+"/s.json", "{}")
	e.put("Claude-Home/local-agent-mode-sessions/"+string(lgW)+"/"+string(lgWO)+"/s.json", "{}")
	if err := os.Remove(filepath.Join(e.root, "Claude-Home", "local-agent-mode-sessions", string(lgW), string(lgWO), "only-here.json")); err != nil {
		t.Fatal(err)
	}
	e.fs.Fail("MoveInto", errors.New("injected rename failure"))
	opts := legacyFlags(nil)
	opts.Quarantine = true
	res, err := e.applyLegacy(opts)
	if pderr.CodeOf(err) != pderr.Degraded || res.MovesFailed != 1 || res.MovesDeferred != 1 {
		t.Fatalf("result %+v %v", res, err)
	}
	left := folderOf(res, "Claude-Home").Leftovers
	if left[0].Action != ActionFailed || left[1].Action != ActionDeferred || left[1].Why != legacy.WhyMoveFailed {
		t.Fatalf("leftovers %+v", left)
	}
}

// A leftover holding a path paritydesk never touches (the app's
// owed-removal notes) is reported, never moved.
func TestLegacyProtectedPathInsideLeftover(t *testing.T) {
	e := quarantineEnv(t)
	m := legacyManifest()
	m.Classification.NeverTouched = append(m.Classification.NeverTouched, "claude-code-sessions/*/*/scratchowed_*")
	e.compat.m = m
	e.put("Claude-Home/claude-code-sessions/"+string(lgW)+"/"+string(lgWO)+"/scratchowed_x", "1")
	e.put("Claude/claude-code-sessions/"+string(lgW)+"/"+string(lgWO)+"/scratchowed_x", "1")
	opts := legacyFlags(nil)
	opts.Quarantine = true
	res, err := e.applyLegacy(opts)
	if err != nil {
		t.Fatal(err)
	}
	if l := folderOf(res, "Claude-Home").Leftovers[0]; l.Action != ActionReportOnly || l.Why != legacy.WhyDenied {
		t.Fatalf("leftover %+v", l)
	}
	if _, err := os.Stat(filepath.Join(e.root, "Claude-Home", "claude-code-sessions", string(lgW), string(lgWO), "scratchowed_x")); err != nil {
		t.Fatal("a protected path was moved")
	}
}

// Rolling back a quarantine of leftovers inside Claude quits the running
// app as any rollback into the active folder does, and still records no
// epoch event and changes no base.
func TestLegacyRollbackOfClaudeQuarantineQuits(t *testing.T) {
	e := newLegacyEnv(t)
	e.put("Claude/claude-code-sessions/"+string(lgH)+"/"+string(lgHO)+"/local_h.json", "{}")
	e.put("Claude-Home/claude-code-sessions/"+string(lgH)+"/"+string(lgHO)+"/local_h.json", "{}")
	opts := legacyFlags(nil)
	opts.Quarantine = true
	res, err := e.applyLegacy(opts)
	if err != nil || len(res.Generations) != 1 || res.Generations[0].Folder != ids.DefaultFolder {
		t.Fatalf("quarantine %+v %v", res, err)
	}
	n := res.Generations[0].N
	root := e.st.root(t)
	epoch, bases := root.Epoch, len(root.Bases)
	e.app.running = true
	quits := e.app.quits
	e.ui.interactive, e.ui.answers[ConfirmRollback] = true, true
	rb, err := e.orch().Rollback(context.Background(), n, false)
	if err != nil || rb.Restored != 1 {
		t.Fatalf("rollback %+v %v", rb, err)
	}
	if e.app.quits == quits {
		t.Fatal("rollback into Claude did not quit the running app")
	}
	root = e.st.root(t)
	if root.Epoch != epoch || len(root.Bases) != bases {
		t.Fatal("rollback of a quarantine changed the epoch or the bases")
	}
	if _, err := os.Stat(filepath.Join(e.root, "Claude", "claude-code-sessions", string(lgH), string(lgHO), "local_h.json")); err != nil {
		t.Fatal("not put back")
	}
}

// snapshotCounter counts Local Storage snapshot opens.
type snapshotCounter struct {
	kv.Opener
	n int
}

func (c *snapshotCounter) OpenSnapshot(ctx context.Context, dir string) (kv.Reader, error) {
	c.n++
	return c.Opener.OpenSnapshot(ctx, dir)
}

// Seeding opens one Local Storage snapshot per participant, shared by its
// probes and every row.
func TestLegacySeedOneSnapshotPerFolder(t *testing.T) {
	e := newLegacyEnv(t)
	for _, n := range []string{ids.DefaultFolder, "Claude-Home"} {
		dir := sources.LevelDBPath(filepath.Join(e.root, n))
		e.makeDir(dir)
		e.mem.Put(dir, kv.NewMem())
	}
	c := &snapshotCounter{}
	prev := e.tweak
	e.tweak = func(d *Deps) { prev(d); c.Opener = d.KV; d.KV = c }
	res := e.dry(legacyFlags(nil))
	if !res.Seed.Seeded || c.n != 2 {
		t.Fatalf("seeded %v with %d snapshots, want 2", res.Seed.Seeded, c.n)
	}
}
