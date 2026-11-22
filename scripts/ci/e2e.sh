#!/bin/sh
# Runs the stub-app end-to-end tests (internal/e2e) on a disposable CI
# macOS runner. Never run this on a Mac with the real app or real data.
#
# It builds paritydesk, the e2e test binary and the stub app, installs the
# stub as Applications/Claude.app inside a fresh temporary HOME, and runs
# the tests with HOME pointing there. paritydesk's config in each test
# names the stub's bundle id and a data root inside that HOME.
#
# Guards: it runs only when CI=true, refuses the real app's bundle id, and
# refuses to run when /Applications/Claude.app exists (a machine with the
# real app installed).
set -eu

if [ "${CI:-}" != "true" ]; then
	echo "e2e: CI only (CI=true is not set); refusing to run" >&2
	exit 1
fi
bundle_id=${PARITYDESK_E2E_BUNDLE_ID:-dev.paritydesk.stubapp}
case "$bundle_id" in
com.anthropic.* | COM.ANTHROPIC.* | "")
	echo "e2e: refusing bundle id '$bundle_id'" >&2
	exit 1
	;;
esac
if [ -e /Applications/Claude.app ]; then
	echo "e2e: /Applications/Claude.app exists; refusing to run next to the real app" >&2
	exit 1
fi

root=$(cd "$(dirname "$0")/../.." && pwd -P)
tmp_parent=${RUNNER_TEMP:-${TMPDIR:-/tmp}}
mkdir -p "$tmp_parent"
# A path without symlinks: paritydesk compares process paths literally.
base="$(cd "$tmp_parent" && pwd -P)/paritydesk-e2e"
home="$base/home"
rm -rf "$base"
mkdir -p "$home/Applications"

cd "$root"
go build -o "$base/paritydesk" ./cmd/paritydesk
go test -c -o "$base/e2e.test" ./internal/e2e
"$root/scripts/ci/build-stubapp.sh" "$home/Applications/Claude.app" "$bundle_id"

# With PARITYDESK_E2E_NONET=1 the tests, and so every paritydesk run they
# start, run under the network-deny sandbox. The stub is started by
# Launch Services, outside the sandbox.
wrap=
if [ "${PARITYDESK_E2E_NONET:-}" = "1" ]; then
	wrap="$root/scripts/ci/nonet-exec.sh"
fi

echo "e2e: HOME=$home bundle id $bundle_id sandbox=${wrap:-none}"
cd "$root/internal/e2e"
# Output is shown as it comes and kept for the skip check below.
{
	set +e
	env HOME="$home" \
		PARITYDESK_E2E=1 \
		PARITYDESK_E2E_BUNDLE_ID="$bundle_id" \
		PARITYDESK_E2E_BINARY="$base/paritydesk" \
		${wrap:+"$wrap"} "$base/e2e.test" -test.v -test.count=1 -test.timeout=30m -test.run 'TestStubApp|TestCheckTarget'
	echo "$?" >"$base/status"
} 2>&1 | tee "$base/e2e.log"
status=$(cat "$base/status")
if grep -q -- '--- SKIP: TestStubApp' "$base/e2e.log"; then
	# The runner could not deliver Apple Events to the stub (or another
	# precondition failed); the tests did not run. A skipped run is not a
	# pass: the job fails unless PARITYDESK_E2E_ALLOW_SKIP=1 is set for a
	# runner known to block Apple Events.
	if [ "${PARITYDESK_E2E_ALLOW_SKIP:-}" = "1" ]; then
		echo "::warning title=e2e skipped::the stub-app tests were skipped (PARITYDESK_E2E_ALLOW_SKIP=1); see the e2e log"
	else
		echo "::error title=e2e skipped::the stub-app tests were skipped, so nothing was tested; see the e2e log"
		[ "$status" = "0" ] && status=1
	fi
fi
exit "$status"
