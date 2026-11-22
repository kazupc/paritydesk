package app

// This file is the only place in the module that runs external commands
// (D22; a policy test enforces it). Every command is a fixed
// argv template; variable parts are validated before use and passed as
// separate arguments, never through a shell. AppleScript text is constant
// and takes its arguments through `on run argv`.

import (
	"context"
	"os/exec"

	"github.com/kazupc/paritydesk/internal/pderr"
)

// Absolute paths of the only programs paritydesk runs.
const (
	binOpen      = "/usr/bin/open"
	binOsascript = "/usr/bin/osascript"
	binLsof      = "/usr/sbin/lsof"
	binLaunchctl = "/bin/launchctl"
	binGit       = "/usr/bin/git"
)

// Constant AppleScript programs. Arguments arrive through argv.
const (
	scriptQuit = `on run argv
tell application id (item 1 of argv) to quit
end run`
	scriptProbe = `on run argv
tell application id (item 1 of argv) to get name
end run`
	scriptNotify = `on run argv
display notification (item 2 of argv) with title (item 1 of argv)
end run`
)

// Template names one allowed command.
type Template string

// Allowed command templates.
const (
	TmplOpenBundle   Template = "open-bundle"  // open -b <bundle id>
	TmplOpenURL      Template = "open-url"     // open <validated claude:// URL>
	TmplQuit         Template = "osa-quit"     // osascript -e <scriptQuit> <bundle id>
	TmplProbe        Template = "osa-probe"    // osascript -e <scriptProbe> <bundle id>
	TmplNotify       Template = "osa-notify"   // osascript -e <scriptNotify> <title> <text>
	TmplLsof         Template = "lsof"         // lsof -nP -w -F pfan
	TmplBootstrap    Template = "lc-bootstrap" // launchctl bootstrap gui/<uid> <plist>
	TmplBootout      Template = "lc-bootout"   // launchctl bootout gui/<uid>/<label>
	TmplGitPorcelain Template = "git-status"   // git -C <dir> status --porcelain
)

// Argv returns the full argv for a template and its validated arguments.
// Callers validate arguments before calling (bundle id pattern, URL from
// SessionURL, absolute tool-owned plist path, numeric uid, label pattern,
// absolute existing directory for git).
func Argv(t Template, args ...string) ([]string, error) {
	switch t {
	case TmplOpenBundle:
		return need(1, args, binOpen, "-b", arg(args, 0))
	case TmplOpenURL:
		return need(1, args, binOpen, arg(args, 0))
	case TmplQuit:
		return need(1, args, binOsascript, "-e", scriptQuit, arg(args, 0))
	case TmplProbe:
		return need(1, args, binOsascript, "-e", scriptProbe, arg(args, 0))
	case TmplNotify:
		return need(2, args, binOsascript, "-e", scriptNotify, arg(args, 0), arg(args, 1))
	case TmplLsof:
		return need(0, args, binLsof, "-nP", "-w", "-F", "pfan")
	case TmplBootstrap:
		return need(2, args, binLaunchctl, "bootstrap", "gui/"+arg(args, 0), arg(args, 1))
	case TmplBootout:
		return need(2, args, binLaunchctl, "bootout", "gui/"+arg(args, 0)+"/"+arg(args, 1))
	case TmplGitPorcelain:
		return need(1, args, binGit, "-C", arg(args, 0), "status", "--porcelain")
	}
	return nil, pderr.New(pderr.Internal, "exec", "unknown command template")
}

func arg(a []string, i int) string {
	if i < len(a) {
		return a[i]
	}
	return ""
}

func need(n int, args []string, argv ...string) ([]string, error) {
	if len(args) != n {
		return nil, pderr.New(pderr.Internal, "exec", "wrong argument count for command template")
	}
	return argv, nil
}

// Runner runs a template. Tests substitute a recorder.
type Runner interface {
	Run(ctx context.Context, t Template, args ...string) (stdout []byte, err error)
}

// ExecRunner is the real Runner.
type ExecRunner struct{}

// Run executes the template's argv with a minimal fixed environment and
// returns stdout.
func (ExecRunner) Run(ctx context.Context, t Template, args ...string) ([]byte, error) {
	argv, err := Argv(t, args...)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	return cmd.Output()
}
