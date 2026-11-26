package all_test

import (
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/testkit"
)

// carried reports whether the manifest carries row (a write path other
// than "none"). sidebar-seed is declared "none" on 6.x, the manifest of
// testkit.TestedVersion, so it has nothing to read there.
func carried(m *compat.Manifest, row model.RowID) bool {
	s, ok := m.Row(row)
	return ok && s.WritePath != compat.PathNone
}

// richV02 is testkit.Rich plus collapsed groups and fork families that
// differ between the variants (testkit.Rich itself keeps the v0.1 values,
// so existing tree hashes do not change).
func richV02(name string, variant int) testkit.FolderSpec {
	spec := testkit.Rich(name, variant)
	if variant%2 == 0 {
		spec.CollapsedGroups = []string{"recents", "project-/tmp/project"}
		spec.CollapsedFamilies = []string{"code:" + testkit.FakeSession(3)}
	} else {
		spec.CollapsedGroups = []string{"project-/tmp/other", "pinned", "project-/tmp/other"}
		spec.CollapsedFamilies = []string{}
	}
	spec.OpenedGroups = []string{"project-/tmp/third"}
	return spec
}
