package policy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// repoRoot returns the module root (the directory holding go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

var (
	filesOnce sync.Once
	filesList []string
	filesErr  error
)

// trackedFiles returns the repository's files relative to the root: every
// file git tracks plus every untracked file that .gitignore does not
// exclude (new files count before their first commit).
func trackedFiles(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	filesOnce.Do(func() {
		cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		out, err := cmd.Output()
		if err != nil {
			filesErr = err
			return
		}
		seen := map[string]bool{}
		for _, f := range strings.Split(string(out), "\x00") {
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			if fi, err := os.Lstat(filepath.Join(root, f)); err == nil && fi.Mode().IsRegular() {
				filesList = append(filesList, filepath.ToSlash(f))
			}
		}
	})
	if filesErr != nil {
		t.Skipf("git ls-files: %v", filesErr)
	}
	return filesList
}

// textFile is one readable text file.
type textFile struct {
	rel  string
	data []byte
}

// textFiles returns the tracked files that are UTF-8 text (no NUL byte),
// filtered by keep.
func textFiles(t *testing.T, keep func(rel string) bool) []textFile {
	t.Helper()
	root := repoRoot(t)
	var out []textFile
	for _, rel := range trackedFiles(t) {
		if keep != nil && !keep(rel) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
			continue
		}
		out = append(out, textFile{rel: rel, data: b})
	}
	return out
}

// underAny reports whether rel lies under one of the prefixes.
func underAny(rel string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// lineOf returns the 1-based line of byte offset off in b.
func lineOf(b []byte, off int) int { return bytes.Count(b[:off], []byte("\n")) + 1 }

// isFixture reports whether rel is test data or a test file.
func isFixture(rel string) bool {
	return strings.HasSuffix(rel, "_test.go") || strings.Contains("/"+rel, "/testdata/")
}

const policyDir = "internal/policy/"
