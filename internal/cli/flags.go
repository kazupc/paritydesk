package cli

import (
	"flag"
	"slices"
	"strconv"
	"strings"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/orchestrator"
)

// value returns a registered flag's value; it panics on a programming
// error (a name that the command did not register).
func value(fs *flag.FlagSet, name string) any {
	f := fs.Lookup(name)
	if f == nil {
		panic("cli: flag not registered: " + name)
	}
	return f.Value.(flag.Getter).Get()
}

func getBool(fs *flag.FlagSet, name string) bool     { return value(fs, name).(bool) }
func getString(fs *flag.FlagSet, name string) string { return value(fs, name).(string) }
func getInt(fs *flag.FlagSet, name string) int       { return value(fs, name).(int) }
func getInt64(fs *flag.FlagSet, name string) int64   { return value(fs, name).(int64) }
func getUint64(fs *flag.FlagSet, name string) uint64 { return value(fs, name).(uint64) }

// categoryNames lists the --only and --skip category names.
func categoryNames() []string {
	out := make([]string, len(model.AllCategories))
	for i, c := range model.AllCategories {
		out[i] = string(c)
	}
	return out
}

// parseCategories parses a comma-separated category list. Empty means
// none; unknown names and duplicates are usage errors.
func parseCategories(flagName, s string) ([]model.Category, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []model.Category
	for _, part := range strings.Split(s, ",") {
		c := model.Category(strings.TrimSpace(part))
		if c == model.CatArtifacts {
			return nil, usageErrorf("--%s: artifacts are synced by /paritydesk:artifacts, not by switch", flagName)
		}
		if !slices.Contains(model.AllCategories, c) {
			return nil, usageErrorf("--%s: unknown category %q (one of %s)", flagName, string(c), strings.Join(categoryNames(), ","))
		}
		if slices.Contains(out, c) {
			return nil, usageErrorf("--%s: category %q given twice", flagName, string(c))
		}
		out = append(out, c)
	}
	return out, nil
}

// categoryOptions reads --only and --skip; a category in both is an error.
func categoryOptions(fs *flag.FlagSet) (only, skip []model.Category, err error) {
	if only, err = parseCategories(orchestrator.FlagOnly, getString(fs, orchestrator.FlagOnly)); err != nil {
		return nil, nil, err
	}
	if skip, err = parseCategories(orchestrator.FlagSkip, getString(fs, orchestrator.FlagSkip)); err != nil {
		return nil, nil, err
	}
	for _, c := range only {
		if slices.Contains(skip, c) {
			return nil, nil, usageErrorf("category %q is in both --only and --skip", string(c))
		}
	}
	// Scratch workspaces follow the sessions that name them.
	for _, c := range only {
		if imp := model.ImpliedBy(c); imp != "" && !slices.Contains(only, imp) {
			return nil, nil, usageErrorf("%s", i18n.T(i18n.CliOnlyScratch))
		}
	}
	return only, skip, nil
}

// parseAlias validates a profile alias argument.
func parseAlias(s string) (ids.Alias, error) {
	a, err := ids.ParseAlias(s)
	if err != nil {
		return "", usageErrorf("%q is not a profile alias (a-z, 0-9 and -, starting with a letter or digit)", s)
	}
	return a, nil
}

// parseTarget reads the optional target alias: none or "-" means the
// previous profile (the empty alias).
func parseTarget(args []string) (ids.Alias, error) {
	if len(args) == 0 || args[0] == "-" {
		return "", nil
	}
	return parseAlias(args[0])
}

// parseGeneration reads a generation number argument; none means 0 (the
// latest).
func parseGeneration(args []string) (uint64, error) {
	if len(args) == 0 {
		return 0, nil
	}
	n, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil {
		return 0, usageErrorf("%q is not a generation number", args[0])
	}
	return n, nil
}

// switchOptions maps the switch flags to orchestrator.SwitchOptions.
func switchOptions(fs *flag.FlagSet, args []string) (orchestrator.SwitchOptions, error) {
	to, err := parseTarget(args)
	if err != nil {
		return orchestrator.SwitchOptions{}, err
	}
	only, skip, err := categoryOptions(fs)
	if err != nil {
		return orchestrator.SwitchOptions{}, err
	}
	o := orchestrator.SwitchOptions{
		To:                 to,
		Yes:                getBool(fs, orchestrator.FlagYes),
		Only:               only,
		Skip:               skip,
		NoLaunch:           getBool(fs, orchestrator.FlagNoLaunch),
		NoReopen:           getBool(fs, orchestrator.FlagNoReopen),
		Detach:             getBool(fs, orchestrator.FlagDetach),
		FromRunner:         getBool(fs, orchestrator.FlagFromRunner),
		ReopenCLISession:   getString(fs, orchestrator.FlagReopenSession),
		AllowDeletions:     getInt(fs, orchestrator.FlagAllowDeletions),
		AcceptWorktreeRisk: getBool(fs, orchestrator.FlagAcceptWorktreeRisk),
		LogOffset:          getInt64(fs, orchestrator.FlagLogOffset),
		LogInode:           getUint64(fs, orchestrator.FlagLogInode),
		ConfirmedPlan:      getString(fs, orchestrator.FlagConfirmedPlan),
		ConfirmFirstSync:   getBool(fs, orchestrator.FlagConfirmFirstSync),
	}
	// An empty --reopen-session means no reopen; anything else must be a
	// Claude Code session id (a UUID).
	if o.ReopenCLISession != "" && !ids.IsUUID(o.ReopenCLISession) {
		return orchestrator.SwitchOptions{}, usageErrorf("--%s must be a session UUID", orchestrator.FlagReopenSession)
	}
	if o.AllowDeletions < 0 {
		return orchestrator.SwitchOptions{}, usageErrorf("--%s must not be negative", orchestrator.FlagAllowDeletions)
	}
	if o.LogOffset < -1 {
		return orchestrator.SwitchOptions{}, usageErrorf("--%s must not be negative", orchestrator.FlagLogOffset)
	}
	if o.ConfirmedPlan != "" && !isHexHash(o.ConfirmedPlan) {
		return orchestrator.SwitchOptions{}, usageErrorf("--%s must be the plan_hash printed by paritydesk plan --json", orchestrator.FlagConfirmedPlan)
	}
	return o, nil
}

// isHexHash reports whether s is a full hex sha256 digest (64 lower-case
// hex digits), the only form the plan store accepts.
func isHexHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// multiFlag is a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

// Set appends one value.
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// Get returns the values (flag.Getter).
func (m *multiFlag) Get() any { return []string(*m) }

func getStrings(fs *flag.FlagSet, name string) []string { return value(fs, name).([]string) }
