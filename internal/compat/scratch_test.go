package compat_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/model"
)

func text7(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("manifests", "7.x.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestScratchRowIn7x(t *testing.T) {
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m6, m7 := ms[0], ms[1]
	if _, ok := m6.Row(model.RowScratch); ok {
		t.Error("6.x declares row scratch")
	}
	if !contains(m6.Classification.NeverUntilClassified, "scratch-workspaces/**") {
		t.Error("6.x no longer holds scratch-workspaces back")
	}
	sc, ok := m7.Row(model.RowScratch)
	if !ok || sc.WritePath != compat.PathOff || sc.Scratch == nil || sc.Scratch.Root != "scratch-workspaces" ||
		sc.Scratch.LeafRE() == nil || !sc.Scratch.LeafRE().MatchString("scratch-2026-01-02-0a0b0c") ||
		!contains(sc.Scratch.DependencyDirs, "node_modules") || !contains(sc.Scratch.Exclude, ".claude/settings.local.json") {
		t.Fatalf("7.x scratch row %+v", sc)
	}
	sess, _ := m7.Row(model.RowSessions)
	if !contains(sess.Fields["scratch_ref"], "cwd") || !contains(sess.Fields["scratch_ref"], "pendingFirstStart") {
		t.Errorf("scratch_ref %v", sess.Fields["scratch_ref"])
	}
	var probe compat.Probe
	for _, p := range m7.Probes {
		if p.ID == "scratch-root" {
			probe = p
		}
	}
	if probe.Kind != compat.ProbeDirOptional || probe.Row != model.RowScratch {
		t.Errorf("probe %+v", probe)
	}
	if contains(m7.Classification.NeverUntilClassified, "scratch-workspaces/**") || !contains(m7.Classification.Synced, "scratch-workspaces/**") ||
		!contains(m7.Classification.NeverTouched, "claude-code-sessions/*/*/scratchowed_*") {
		t.Error("7.x classification of scratch-workspaces")
	}
}

func TestDirOptionalProbe(t *testing.T) {
	m := &compat.Manifest{Name: "t", Probes: []compat.Probe{{ID: "p", Kind: compat.ProbeDirOptional, Target: "scratch-workspaces"}}}
	run := func(folder string) compat.ProbeResult {
		res, err := compat.RunProbes(context.Background(), m, compat.ProbeEnv{Folder: folder})
		if err != nil || len(res) != 1 {
			t.Fatalf("%v %v", res, err)
		}
		return res[0]
	}
	absent := t.TempDir()
	if r := run(absent); !r.OK {
		t.Errorf("absent: %+v", r)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "scratch-workspaces"), 0o700); err != nil {
		t.Fatal(err)
	}
	if r := run(dir); !r.OK {
		t.Errorf("dir: %+v", r)
	}
	file := t.TempDir()
	if err := os.WriteFile(filepath.Join(file, "scratch-workspaces"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := run(file); r.OK || r.Detail != "present but not a directory" {
		t.Errorf("file: %+v", r)
	}
	link := t.TempDir()
	if err := os.Symlink(dir, filepath.Join(link, "scratch-workspaces")); err != nil {
		t.Fatal(err)
	}
	if r := run(link); r.OK {
		t.Errorf("symlink: %+v", r)
	}
}

func TestScratchSpecValidation(t *testing.T) {
	base := text7(t)
	tests := []struct {
		name, old, new string
	}{
		{"root with a slash", `root = "scratch-workspaces"`, `root = "a/b"`},
		{"leaf not anchored", `leaf = '^scratch-[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9a-f]{6}$'`, `leaf = 'scratch-'`},
		{"leaf does not compile", `leaf = '^scratch-[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9a-f]{6}$'`, `leaf = '^scratch-($'`},
		{"exclude with ..", `exclude = [".DS_Store", "**/.DS_Store",`, `exclude = ["../x", "**/.DS_Store",`},
		{"exclude empty", `exclude = [".DS_Store", "**/.DS_Store",`, `exclude = ["", "**/.DS_Store",`},
		{"dependency dir with a slash", `dependency_dirs = ["node_modules", ".venv", "vendor"]`, `dependency_dirs = ["a/b"]`},
		{"vcs dir ..", `vcs_dirs = [".sl", ".jj", ".bzr", "_darcs", ".svn"]`, `vcs_dirs = [".."]`},
		{"sweep days", `sweep_untouched_days = 7`, `sweep_untouched_days = 400`},
		{"table missing", "[row.scratch]\n", "[row.scratchx]\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(base, tc.old) {
				t.Fatalf("fixture lacks %q", tc.old)
			}
			if _, err := compat.Decode([]byte(strings.Replace(base, tc.old, tc.new, 1)), "7.x.toml"); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// [row.scratch] on another row.
	bad := strings.Replace(base, "[[row]]\nrow = \"sessions\"\n", "[[row]]\nrow = \"sessions\"\n[row.scratch]\nroot = \"x\"\nleaf = '^x$'\n", 1)
	if _, err := compat.Decode([]byte(bad), "7.x.toml"); err == nil {
		t.Error("[row.scratch] accepted on row sessions")
	}
}

func TestScratchOverrides(t *testing.T) {
	base := text7(t)
	for _, tc := range []struct {
		name string
		text string
		ok   bool
	}{
		{"adds an exclude", strings.Replace(base, `exclude = [".DS_Store",`, `exclude = ["extra/**", ".DS_Store",`, 1), true},
		{"removes an exclude", strings.Replace(base, `".claude/settings.local.json", `, ``, 1), false},
		{"removes a dependency dir", strings.Replace(base, `"node_modules", `, ``, 1), false},
		{"changes the leaf", strings.Replace(base, `[0-9a-f]{6}$'`, `[0-9a-z]{6}$'`, 1), false},
		{"changes the root", strings.Replace(base, `root = "scratch-workspaces"`, `root = "elsewhere"`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeOverride(t, dir, "7.x.toml", tc.text)
			_, err := compat.Load(dir)
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v err=%v", tc.ok, err)
			}
		})
	}
}
