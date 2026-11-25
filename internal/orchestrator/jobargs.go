package orchestrator

// This file is part of the frozen contract between the orchestrator and
// cli: the detached job's argv is built here, and cli registers the
// switch flags with these names, so no flag name is spelled twice. A cli
// test parses JobArgs output back into SwitchOptions.

import (
	"strconv"
	"strings"
)

// Switch flag names.
const (
	FlagYes                = "yes"
	FlagOnly               = "only"
	FlagSkip               = "skip"
	FlagNoLaunch           = "no-launch"
	FlagNoReopen           = "no-reopen"
	FlagDetach             = "detach"
	FlagFromRunner         = "from-runner"
	FlagReopenSession      = "reopen-session"
	FlagAllowDeletions     = "allow-deletions"
	FlagAcceptWorktreeRisk = "accept-worktree-risk"
	FlagLogOffset          = "log-offset"
	FlagConfirmedPlan      = "confirmed-plan"
	FlagConfirmFirstSync   = "confirm-first-sync"
)

// Global flag names the job must inherit so it uses the same files.
const (
	FlagConfig        = "config"
	FlagStateDir      = "state-dir"
	FlagAppSupportDir = "app-support-dir"
)

// JobGlobals are the invoking process's global path flags (absolute, or
// empty when unset).
type JobGlobals struct {
	Config, StateDir, AppSupportDir string
}

// JobArgs returns the detached job's arguments for these options: the
// "switch" command, --from-runner, every option that is set, the
// inherited global path flags, then "--" and the target alias ("-" for the
// previous profile). Detach is never passed on.
func (o SwitchOptions) JobArgs(g JobGlobals) []string {
	a := []string{"switch", "--" + FlagFromRunner}
	flag := func(on bool, name string) {
		if on {
			a = append(a, "--"+name)
		}
	}
	value := func(name, v string) {
		if v != "" {
			a = append(a, "--"+name+"="+v)
		}
	}
	flag(o.Yes, FlagYes)
	value(FlagOnly, joinCategories(o.Only))
	value(FlagSkip, joinCategories(o.Skip))
	flag(o.NoLaunch, FlagNoLaunch)
	flag(o.NoReopen, FlagNoReopen)
	value(FlagReopenSession, o.ReopenCLISession)
	if o.AllowDeletions > 0 {
		value(FlagAllowDeletions, strconv.Itoa(o.AllowDeletions))
	}
	flag(o.AcceptWorktreeRisk, FlagAcceptWorktreeRisk)
	if o.LogOffset >= 0 {
		value(FlagLogOffset, strconv.FormatInt(o.LogOffset, 10))
	}
	value(FlagConfirmedPlan, o.ConfirmedPlan)
	flag(o.ConfirmFirstSync, FlagConfirmFirstSync)
	value(FlagConfig, g.Config)
	value(FlagStateDir, g.StateDir)
	value(FlagAppSupportDir, g.AppSupportDir)
	to := string(o.To)
	if to == "" {
		to = "-"
	}
	return append(a, "--", to)
}

func joinCategories[T ~string](cs []T) string {
	s := make([]string, len(cs))
	for i, c := range cs {
		s[i] = string(c)
	}
	return strings.Join(s, ",")
}
