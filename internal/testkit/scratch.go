package testkit

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// ScratchRoot is the scratch root of the 7.x manifest.
const ScratchRoot = "scratch-workspaces"

// ScratchWorkspace creates <folder>/scratch-workspaces/<acct>/<org>/<leaf>
// with files (rel -> content), directories (rel ending in "/") and
// symlinks (content "->target"), every mtime set to mtime, and returns
// the workspace directory. Use FakeUUID values for acct and org and a
// leaf like "scratch-2026-01-02-0a0b0c".
func ScratchWorkspace(t testing.TB, folder string, acct, org, leaf string, entries map[string]string, mtime time.Time) string {
	t.Helper()
	ws := filepath.Join(folder, ScratchRoot, acct, org, leaf)
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	rels := make([]string, 0, len(entries))
	for rel := range entries {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	var dirs []string
	for _, rel := range rels {
		content := entries[rel]
		p := filepath.Join(ws, filepath.FromSlash(strings.TrimSuffix(rel, "/")))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.HasSuffix(rel, "/"):
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
		case strings.HasPrefix(content, "->"):
			if err := os.Symlink(strings.TrimPrefix(content, "->"), p); err != nil {
				t.Fatal(err)
			}
			continue
		default:
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	// Directory mtimes last, deepest first: creating entries moves them.
	_ = filepath.WalkDir(ws, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		if err := os.Chtimes(d, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

// ScratchKey returns the workspace key "<acct>/<org>/<leaf>".
func ScratchKey(acct, org, leaf string) string { return acct + "/" + org + "/" + leaf }
