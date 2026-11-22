package app

// This file is the only place in the module that runs external commands
// (D22; a policy test enforces it). Every command is a fixed
// argv template; variable parts are validated here, in Argv, and passed as
// separate arguments, never through a shell. AppleScript text is constant
// and takes its arguments through `on run argv`.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// Absolute paths of the only programs paritydesk runs.
const (
	binOpen      = "/usr/bin/open"
	binOsascript = "/usr/bin/osascript"
	binLsof      = "/usr/sbin/lsof"
	binLaunchctl = "/bin/launchctl"
	binGit       = "/usr/bin/git"
	binPlutil    = "/usr/bin/plutil"
)

// emptyTree is git's well-known empty tree object (SHA-1), the attribute
// source of TmplGitPorcelain.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

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
	TmplOpenURL      Template = "open-url"     // open <claude:// URL rebuilt by SessionURL>
	TmplQuit         Template = "osa-quit"     // osascript -e <scriptQuit> <bundle id>
	TmplProbe        Template = "osa-probe"    // osascript -e <scriptProbe> <bundle id>
	TmplNotify       Template = "osa-notify"   // osascript -e <scriptNotify> <title> <text>
	TmplLsof         Template = "lsof"         // lsof -nP -w -F pfan
	TmplBootstrap    Template = "lc-bootstrap" // launchctl bootstrap gui/<uid> <plist>
	TmplBootout      Template = "lc-bootout"   // launchctl bootout gui/<uid>/<label>
	TmplGitPorcelain Template = "git-status"   // git (hardened) -C <dir> status --porcelain
	TmplPlutil       Template = "plutil-xml"   // plutil -convert xml1 -o - <plist>
)

// Argument limits enforced by Argv.
const (
	maxNotifyTitle = 64
	maxNotifyText  = 200
)

