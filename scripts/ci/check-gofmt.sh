#!/bin/sh
# Fails when any Go file needs gofmt.
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd -P)
cd "$root"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo "These files need gofmt:" >&2
	echo "$unformatted" >&2
	exit 1
fi
echo "gofmt: ok"
