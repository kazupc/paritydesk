package compat

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/kazupc/paritydesk/internal/model"
)

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

// maxSweepDays bounds sweep_untouched_days (informational, for doctor).
const maxSweepDays = 365

// validateRowScratch checks a row's [row.scratch] table and compiles its
// leaf pattern (spec-scratch 5.3): the table is allowed only on row
// scratch and required there; root is one clean segment; leaf compiles
// and is anchored; exclude entries are relative, slash-separated globs
// without ".."; dependency_dirs and vcs_dirs are single folder names;
// sweep_untouched_days is 0..365. Errors name the key only.
func validateRowScratch(_ *Manifest, r RowSpec) error {
	if r.Row != model.RowScratch {
		if r.Scratch != nil {
			return fmt.Errorf("row %s: [row.scratch] is only allowed on row scratch", r.Row)
		}
		return nil
	}
	s := r.Scratch
	if s == nil {
		return errors.New("row scratch: [row.scratch] is required")
	}
	if !oneSegment(s.Root) {
		return errors.New("row scratch: scratch.root must be one folder name")
	}
	if !strings.HasPrefix(s.Leaf, "^") || !strings.HasSuffix(s.Leaf, "$") {
		return errors.New("row scratch: scratch.leaf must be anchored with ^ and $")
	}
	re, err := regexp.Compile(s.Leaf)
	if err != nil {
		return errors.New("row scratch: scratch.leaf does not compile")
	}
	for i, g := range s.Exclude {
		if !ScratchGlobOK(g) {
			return fmt.Errorf("row scratch: scratch.exclude[%d] is not a relative slash-separated glob without ..", i)
		}
	}
	for _, l := range []struct {
		key  string
		list []string
	}{{"dependency_dirs", s.DependencyDirs}, {"vcs_dirs", s.VCSDirs}} {
		for i, d := range l.list {
			if !oneSegment(d) {
				return fmt.Errorf("row scratch: scratch.%s[%d] must be one folder name", l.key, i)
			}
		}
	}
	if s.SweepUntouchedDays < 0 || s.SweepUntouchedDays > maxSweepDays {
		return fmt.Errorf("row scratch: scratch.sweep_untouched_days must be between 0 and %d", maxSweepDays)
	}
	s.leafRE = re
	return nil
}

// oneSegment reports whether s is one clean path segment.
func oneSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}

// ScratchGlobOK reports whether g is a usable scratch exclude glob: not
// empty, relative, slash-separated, with no "." or ".." segment and no
// backslash or NUL. Config validation applies the same rule.
func ScratchGlobOK(g string) bool {
	if g == "" || strings.HasPrefix(g, "/") || strings.ContainsAny(g, "\\\x00") {
		return false
	}
	for _, seg := range strings.Split(g, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// checkOverrideScratch refuses an override of base that weakens a
// [row.scratch] table: an override may add exclude, dependency_dirs and
// vcs_dirs entries but never remove them, and never change root or leaf
// (spec-scratch 5.3). An override without the row does not carry scratch
// workspaces at all, which weakens nothing.
func checkOverrideScratch(base, over *Manifest) error {
	b, ok := base.Row(model.RowScratch)
	if !ok || b.Scratch == nil {
		return nil
	}
	o, ok := over.Row(model.RowScratch)
	if !ok {
		return nil
	}
	if o.Scratch == nil {
		return errors.New("row scratch: [row.scratch] removed")
	}
	if o.Scratch.Root != b.Scratch.Root {
		return errors.New("row scratch: scratch.root changed")
	}
	if o.Scratch.Leaf != b.Scratch.Leaf {
		return errors.New("row scratch: scratch.leaf changed")
	}
	for _, l := range []struct {
		key        string
		base, next []string
	}{
		{"exclude", b.Scratch.Exclude, o.Scratch.Exclude},
		{"dependency_dirs", b.Scratch.DependencyDirs, o.Scratch.DependencyDirs},
		{"vcs_dirs", b.Scratch.VCSDirs, o.Scratch.VCSDirs},
	} {
		if missing := removedEntries(l.base, l.next); len(missing) > 0 {
			return fmt.Errorf("row scratch: scratch.%s entries removed: %s", l.key, strings.Join(missing, ", "))
		}
	}
	return nil
}
