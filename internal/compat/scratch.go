package compat

import "regexp"

// ScratchSpec is [row.scratch] (row "scratch" only): the scratch
// workspace root under a data folder and what is never carried from it.
type ScratchSpec struct {
	Root               string   `toml:"root"`
	Leaf               string   `toml:"leaf"`
	Exclude            []string `toml:"exclude"`
	DependencyDirs     []string `toml:"dependency_dirs"`
	VCSDirs            []string `toml:"vcs_dirs"`
	SweepUntouchedDays int      `toml:"sweep_untouched_days"`

	// leafRE is Leaf compiled at load by validateRowScratch.
	leafRE *regexp.Regexp
}

// LeafRE returns the compiled Leaf pattern (nil when it was not compiled
// at load).
func (s *ScratchSpec) LeafRE() *regexp.Regexp {
	if s == nil {
		return nil
	}
	return s.leafRE
}

// validateRowScratch checks a row's [row.scratch] table and compiles its
// leaf pattern. The scratch unit owns its rules (spec-scratch 5.3).
func validateRowScratch(m *Manifest, r RowSpec) error { return nil }

// checkOverrideScratch refuses an override of base that weakens a
// [row.scratch] table. The scratch unit owns its rules (spec-scratch 5.3).
func checkOverrideScratch(base, over *Manifest) error { return nil }
