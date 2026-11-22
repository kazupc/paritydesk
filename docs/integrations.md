# Integrations

paritydesk ships three small wrappers for one-click switching. Each is a
thin shell script around two commands:

- `paritydesk status --short --json` to build a menu;
- `paritydesk switch --detach ... -- <alias>` to switch.

They are designed to behave exactly like those commands; they add no
logic of their own. They have been tested only against a fake
`paritydesk` binary, not yet in SwiftBar, xbar or Raycast on a real Mac
(release checklist steps 26 to 28).

All three:

- find the binary the way the plugin's hook shim does: the absolute path
  that `paritydesk init` records in
  `~/Library/Application Support/paritydesk/hook-binary`, then
  `/opt/homebrew/bin/paritydesk`, then `/usr/local/bin/paritydesk`.
  Never from `PATH`. Run `paritydesk init` (or `paritydesk doctor`) once
  so the path is recorded.
- accept only a profile alias that matches `^[a-z0-9][a-z0-9-]{0,31}$`
  and refuse anything else before running paritydesk;
- run with `set -eu`, quote every variable and use no `eval`.

The scripts are in [`integrations/`](../integrations). Copy them; do not
point SwiftBar or Raycast at a clone you might edit by accident.

## What a click does

A menu click or a Raycast run passes `--yes`, because there is no
terminal to show the plan in: the click is your confirmation. The
switch then runs as a background job, exactly as `paritydesk switch
--detach --yes` does, and it still stops on its own safety checks
without changing anything:

- more deletions than the limit (exit 22);
- a first sync that merges both accounts, which needs a confirmation in
  Terminal (exit 21);
- Code sessions that paritydesk sees working (exit 12). v0.1 cannot see
  them yet (`paritydesk doctor` says busy detection is unavailable), so
  only Claude's own quit dialog protects them: if you cancel there,
  paritydesk exits 10 and changes nothing.

`--yes` also accepts a switch after the active account was changed
outside paritydesk, which Terminal asks you to confirm first. Run
`paritydesk status` before clicking if you switched accounts by hand.

paritydesk is designed to post a notification when the background switch
finishes (release checklist step 13). If
something stopped it, run `paritydesk status` in Terminal. To see the
plan before you switch, use `paritydesk plan <alias>` or the `pd`
function, which asks first.

## SwiftBar or xbar

Copy [`integrations/swiftbar/paritydesk.5m.sh`](../integrations/swiftbar/paritydesk.5m.sh)
into your SwiftBar (or xbar) plugin folder and make it executable:

```
cp integrations/swiftbar/paritydesk.5m.sh "<plugin folder>/"
chmod 755 "<plugin folder>/paritydesk.5m.sh"
```

The `5m` in the name refreshes the menu every five minutes; rename it to
change that. The menu bar shows `pd <active alias>`. The menu lists the
other profiles; choosing one runs `paritydesk switch --detach --yes --
<alias>`. When a run was interrupted, the menu shows only a line asking
you to run `paritydesk recover` in Terminal.

The plugin reads the status document with `/usr/bin/plutil`, which
macOS ships. If the plugin folder's path contains a double quote or a
`|`, the menu shows the switch commands as text instead of actions.

## Raycast

Add the folder that holds
[`integrations/raycast/paritydesk-switch.sh`](../integrations/raycast/paritydesk-switch.sh)
as a script command directory in Raycast (Extensions, Script Commands,
Add Directories). The command "Switch Claude account" takes the profile
alias as its argument and runs `paritydesk switch --detach --yes --
<alias>`. It shows one line: that the switch continues in the
background, or the exit code.

## Shell function `pd`

Source [`integrations/shell/paritydesk.sh`](../integrations/shell/paritydesk.sh)
from `~/.zshrc` or `~/.bashrc`:

```
. /path/to/paritydesk.sh
```

Then `pd` prints `paritydesk status --short`, and `pd <alias>` runs
`paritydesk switch --detach -- <alias>`. In a terminal that shows the
plan and asks before it hands the switch to a background job; `pd` does
not pass `--yes`. The function body runs in a subshell, so its `set -eu`
never changes your shell.

## Checking the scripts

`go test ./internal/cli` runs each script against a fake `paritydesk`
binary under a temporary home and checks that invalid aliases never
reach it. When `shellcheck` is installed, the same tests run it on every
script and on the generated bash completion; otherwise that test is
skipped.
