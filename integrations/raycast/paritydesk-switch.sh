#!/bin/sh

# Required parameters:
# @raycast.schemaVersion 1
# @raycast.title Switch Claude account
# @raycast.mode compact
# @raycast.packageName paritydesk
#
# Optional parameters:
# @raycast.argument1 { "type": "text", "placeholder": "profile alias" }
# @raycast.description Switch the Claude desktop app to another paritydesk profile in the background.
# @raycast.author paritydesk contributors
# @raycast.authorURL https://github.com/kazupc/paritydesk

# Raycast script command for paritydesk. It is a thin wrapper around
# "paritydesk switch --detach --yes -- <alias>". Running the command is
# your confirmation. The background job still stops on its own safety
# checks (deletion breaker, first sync, busy sessions); see
# docs/integrations.md.
#
# The binary is found like the plugin's hook shim finds it: the path that
# "paritydesk init" records, then the fixed Homebrew locations. Never
# from PATH.
set -eu
LC_ALL=C
export LC_ALL

RECORD="$HOME/Library/Application Support/paritydesk/hook-binary"

# find_paritydesk prints the path of the paritydesk binary.
find_paritydesk() {
  if [ -f "$RECORD" ]; then
    b=$(head -n 1 "$RECORD" 2>/dev/null || true)
    case "$b" in
      /*)
        if [ -f "$b" ] && [ -x "$b" ]; then
          printf '%s\n' "$b"
          return 0
        fi
        ;;
    esac
  fi
  for b in /opt/homebrew/bin/paritydesk /usr/local/bin/paritydesk; do
    if [ -f "$b" ] && [ -x "$b" ]; then
      printf '%s\n' "$b"
      return 0
    fi
  done
  return 1
}

# valid_alias succeeds when $1 matches ^[a-z0-9][a-z0-9-]{0,31}$.
valid_alias() {
  case "$1" in
    '' | -* | *[!a-z0-9-]*) return 1 ;;
  esac
  [ "${#1}" -le 32 ]
}

alias_arg="${1-}"
if ! valid_alias "$alias_arg"; then
  echo "Not a profile alias (a-z, 0-9 and -)"
  exit 2
fi
if ! bin=$(find_paritydesk); then
  echo "paritydesk not found; run paritydesk init once in Terminal"
  exit 127
fi
if "$bin" switch --detach --yes -- "$alias_arg" >/dev/null 2>&1; then
  echo "Switching to $alias_arg in the background"
else
  code=$?
  echo "paritydesk exited with $code; run paritydesk status in Terminal"
  exit "$code"
fi
