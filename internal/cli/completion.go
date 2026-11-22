package cli

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/orchestrator"
)

// This file holds the completion command. The scripts are generated from
// the command tree (root) and the flags each command registers, so a new
// command or flag shows up in every shell without editing this file.

// Shells that completion supports.
var completionShells = []string{"bash", "fish", "zsh"}

// aliasesCommand is the hidden command the scripts call to list the
// profile aliases of the config file.
const aliasesCommand = "__aliases"

// valueKind says how to complete a flag's value.
type valueKind int

const (
	valueNone     valueKind = iota // a boolean flag
	valuePlain                     // any text; nothing to offer
	valueFile                      // a file name
	valueDir                       // a directory name
	valueCategory                  // comma-separated category names
)

// flagValueKinds names the flags whose values can be completed.
var flagValueKinds = map[string]valueKind{
	orchestrator.FlagConfig:        valueFile,
	flagBundle:                     valueFile,
	orchestrator.FlagStateDir:      valueDir,
	orchestrator.FlagAppSupportDir: valueDir,
	orchestrator.FlagOnly:          valueCategory,
	orchestrator.FlagSkip:          valueCategory,
}

// argKind says how to complete a command's positional arguments.
type argKind string

const (
	argNone    argKind = ""
	argAlias   argKind = "alias"   // a profile alias from the config
	argCommand argKind = "command" // a top-level command name
	argShell   argKind = "shell"   // a shell name
)

// commandArgKinds names the commands whose arguments can be completed.
var commandArgKinds = map[string]argKind{
	"plan":           argAlias,
	"switch":         argAlias,
	"profile remove": argAlias,
	"profile rename": argAlias,
	"help":           argCommand,
	"completion":     argShell,
}

// compFlag is one visible flag.
type compFlag struct {
	name  string
	usage string
	kind  valueKind
}

// compCmd is one visible command.
type compCmd struct {
	path  string // space-separated, "" for the root
	name  string
	short string
	subs  []*compCmd
	flags []compFlag // the command's own flags, without the global ones
	args  argKind
}

// compTree is everything the generators need.
type compTree struct {
	root    *compCmd
	all     []*compCmd // depth first, root first
	globals []compFlag
}

// buildCompTree walks the command tree. Hidden commands and hidden flags
// are left out.
func buildCompTree() *compTree {
	t := &compTree{globals: visibleFlags(func(fs *flag.FlagSet) { (&globalFlags{}).register(fs) })}
	var walk func(c *command, path string) *compCmd
	walk = func(c *command, path string) *compCmd {
		n := &compCmd{path: path, name: c.name, short: c.short, args: commandArgKinds[path]}
		if c.flags != nil {
			n.flags = visibleFlags(c.flags)
		}
		t.all = append(t.all, n)
		subs := append([]*command(nil), c.sub...)
		sort.Slice(subs, func(i, j int) bool { return subs[i].name < subs[j].name })
		for _, s := range subs {
			if s.hidden {
				continue
			}
			n.subs = append(n.subs, walk(s, strings.TrimSpace(path+" "+s.name)))
		}
		return n
	}
	t.root = walk(root(), "")
	return t
}

// visibleFlags returns the flags that register adds, sorted, without the
// hidden ones.
func visibleFlags(register func(*flag.FlagSet)) []compFlag {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	register(fs)
	var out []compFlag
	fs.VisitAll(func(f *flag.Flag) {
		if hiddenFlags[f.Name] {
			return
		}
		kind := valuePlain
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			kind = valueNone
		} else if k, ok := flagValueKinds[f.Name]; ok {
			kind = k
		}
		_, usage := flag.UnquoteUsage(f)
		out = append(out, compFlag{name: f.Name, usage: usage, kind: kind})
	})
	return out
}

// valueFlags returns the names ("--name") of the command's own flags that
// take a value.
func (t *compTree) valueFlags(c *compCmd) []string {
	var out []string
	for _, f := range c.flags {
		if f.kind != valueNone {
			out = append(out, "--"+f.name)
		}
	}
	return out
}

// globalValueFlags returns the names of the global flags that take a
// value.
func (t *compTree) globalValueFlags() []string {
	return t.valueFlags(&compCmd{flags: t.globals})
}

