package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/pderr"
)

// TestCatalogAnchorsDocumented: every error-catalog entry links to an
// anchor that exists in docs/if-something-goes-wrong.md.
func TestCatalogAnchorsDocumented(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "if-something-goes-wrong.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, e := range pderr.Catalog() {
		if e.Anchor == "" {
			continue
		}
		if !strings.Contains(doc, `<a id="`+e.Anchor+`"></a>`) {
			t.Errorf("%s: anchor %q missing from docs/if-something-goes-wrong.md", pderr.ID(e.Code), e.Anchor)
		}
		if !strings.Contains(doc, "("+pderr.ID(e.Code)+")") {
			t.Errorf("%s has no section heading", pderr.ID(e.Code))
		}
	}
}

// TestDocsExist: the documents the README and the error messages link to
// are present.
func TestDocsExist(t *testing.T) {
	for _, rel := range []string{
		"README.md", "PRIVACY.md", "SECURITY.md", "DISCLAIMER.md", "CONTRIBUTING.md", "CHANGELOG.md",
		"docs/architecture.md", "docs/if-something-goes-wrong.md", "docs/release-checklist.md",
		"docs/adr/0003-online-reconcile.md",
		"docs/migration-from-legacy.md",
	} {
		if _, err := os.Stat(filepath.Join(repoRoot(t), filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
}