var (
	bundleIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,254}$`)
	uidRE      = regexp.MustCompile(`^[0-9]{1,10}$`)
	labelRE    = regexp.MustCompile(`^` + regexp.QuoteMeta(JobLabelPrefix) + `[0-9a-z-]{8,64}$`)
)

// Argv validates the template's arguments and returns the full argv. It is
// the single place that decides what may reach an external program
// (D22, C5): bundle ids match ^[A-Za-z0-9][A-Za-z0-9.-]{0,254}$; a URL must
// be exactly what SessionURL builds; uids are numeric; job labels are
// JobLabelPrefix plus a run id; plists are absolute, clean .plist paths;
// the git directory is absolute, clean and an existing directory;
// notification text is short, printable and never starts with '-'.
// Anything else returns an error wrapping ErrInvalidArgument.
func Argv(t Template, args ...string) ([]string, error) {
	spec, ok := templates[t]
	if !ok {
		return nil, pderr.New(pderr.Internal, "exec", "unknown command template")
	}
	if len(args) != len(spec.check) {
		return nil, pderr.New(pderr.Internal, "exec", "wrong argument count for command template")
	}
	for i, check := range spec.check {
		if err := check(args[i]); err != nil {
			return nil, pderr.Wrap(pderr.Internal, "exec "+string(t), err)
		}
	}
	return spec.build(args), nil
}

type templateSpec struct {
	check []func(string) error
	build func(a []string) []string
}

var templates = map[Template]templateSpec{
	TmplOpenBundle: {[]func(string) error{checkBundleID}, func(a []string) []string {
		return []string{binOpen, "-b", a[0]}
	}},
	TmplOpenURL: {[]func(string) error{checkSessionURL}, func(a []string) []string {
		return []string{binOpen, a[0]}
	}},
	TmplQuit: {[]func(string) error{checkBundleID}, func(a []string) []string {
		return []string{binOsascript, "-e", scriptQuit, a[0]}
	}},
	TmplProbe: {[]func(string) error{checkBundleID}, func(a []string) []string {
		return []string{binOsascript, "-e", scriptProbe, a[0]}
	}},
	TmplNotify: {[]func(string) error{checkText(maxNotifyTitle), checkText(maxNotifyText)}, func(a []string) []string {
		return []string{binOsascript, "-e", scriptNotify, a[0], a[1]}
	}},
	TmplLsof: {nil, func([]string) []string {
		return []string{binLsof, "-nP", "-w", "-F", "pfan"}
	}},
	TmplBootstrap: {[]func(string) error{checkUID, checkPlist}, func(a []string) []string {
		return []string{binLaunchctl, "bootstrap", "gui/" + a[0], a[1]}
	}},
	TmplBootout: {[]func(string) error{checkUID, checkLabel}, func(a []string) []string {
		return []string{binLaunchctl, "bootout", "gui/" + a[0] + "/" + a[1]}
	}},
	TmplGitPorcelain: {[]func(string) error{checkGitDir}, func(a []string) []string {
		// fsmonitor and hooks are disabled on the command line (it takes
		// precedence over repository config); optional locks are off so
		// the index is never rewritten. Attributes are read from the empty
		// tree and no attributes file, so no clean or smudge filter named
		// by the worktree's .gitattributes runs (a filter driver is a
		// command). Files a filter normalizes (Git LFS, for example) may
		// then show as modified, which only errs toward "dirty". A git
		// without --attr-source (before 2.42) fails, which also counts as
		// dirty.
		return []string{binGit, "--no-optional-locks", "--attr-source=" + emptyTree,
			"-c", "core.attributesFile=/dev/null", "-c", "core.fsmonitor=", "-c", "core.hooksPath=/dev/null",
			"-C", a[0], "status", "--porcelain"}
	}},
	TmplPlutil: {[]func(string) error{checkPlist}, func(a []string) []string {
		return []string{binPlutil, "-convert", "xml1", "-o", "-", a[0]}
	}},
}

func invalid(what string) error { return fmt.Errorf("%w: %s", ErrInvalidArgument, what) }

func checkBundleID(s string) error {
	if !bundleIDRE.MatchString(s) {
		return invalid("bundle id")
	}
	return nil
}

// checkSessionURL accepts only the exact link SessionURL builds.
func checkSessionURL(s string) error {
	id, ok := strings.CutPrefix(s, sessionURLPrefix)
	if !ok {
		return invalid("url")
	}
	want, err := SessionURL(ids.SessionID(id))
	if err != nil || want != s {
		return invalid("url")
	}
	return nil
}

func checkUID(s string) error {
	if !uidRE.MatchString(s) {
		return invalid("uid")
	}
	return nil
}

func checkLabel(s string) error {
	if !labelRE.MatchString(s) {
		return invalid("label")
	}
	return nil
}

func checkAbsClean(s string) error {
	if s == "" || !filepath.IsAbs(s) || filepath.Clean(s) != s || strings.ContainsFunc(s, unicode.IsControl) {
		return invalid("path")
	}
	return nil
}

func checkPlist(s string) error {
	if err := checkAbsClean(s); err != nil {
		return err
	}
	if !strings.HasSuffix(s, ".plist") {
		return invalid("plist path")
	}
	return nil
}

func checkGitDir(s string) error {
	if err := checkAbsClean(s); err != nil {
		return err
	}
	fi, err := os.Stat(s)
	if err != nil || !fi.IsDir() {
		return invalid("git directory")
	}
	return nil
}

func checkText(max int) func(string) error {
	return func(s string) error {
		if s == "" || strings.HasPrefix(s, "-") || utf8.RuneCountInString(s) > max || !utf8.ValidString(s) ||
			strings.ContainsFunc(s, unicode.IsControl) {
			return invalid("notification text")
		}
		return nil
	}
}

// env returns the fixed environment of a template's process.
func env(t Template) []string {
	e := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	if t == TmplGitPorcelain {
		e = append(e, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	}
	return e
}

// CommandError reports a command that ran and exited unsuccessfully. It
// never carries the command's output: for osascript templates it keeps
// only the AppleScript error number parsed from the last line of stderr.
type CommandError struct {
	Template Template
	ExitCode int
	// OSAStatus is the AppleScript or Apple Event error number (for
	// example -1743 or -600), or 0 when none was reported.
	OSAStatus int
}

func (e *CommandError) Error() string {
	if e.OSAStatus != 0 {
		return fmt.Sprintf("app: %s exited with status %d (AppleScript error %d)", e.Template, e.ExitCode, e.OSAStatus)
	}
	return fmt.Sprintf("app: %s exited with status %d", e.Template, e.ExitCode)
}

// osaStatusRE finds the error number osascript prints at the end of its
// error line, e.g. "0:42: execution error: ... (-1743)".
var osaStatusRE = regexp.MustCompile(`\((-[0-9]{1,6})\)\s*$`)

// osaCanceledRE matches an error line that says the event was cancelled
// without giving a number (userCanceledErr's message is "User canceled.").
var osaCanceledRE = regexp.MustCompile(`(?i)\buser cancell?ed\b`)

// osaStatus returns the AppleScript error number osascript reported in
// stderr: the number at the end of the last line that ends with one.
// Without any number, a line saying "User canceled." gives -128
// (userCanceledErr); otherwise it returns 0.
func osaStatus(stderr []byte) int {
	lines := bytes.Split(bytes.TrimSpace(stderr), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		m := osaStatusRE.FindSubmatch(lines[i])
		if m == nil {
			continue
		}
		if n, err := strconv.Atoi(string(m[1])); err == nil {
			return n
		}
	}
	for _, l := range lines {
		if osaCanceledRE.Match(l) {
			return osaUserCanceled
		}
	}
	return 0
}

func isOSA(t Template) bool { return t == TmplQuit || t == TmplProbe || t == TmplNotify }

// Runner runs a template. Tests substitute a recorder.
type Runner interface {
	Run(ctx context.Context, t Template, args ...string) (stdout []byte, err error)
}

// ExecRunner is the real Runner.
type ExecRunner struct{}

// Run executes the template's argv with a minimal fixed environment and
// returns stdout. A non-zero exit gives a *CommandError together with
// whatever stdout the command wrote (lsof exits 1 on partial results).
func (ExecRunner) Run(ctx context.Context, t Template, args ...string) ([]byte, error) {
	argv, err := Argv(t, args...)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env(t)
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		ce := &CommandError{Template: t, ExitCode: ee.ExitCode()}
		if isOSA(t) {
			ce.OSAStatus = osaStatus(ee.Stderr)
		}
		return out, ce
	}
	if err != nil {
		return out, fmt.Errorf("app: run %s: %w", t, err)
	}
	return out, nil
}