// flagsOfKind returns the names ("--name") of every flag of kind k.
func (t *compTree) flagsOfKind(k valueKind) []string {
	seen := map[string]bool{}
	add := func(fs []compFlag) {
		for _, f := range fs {
			if f.kind == k {
				seen["--"+f.name] = true
			}
		}
	}
	add(t.globals)
	for _, c := range t.all {
		add(c.flags)
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// argPaths returns the command paths whose arguments are of kind k.
func (t *compTree) argPaths(k argKind) []string {
	var out []string
	for _, c := range t.all {
		if c.args == k {
			out = append(out, c.path)
		}
	}
	return out
}

func names(cs []*compCmd) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.name
	}
	return out
}

func flagNames(fs []compFlag) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = "--" + f.name
	}
	return out
}

// shQuote quotes s for POSIX shells and zsh with single quotes.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fishQuote quotes s for fish with single quotes.
func fishQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

// shCasePattern quotes a command path for a case pattern.
func shCasePattern(paths []string) string {
	q := make([]string, len(paths))
	for i, p := range paths {
		q[i] = shQuote(p)
	}
	return strings.Join(q, "|")
}

// zshCasePattern quotes a command path for a zsh case pattern.
func zshCasePattern(paths []string) string {
	q := make([]string, len(paths))
	for i, p := range paths {
		q[i] = shQuote(p)
	}
	return "(" + strings.Join(q, "|") + ")"
}

// completionScript returns the completion script for shell.
func completionScript(shell string) (string, error) {
	t := buildCompTree()
	switch shell {
	case "bash":
		return t.bash(), nil
	case "zsh":
		return t.zsh(), nil
	case "fish":
		return t.fish(), nil
	}
	return "", usageErrorf("unknown shell %q (one of %s)", shell, strings.Join(completionShells, ", "))
}

func runCompletion(_ context.Context, e *env, _ *flag.FlagSet, args []string) error {
	s, err := completionScript(args[0])
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(e.stdout, s)
	return err
}

// runAliases prints the valid profile aliases of the config file, one per
// line, for the completion scripts. It never fails and prints nothing
// when the config cannot be read.
func runAliases(_ context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	paths, _, err := e.paths()
	if err != nil {
		return nil
	}
	cfg, exists, err := loadConfig(paths.ConfigFile)
	if err != nil || !exists {
		return nil
	}
	for _, p := range cfg.Profiles {
		if _, err := ids.ParseAlias(string(p.Alias)); err == nil {
			fmt.Fprintln(e.stdout, string(p.Alias))
		}
	}
	return nil
}

const generatedNote = `Generated by "paritydesk completion %s" from the command table; do not edit.`

