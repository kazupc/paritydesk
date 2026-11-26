package compat

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

// validateLegacy checks the [legacy] section. The legacy-adopt unit owns
// its rules (spec-legacy-adopt 4.2).
func (m *Manifest) validateLegacy() error { return nil }
