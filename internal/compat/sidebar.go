package compat

import (
	"fmt"
	"slices"
)

// Field roles of the sidebar rows that the manifest validation checks
// (spec-sidebar-offline 3.3). The adapters and merge modules name the same
// roles in their own packages.
const (
	fieldSeedKeys    = "seed_keys"
	fieldSeedOnly    = "seed_only"
	fieldSeedFrom    = "seed_from"
	fieldSeedSection = "seed_section"
	fieldSets        = "sets"
)

// validateRowSidebar checks the field roles of the sidebar rows
// (seed_keys, seed_from, seed_section, seed_only, sets) against the row's
// keys and mirror paths. A row with seed_keys names one mirror path, one
// Local Storage key and one seed_from member per key, exactly one
// seed_section, and only seed keys in seed_only. A row with sets keeps
// them in exactly one Local Storage key.
func validateRowSidebar(m *Manifest, r RowSpec) error {
	if keys, ok := r.Fields[fieldSeedKeys]; ok {
		n := len(keys)
		if n == 0 || len(r.MirrorPaths) != n || len(r.LSKeys) != n || len(r.Fields[fieldSeedFrom]) != n {
			return fmt.Errorf("row %s: seed_keys, seed_from, mirror_paths and ls_keys differ in length", r.Row)
		}
		if len(r.Fields[fieldSeedSection]) != 1 {
			return fmt.Errorf("row %s: seed_section must name exactly one section", r.Row)
		}
		for _, k := range r.Fields[fieldSeedOnly] {
			if !slices.Contains(keys, k) {
				return fmt.Errorf("row %s: seed_only names a key not in seed_keys", r.Row)
			}
		}
	}
	if _, ok := r.Fields[fieldSets]; ok && len(r.LSKeys) != 1 {
		return fmt.Errorf("row %s: sets need exactly one ls_key", r.Row)
	}
	return nil
}