// bash returns the bash script. It works with the bash 3.2 that macOS
// ships: no mapfile, no compopt requirement.
func (t *compTree) bash() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# bash completion for paritydesk.\n# "+generatedNote+"\n", "bash")
	w("# Load it with: source <(paritydesk completion bash)\n\n")

	w("_paritydesk_subs() {\n    case \"$1\" in\n")
	for _, c := range t.all {
		if len(c.subs) > 0 {
			w("        %s) echo %s ;;\n", shQuote(c.path), shQuote(strings.Join(names(c.subs), " ")))
		}
	}
	w("    esac\n}\n\n")

	w("_paritydesk_flags() {\n    case \"$1\" in\n")
	for _, c := range t.all {
		if len(c.flags) > 0 {
			w("        %s) echo %s ;;\n", shQuote(c.path), shQuote(strings.Join(flagNames(c.flags), " ")))
		}
	}
	w("    esac\n    echo %s\n}\n\n", shQuote(strings.Join(flagNames(t.globals), " ")))

	w("_paritydesk_value_flags() {\n    case \"$1\" in\n")
	for _, c := range t.all {
		if v := t.valueFlags(c); len(v) > 0 {
			w("        %s) echo %s ;;\n", shQuote(c.path), shQuote(strings.Join(v, " ")))
		}
	}
	w("    esac\n    echo %s\n}\n\n", shQuote(strings.Join(t.globalValueFlags(), " ")))

	w("_paritydesk_args() {\n    case \"$1\" in\n")
	for _, k := range []argKind{argAlias, argCommand, argShell} {
		w("        %s) echo %s ;;\n", shCasePattern(t.argPaths(k)), string(k))
	}
	w("    esac\n}\n\n")

	w(`# _paritydesk_has <words> <word> succeeds when word is one of the
# space-separated words.
_paritydesk_has() {
    local w
    for w in $1; do
        [ "$w" = "$2" ] && return 0
    done
    return 1
}

# _paritydesk_long <flag> spells a flag with two dashes.
_paritydesk_long() {
    case "$1" in
        --*) echo "$1" ;;
        *) echo "-$1" ;;
    esac
}

# _paritydesk_reply adds the lines of its input to COMPREPLY.
_paritydesk_reply() {
    local line
    while IFS= read -r line; do
        COMPREPLY+=("$line")
    done
}

_paritydesk() {
    local cur prev cmdpath word i subs pre
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev=""
    if [ "$COMP_CWORD" -gt 0 ]; then
        prev="${COMP_WORDS[COMP_CWORD-1]}"
    fi
    cmdpath=""
    i=1
    while [ "$i" -lt "$COMP_CWORD" ]; do
        word="${COMP_WORDS[i]}"
        case "$word" in
            --) break ;;
            -*=*) ;;
            -*)
                if _paritydesk_has "$(_paritydesk_value_flags "$cmdpath")" "$(_paritydesk_long "$word")"; then
                    i=$((i + 1))
                fi
                ;;
            *)
                if _paritydesk_has "$(_paritydesk_subs "$cmdpath")" "$word"; then
                    cmdpath="${cmdpath:+$cmdpath }$word"
                fi
                ;;
        esac
        i=$((i + 1))
    done

    case "$prev" in
        -*)
            if _paritydesk_has "$(_paritydesk_value_flags "$cmdpath")" "$(_paritydesk_long "$prev")"; then
                case "$(_paritydesk_long "$prev")" in
`)
	w("                    %s)\n", strings.Join(t.flagsOfKind(valueFile), "|"))
	w("                        compopt -o filenames 2>/dev/null\n")
	w("                        _paritydesk_reply < <(compgen -f -- \"$cur\") ;;\n")
	w("                    %s)\n", strings.Join(t.flagsOfKind(valueDir), "|"))
	w("                        compopt -o filenames 2>/dev/null\n")
	w("                        _paritydesk_reply < <(compgen -d -- \"$cur\") ;;\n")
	w("                    %s)\n", strings.Join(t.flagsOfKind(valueCategory), "|"))
	w("                        pre=\"\"\n")
	w("                        case \"$cur\" in *,*) pre=\"${cur%%,*},\" ;; esac\n")
	w("                        _paritydesk_reply < <(compgen -W %s -P \"$pre\" -- \"${cur##*,}\") ;;\n",
		shQuote(strings.Join(categoryNames(), " ")))
	w(`                esac
                return 0
            fi
            ;;
    esac

    case "$cur" in
        -*)
            _paritydesk_reply < <(compgen -W "$(_paritydesk_flags "$cmdpath")" -- "$cur")
            return 0
            ;;
    esac
    subs="$(_paritydesk_subs "$cmdpath")"
    if [ -n "$subs" ]; then
        _paritydesk_reply < <(compgen -W "$subs" -- "$cur")
        return 0
    fi
    case "$(_paritydesk_args "$cmdpath")" in
`)
	w("        alias) _paritydesk_reply < <(compgen -W \"$(command paritydesk %s 2>/dev/null)\" -- \"$cur\") ;;\n", aliasesCommand)
	w("        command) _paritydesk_reply < <(compgen -W \"$(_paritydesk_subs '')\" -- \"$cur\") ;;\n")
	w("        shell) _paritydesk_reply < <(compgen -W %s -- \"$cur\") ;;\n", shQuote(strings.Join(completionShells, " ")))
	w("    esac\n    return 0\n}\n\ncomplete -F _paritydesk paritydesk\n")
	return b.String()
}

// zshDescribed returns "name:description" entries for _describe.
func zshDescribed(items [][2]string) string {
	q := make([]string, len(items))
	for i, it := range items {
		q[i] = shQuote(strings.ReplaceAll(it[0], ":", `\:`) + ":" + it[1])
	}
	return strings.Join(q, " ")
}

func zshCommands(cs []*compCmd) string {
	items := make([][2]string, len(cs))
	for i, c := range cs {
		items[i] = [2]string{c.name, c.short}
	}
	return zshDescribed(items)
}

