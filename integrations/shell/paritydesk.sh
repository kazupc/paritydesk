# Shell function for paritydesk. Source this file from ~/.zshrc or
# ~/.bashrc:
#
#   . /path/to/paritydesk/integrations/shell/paritydesk.sh
#
# Then:
#   pd            show the active profile (paritydesk status --short)
#   pd <alias>    switch in the background (paritydesk switch --detach -- <alias>)
#
# In a terminal the switch shows the plan and asks before it hands the
# switch to a background job, exactly as "paritydesk switch --detach"
# does. The function body runs in a subshell, so its "set -eu" and
# variables never leak into your shell. It works in sh, bash and zsh.
#
# The binary is found like the plugin's hook shim finds it: the path that
# "paritydesk init" records, then the fixed Homebrew locations. Never
# from PATH, so a stray paritydesk earlier on PATH is never run.

pd() (
  set -eu
  LC_ALL=C
  export LC_ALL
  record="$HOME/Library/Application Support/paritydesk/hook-binary"
  bin=""
  if [ -f "$record" ]; then
    b=$(head -n 1 "$record" 2>/dev/null || true)
    case "$b" in
      /*)
        if [ -f "$b" ] && [ -x "$b" ]; then
          bin="$b"
        fi
        ;;
    esac
  fi
  if [ -z "$bin" ]; then
    for b in /opt/homebrew/bin/paritydesk /usr/local/bin/paritydesk; do
      if [ -f "$b" ] && [ -x "$b" ]; then
        bin="$b"
        break
      fi
    done
  fi
  if [ -z "$bin" ]; then
    echo "pd: paritydesk not found; run paritydesk init once" >&2
    exit 127
  fi
  if [ "$#" -eq 0 ]; then
    exec "$bin" status --short
  fi
  if [ "$#" -ne 1 ]; then
    echo "usage: pd [<alias>]" >&2
    exit 2
  fi
  # The alias must match ^[a-z0-9][a-z0-9-]{0,31}$.
  case "$1" in
    '' | -* | *[!a-z0-9-]*)
      echo "pd: not a profile alias (a-z, 0-9 and -)" >&2
      exit 2
      ;;
  esac
  if [ "${#1}" -gt 32 ]; then
    echo "pd: not a profile alias (at most 32 characters)" >&2
    exit 2
  fi
  exec "$bin" switch --detach -- "$1"
)
