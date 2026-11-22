#!/bin/sh
# Runs a command with all network access denied, for
#   go test -exec "$PWD/scripts/ci/nonet-exec.sh" ./...
# go test passes the test binary and its flags as arguments. The sandbox
# profile allows everything except network operations (including local
# sockets). sandbox-exec is required; the script fails without it.
set -eu

sandbox=/usr/bin/sandbox-exec
if [ ! -x "$sandbox" ]; then
	echo "nonet-exec: $sandbox not found; the network-deny run needs macOS" >&2
	exit 1
fi
if [ "$#" -eq 0 ]; then
	echo "usage: $0 COMMAND [ARG...]" >&2
	exit 2
fi

exec "$sandbox" -p '(version 1) (allow default) (deny network*)' "$@"