func zshFlags(fs []compFlag) string {
	items := make([][2]string, len(fs))
	for i, f := range fs {
		items[i] = [2]string{"--" + f.name, f.usage}
	}
	return zshDescribed(items)
}

// zsh returns the zsh script. It works both when sourced and when saved
// as _paritydesk on $fpath.
func (t *compTree) zsh() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("#compdef paritydesk\n# zsh completion for paritydesk.\n# "+generatedNote+"\n", "zsh")
	w("# Load it with: source <(paritydesk completion zsh)\n# or save it as _paritydesk in a directory on $fpath.\n\n")

	w("_paritydesk_subs() {\n  case $1 in\n")
	for _, c := range t.all {
		if len(c.subs) > 0 {
			w("    (%s) reply=(%s) ;;\n", shQuote(c.path), zshCommands(c.subs))
		}
	}
	w("    (*) reply=() ;;\n  esac\n}\n\n")

	w("_paritydesk_flags() {\n  case $1 in\n")
	for _, c := range t.all {
		if len(c.flags) > 0 {
			w("    (%s) reply=(%s) ;;\n", shQuote(c.path), zshFlags(c.flags))
		}
	}
	w("    (*) reply=() ;;\n  esac\n  reply+=(%s)\n}\n\n", zshFlags(t.globals))

	w("_paritydesk_value_flags() {\n  case $1 in\n")
	for _, c := range t.all {
		if v := t.valueFlags(c); len(v) > 0 {
			w("    (%s) reply=(%s) ;;\n", shQuote(c.path), strings.Join(v, " "))
		}
	}
	w("    (*) reply=() ;;\n  esac\n  reply+=(%s)\n}\n\n", strings.Join(t.globalValueFlags(), " "))

	w("_paritydesk_args() {\n  case $1 in\n")
	for _, k := range []argKind{argAlias, argCommand, argShell} {
		w("    %s) REPLY=%s ;;\n", zshCasePattern(t.argPaths(k)), string(k))
	}
	w("    (*) REPLY='' ;;\n  esac\n}\n\n")

	w(`_paritydesk() {
  local cur=${words[CURRENT]} prev=${words[CURRENT-1]} cmdpath='' word flag REPLY
  local -a reply aliases
  local -i i=2
  while (( i < CURRENT )); do
    word=${words[i]}
    case $word in
      (--) break ;;
      (-*=*) ;;
      (-*)
        flag=$word
        [[ $flag == --* ]] || flag=-$flag
        _paritydesk_value_flags "$cmdpath"
        if (( ${reply[(Ie)$flag]} )); then
          (( i++ ))
        fi
        ;;
      (*)
        _paritydesk_subs "$cmdpath"
        if (( ${reply[(I)${(b)word}:*]} )); then
          cmdpath=${cmdpath:+$cmdpath }$word
        fi
        ;;
    esac
    (( i++ ))
  done

  if [[ $prev == -* ]]; then
    flag=$prev
    [[ $flag == --* ]] || flag=-$flag
    _paritydesk_value_flags "$cmdpath"
    if (( ${reply[(Ie)$flag]} )); then
      case $flag in
`)
	w("        (%s) _files ;;\n", strings.Join(t.flagsOfKind(valueFile), "|"))
	w("        (%s) _files -/ ;;\n", strings.Join(t.flagsOfKind(valueDir), "|"))
	w("        (%s) compset -P '*,'; compadd -- %s ;;\n", strings.Join(t.flagsOfKind(valueCategory), "|"), strings.Join(categoryNames(), " "))
	w(`      esac
      return
    fi
  fi

  if [[ $cur == -* ]]; then
    _paritydesk_flags "$cmdpath"
    _describe -t flags 'flag' reply
    return
  fi
  _paritydesk_subs "$cmdpath"
  if (( $#reply )); then
    _describe -t commands 'command' reply
    return
  fi
  _paritydesk_args "$cmdpath"
  case $REPLY in
`)
	w("    (alias) aliases=(${(f)\"$(command paritydesk %s 2>/dev/null)\"}); compadd -- $aliases ;;\n", aliasesCommand)
	w("    (command) _paritydesk_subs ''; _describe -t commands 'command' reply ;;\n")
	w("    (shell) compadd -- %s ;;\n", strings.Join(completionShells, " "))
	w(`  esac
}

if [[ $funcstack[1] == _paritydesk ]]; then
  _paritydesk "$@"
else
  compdef _paritydesk paritydesk
fi
`)
	return b.String()
}

