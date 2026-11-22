#!/bin/sh
# <xbar.title>paritydesk</xbar.title>
# <xbar.version>v0.1.0</xbar.version>
# <xbar.author>paritydesk contributors</xbar.author>
# <xbar.desc>Switch the Claude desktop app between paritydesk profiles.</xbar.desc>
# <xbar.dependencies>paritydesk</xbar.dependencies>
# <xbar.abouturl>https://github.com/kazupc/paritydesk</xbar.abouturl>
# <swiftbar.hideRunInTerminal>true</swiftbar.hideRunInTerminal>
# <swiftbar.hideDisablePlugin>true</swiftbar.hideDisablePlugin>
#
# SwiftBar or xbar plugin for paritydesk. It is a thin wrapper:
#   - the menu comes from "paritydesk status --short --json";
#   - a click runs "paritydesk switch --detach --yes -- <alias>".
# A click is your confirmation. The background job still stops on its
# own safety checks (deletion breaker, first sync, busy sessions); see
# docs/integrations.md. The file name sets the refresh interval (5m).
#
# The binary is found like the plugin's hook shim finds it: the path that
# "paritydesk init" records, then the fixed Homebrew locations. Never
# from PATH.
set -eu
LC_ALL=C
export LC_ALL

RECORD="$HOME/Library/Application Support/paritydesk/hook-binary"
PLUTIL=/usr/bin/plutil

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

# field prints one value of the JSON document in $1 (key path $2).
field() {
  printf '%s' "$1" | "$PLUTIL" -extract "$2" raw -o - - 2>/dev/null
}

do_switch() {
  if ! valid_alias "$1"; then
    echo "paritydesk: not a profile alias" >&2
    exit 2
  fi
  if ! bin=$(find_paritydesk); then
    echo "paritydesk: binary not found; run paritydesk init once" >&2
    exit 127
  fi
  exec "$bin" switch --detach --yes -- "$1"
}

menu() {
  if ! bin=$(find_paritydesk); then
    echo "pd ?"
    echo "---"
    echo "paritydesk was not found. Run paritydesk init once in Terminal."
    return 0
  fi
  if [ ! -x "$PLUTIL" ] || ! json=$("$bin" status --short --json 2>/dev/null); then
    echo "pd !"
    echo "---"
    echo "paritydesk status failed. Run paritydesk status in Terminal. | color=red"
    return 0
  fi
  active=$(field "$json" active || true)
  if ! valid_alias "$active"; then
    active=""
  fi
  echo "pd ${active:-?}"
  echo "---"
  if [ "$(field "$json" needs_recover || true)" = "true" ]; then
    echo "A run was interrupted. Run paritydesk recover in Terminal. | color=red"
    return 0
  fi
  # A click calls this script again; only a plain path can be passed.
  self="$0"
  case "$self" in
    /*) ;;
    *) self="" ;;
  esac
  case "$self" in
    *'"'* | *'|'* | *'
'*) self="" ;;
  esac
  count=$(field "$json" profiles || echo 0)
  case "$count" in
    '' | *[!0-9]*) count=0 ;;
  esac
  i=0
  while [ "$i" -lt "$count" ]; do
    a=$(field "$json" "profiles.$i.alias" || true)
    i=$((i + 1))
    if ! valid_alias "$a"; then
      continue
    fi
    if [ "$a" = "$active" ]; then
      echo "$a (active)"
    elif [ -n "$self" ]; then
      echo "Switch to $a | bash=\"$self\" param1=switch param2=$a terminal=false refresh=true"
    else
      echo "Switch to $a: run paritydesk switch $a in Terminal"
    fi
  done
  echo "---"
  echo "Refresh | refresh=true"
}

if [ "$#" -ge 1 ] && [ "$1" = "switch" ]; then
  if [ "$#" -ne 2 ]; then
    echo "usage: $0 switch <alias>" >&2
    exit 2
  fi
  do_switch "$2"
fi
menu
