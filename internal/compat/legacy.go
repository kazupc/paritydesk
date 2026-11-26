package compat

import (
	"fmt"
	"strings"

	"github.com/kazupc/paritydesk/internal/denylist"
)

// Legacy is the [legacy] section: layouts used by profile adopt
// --from-legacy to find other accounts' leftovers and to compare values.
// An absent section is valid (no detection, no projection).
type Legacy struct {
	ScopedDirs         []LegacyDir  `toml:"scoped_dir"`
	Stubs              []LegacyStub `toml:"stub"`
	IgnoreNames        []string     `toml:"ignore_names"`
	AgreeIgnoringRoles []string     `toml:"agree_ignoring_roles"`
}

// LegacyDir is one account-scoped directory that an earlier folder-swap
// script could leave in a folder of another account.
type LegacyDir struct {
	ID          string   `toml:"id"`
	Root        string   `toml:"root"`         // slash path under the data folder
	Layout      string   `toml:"layout"`       // "{acct}/{org}" | "{org}/{acct}"
	SkipNames   []string `toml:"skip_names"`   // first-level names to ignore
	Quarantine  bool     `toml:"quarantine"`   // may be moved into a generation
	UniqueCheck bool     `toml:"unique_check"` // count names missing in the owner folder
}

// LegacyStub is a file that an app writes empty for any scope; such a
// file alone does not make a directory a leftover.
type LegacyStub struct {
	File         string   `toml:"file"`          // single file name
	EmptyMembers []string `toml:"empty_members"` // must be [], {} or null when present
	FlagMembers  []string `toml:"flag_members"`  // must be booleans when present
}

// Legacy layouts.
const (
	LayoutAcctOrg = "{acct}/{org}"
	LayoutOrgAcct = "{org}/{acct}"
)

// fieldRoles are the row field roles a manifest may name in
// legacy.agree_ignoring_roles. They duplicate the role names of packages
// rows and sources, which compat cannot import (import cycle).
// TestFieldRolesCoverRoleConstants keeps the list complete.
var fieldRoles = map[string]bool{
	"per_field": true, "account_bound": true, "grant": true, "run_state": true, "resume_mark": true,
	"max_time": true, "parent": true, "worktree": true, "permission": true, "env_ref": true,
	"project_path": true, "name": true, "secret": true, "known_kinds": true, "session_ref": true,
	"worktree_path": true, "busy": true, "tombstone_time": true, "allow": true, "scratch_ref": true,
	"sets": true, "seed_keys": true, "group": true, "seed_only": true, "seed_from": true,
	"seed_section": true, "env_ref_prefix": true, "family_prefix": true, "project_prefix": true,
	"project_fixed": true, "project_fixed_prefix": true,
}

// singleName reports whether s is one path component.
func singleName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\x00")
}

// validateLegacy checks the [legacy] section (spec-legacy-adopt 4.2). A
// directory that may be quarantined must not itself be, and no path one
// level below it may be, covered by what the classification protects
// (never_touched, never_until_classified or synced, placeholders matched
// as one path component) or by the fixed deny floor, so an override can
// never make such a directory movable. Deeper protected paths (such as
// never_touched claude-code-sessions/*/*/scratchowed_*) cannot be ruled
// out by a root alone: LegacyProtected is checked for every path inside a
// leftover when it is planned and again before it moves, and a leftover
// holding one is never moved.
func (m *Manifest) validateLegacy() error {
	l := m.Legacy
	ids := map[string]bool{}
	for i, d := range l.ScopedDirs {
		at := fmt.Sprintf("legacy.scoped_dir[%d]", i)
		switch {
		case d.ID == "":
			return fmt.Errorf("%s: id is empty", at)
		case ids[d.ID]:
			return fmt.Errorf("%s: id %q is used twice", at, d.ID)
		case !relativeClean(d.Root) || strings.ContainsAny(d.Root, "*?[{}\\"):
			return fmt.Errorf("%s: root %q must be a clean relative path", at, d.Root)
		case d.Layout != LayoutAcctOrg && d.Layout != LayoutOrgAcct:
			return fmt.Errorf("%s: layout must be %q or %q", at, LayoutAcctOrg, LayoutOrgAcct)
		}
		ids[d.ID] = true
		for _, n := range d.SkipNames {
			if !singleName(n) {
				return fmt.Errorf("%s: skip_names entry %q is not a single name", at, n)
			}
		}
		if d.Quarantine {
			if g, ok := m.protectsLegacyRoot(d.Root); ok {
				return fmt.Errorf("%s: root %q cannot be quarantined: it is protected by %q", at, d.Root, g)
			}
		}
	}
	for i, s := range l.Stubs {
		if !singleName(s.File) {
			return fmt.Errorf("legacy.stub[%d]: file %q is not a single name", i, s.File)
		}
	}
	for _, n := range l.IgnoreNames {
		if !singleName(n) {
			return fmt.Errorf("legacy.ignore_names: %q is not a single name", n)
		}
	}
	for _, r := range l.AgreeIgnoringRoles {
		if !fieldRoles[r] {
			return fmt.Errorf("legacy.agree_ignoring_roles: unknown role %q", r)
		}
	}
	return nil
}

// protectsLegacyRoot reports the glob (or the deny floor) that covers root
// or a path one level below it.
func (m *Manifest) protectsLegacyRoot(root string) (string, bool) {
	c := m.Classification
	placeholders := strings.NewReplacer("{acct}", "*", "{org}", "*", "{session}", "*")
	var globs []string
	for _, l := range [][]string{c.NeverTouched, c.NeverUntilClassified, c.Synced} {
		globs = append(globs, l...)
	}
	for _, p := range []string{root, root + "/x"} {
		for _, g := range globs {
			if denylist.MatchGlob(placeholders.Replace(g), p) {
				return g, true
			}
		}
		if denylist.MatchPath(p) {
			return "the deny floor", true
		}
	}
	return "", false
}

// LegacyProtected reports whether a data-folder-relative slash path is one
// profile adopt --from-legacy never moves, also as part of a larger
// directory: the fixed deny floor, or a never_touched or
// never_until_classified glob (placeholders matched as one path
// component). Synced globs are not listed: they describe the folder's own
// account, which a leftover never is, and are checked per move against
// the folder's own replica.
func (m *Manifest) LegacyProtected(rel string) bool {
	if denylist.MatchPath(rel) {
		return true
	}
	c := m.Classification
	placeholders := strings.NewReplacer("{acct}", "*", "{org}", "*", "{session}", "*")
	for _, l := range [][]string{c.NeverTouched, c.NeverUntilClassified} {
		for _, g := range l {
			if denylist.MatchGlob(placeholders.Replace(g), rel) {
				return true
			}
		}
	}
	return false
}