// fish returns the fish script.
func (t *compTree) fish() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# fish completion for paritydesk.\n# "+generatedNote+"\n", "fish")
	w("# Load it with: paritydesk completion fish | source\n\n")

	w("function __paritydesk_subs\n    switch \"$argv[1]\"\n")
	for _, c := range t.all {
		if len(c.subs) > 0 {
			w("        case %s\n            printf '%%s\\n' %s\n", fishQuote(c.path), strings.Join(names(c.subs), " "))
		}
	}
	w("    end\nend\n\n")

	w("function __paritydesk_value_flags\n    switch \"$argv[1]\"\n")
	for _, c := range t.all {
		if v := t.valueFlags(c); len(v) > 0 {
			w("        case %s\n            printf '%%s\\n' %s\n", fishQuote(c.path), strings.Join(v, " "))
		}
	}
	w("    end\n    printf '%%s\\n' %s\nend\n\n", strings.Join(t.globalValueFlags(), " "))

	w(`# __paritydesk_path prints the command path typed so far, such as
# "profile add".
function __paritydesk_path
    set -l tokens (commandline -opc)
    set -e tokens[1]
    set -l cmdpath ''
    set -l skip 0
    for word in $tokens
        if test $skip -eq 1
            set skip 0
            continue
        end
        switch $word
            case '--'
                break
            case '-*=*'
                continue
            case '--*'
                if contains -- $word (__paritydesk_value_flags "$cmdpath")
                    set skip 1
                end
            case '-*'
                if contains -- -$word (__paritydesk_value_flags "$cmdpath")
                    set skip 1
                end
            case '*'
                if contains -- $word (__paritydesk_subs "$cmdpath")
                    set cmdpath (string trim -- "$cmdpath $word")
                end
        end
    end
    echo $cmdpath
end

function __paritydesk_at
    set -l p (__paritydesk_path)
    test "$p" = "$argv[1]"
end

# __paritydesk_categories completes one name of a comma-separated list.
function __paritydesk_categories
    set -l pre (string replace -r '[^,]*$' '' -- (commandline -ct))
`)
	w("    for c in %s\n        echo $pre$c\n    end\nend\n\n", strings.Join(categoryNames(), " "))

	w("complete -c paritydesk -f\n\n# Commands\n")
	for _, c := range t.all {
		for _, s := range c.subs {
			w("complete -c paritydesk -n %s -a %s -d %s\n", fishQuote("__paritydesk_at "+shQuote(c.path)), s.name, fishQuote(s.short))
		}
	}
	fishFlag := func(cond string, f compFlag) {
		w("complete -c paritydesk")
		if cond != "" {
			w(" -n %s", fishQuote(cond))
		}
		w(" -l %s", f.name)
		switch f.kind {
		case valueFile:
			w(" -r -F")
		case valueDir:
			w(" -x -a '(__fish_complete_directories)'")
		case valueCategory:
			w(" -x -a '(__paritydesk_categories)'")
		case valuePlain:
			w(" -x")
		}
		w(" -d %s\n", fishQuote(f.usage))
	}
	w("\n# Flags of each command\n")
	for _, c := range t.all {
		for _, f := range c.flags {
			fishFlag("__paritydesk_at "+shQuote(c.path), f)
		}
	}
	w("\n# Global flags\n")
	for _, f := range t.globals {
		fishFlag("", f)
	}
	w("\n# Arguments\n")
	for _, p := range t.argPaths(argAlias) {
		w("complete -c paritydesk -n %s -a '(command paritydesk %s 2>/dev/null)'\n", fishQuote("__paritydesk_at "+shQuote(p)), aliasesCommand)
	}
	for _, p := range t.argPaths(argCommand) {
		w("complete -c paritydesk -n %s -a %s\n", fishQuote("__paritydesk_at "+shQuote(p)), fishQuote(strings.Join(names(t.root.subs), " ")))
	}
	for _, p := range t.argPaths(argShell) {
		w("complete -c paritydesk -n %s -a %s\n", fishQuote("__paritydesk_at "+shQuote(p)), fishQuote(strings.Join(completionShells, " ")))
	}
	return b.String()
}
