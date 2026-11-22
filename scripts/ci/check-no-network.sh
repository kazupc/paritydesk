#!/bin/sh
# Fails when net, net/http or crypto/tls is in the dependency graph of the
# module's packages, with and without their tests, for darwin and linux
# (D22). The policy test TestNoNetworkInDependencies checks the same for
# the host OS.
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd -P)
cd "$root"
status=0
for goos in darwin linux; do
	for tests in "" "-test"; do
		# $tests is empty or the single word -test.
		# shellcheck disable=SC2086
		deps=$(GOOS=$goos go list -deps $tests ./...)
		bad=$(printf '%s\n' "$deps" | grep -E -x 'net|net/http|crypto/tls' || true)
		if [ -n "$bad" ]; then
			echo "GOOS=$goos ${tests:-(no tests)}: network packages in the dependency graph:" >&2
			printf '%s\n' "$bad" >&2
			for pkg in $bad; do
				echo "  imported through:" >&2
				# shellcheck disable=SC2086
				GOOS=$goos go list -deps $tests -f '{{.ImportPath}} {{join .Imports " "}}' ./... |
					grep -E " $pkg( |\$)" | cut -d' ' -f1 | sed 's/^/    /' >&2 || true
			done
			status=1
		fi
	done
done
if [ "$status" -eq 0 ]; then
	echo "no network packages in the dependency graph (darwin, linux, with and without tests)"
fi
exit "$status"
