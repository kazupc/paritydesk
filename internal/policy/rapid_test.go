package policy

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoRapidFailfiles: pgregory.net/rapid writes a failfile to
// <package>/testdata/rapid/<Test>/<Test>-<time>-<pid>.fail when a property
// fails. Those are debug debris: .gitignore must exclude them at any depth,
// and none may be tracked (a force-added one would still be caught here).
func TestNoRapidFailfiles(t *testing.T) {
	for _, rel := range trackedFiles(t) {
		if strings.Contains("/"+rel, "/testdata/rapid/") || strings.HasSuffix(rel, ".fail") {
			t.Errorf("%s is a rapid failfile; delete it instead of committing it", rel)
		}
	}
	root := repoRoot(t)
	for _, p := range []string{
		"internal/legacy/testdata/rapid/TestSeed/TestSeed-20260101000000-1.fail",
		"internal/app/testdata/rapid/TestX/TestX-20260101000000-1.fail",
		"testdata/rapid/TestX/TestX-20260101000000-1.fail",
	} {
		// check-ignore exits 0 when the path is ignored; the path need not exist.
		if err := exec.Command("git", "-C", root, "check-ignore", "-q", "--no-index", p).Run(); err != nil {
			t.Errorf(".gitignore does not exclude %s (git check-ignore: %v); keep the **/testdata/rapid/ rule", p, err)
		}
	}
	for _, p := range []string{"internal/legacy/testdata/keep.json"} {
		if exec.Command("git", "-C", root, "check-ignore", "-q", "--no-index", p).Run() == nil {
			t.Errorf(".gitignore excludes %s; the rapid rule must not hide other fixtures", p)
		}
	}
}
