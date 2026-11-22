#!/bin/sh
# Runs the stub-app end-to-end tests (internal/e2e) on a disposable CI
# macOS runner. Never run this on a Mac with the real app or real data.
#
# usage: e2e.sh [prepare | probe LABEL | grant-tcc | run]
#
#   prepare    builds paritydesk, the e2e test binary and the stub app, and
#              installs the stub as Applications/Claude.app inside a fresh
#              temporary HOME.
#   probe      launches the installed stub once and sends it two harmless
#              Apple Events (reopen, count windows) from osascript, outside
#              any sandbox, inside an allow-all sandbox-exec profile and
#              inside the network-deny profile of nonet-exec.sh. A Finder
#              "count windows" in each mode is the control: the runner
#              image pre-grants Automation for osascript to Finder. It
#              prints what each probe returned and, on GitHub Actions,
#              writes step outputs and a job summary (see probe_outputs).
#              It quits the stub and exits 0 whatever the probes return.
#   grant-tcc  GitHub-hosted runners only: adds Automation (AppleEvents)
#              rows for the stub's bundle id to the runner user's TCC.db,
#              in the format the runner image itself uses for Finder and
#              System Events (actions/runner-images,
#              images/macos/scripts/build/configure-tccdb-macos.sh).
#   run        runs the tests with HOME pointing at the temporary HOME.
#              PARITYDESK_E2E_NONET=1 runs them (and every paritydesk run
#              they start) under the network-deny sandbox; otherwise they
#              run without sandbox-exec. A skip fails, unless both
#              PARITYDESK_E2E_ALLOW_SKIP=1 (a maintainer's choice, a
#              repository variable in CI) and PARITYDESK_E2E_RUNNER_BLOCKS_AE=1
#              (set from the probe's runner_blocks_ae output) are set.
#   (none)     prepare, then run (no probe: set PARITYDESK_E2E_NONET
#              yourself).
#
# paritydesk's config in each test names the stub's bundle id and a data
# root inside the temporary HOME.
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
case "$bundle_id" in
*[!A-Za-z0-9.-]*)
	echo "e2e: bundle id may hold only letters, digits, dots and hyphens" >&2
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
app="$home/Applications/Claude.app"
lsregister=/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister

# summary appends its arguments, one line each, to the job summary.
summary() {
	if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
		printf '%s\n' "$@" >>"$GITHUB_STEP_SUMMARY"
	fi
}

# output sets a step output (name=value).
output() {
	echo "e2e: output $1"
	if [ -n "${GITHUB_OUTPUT:-}" ]; then
		printf '%s\n' "$1" >>"$GITHUB_OUTPUT"
	fi
}

# with_timeout SECONDS COMMAND... runs COMMAND and kills it with SIGALRM
# (exit status 142) after SECONDS. The alarm survives exec.
with_timeout() {
	/usr/bin/perl -e 'alarm shift @ARGV; exec @ARGV or die "exec failed\n"' "$@"
}

do_prepare() {
	rm -rf "$base"
	mkdir -p "$home/Applications"
	cd "$root"
	go build -o "$base/paritydesk" ./cmd/paritydesk
	go test -c -o "$base/e2e.test" ./internal/e2e
	"$root/scripts/ci/build-stubapp.sh" "$app" "$bundle_id"
	echo "e2e: prepared $base (HOME=$home, bundle id $bundle_id)"
}

require_prepared() {
	if [ ! -x "$base/paritydesk" ] || [ ! -x "$base/e2e.test" ] || [ ! -f "$app/Contents/Info.plist" ]; then
		echo "e2e: run 'e2e.sh prepare' first" >&2
		exit 1
	fi
}

# ---------------------------------------------------------------- probe

diag="$home/ae-diagnostic"
conf="$home/Applications/stubapp.conf"
stub_pid=

# in_mode MODE COMMAND... runs COMMAND outside any sandbox (none), under
# an allow-all sandbox-exec profile, or under the network-deny profile the
# tests use.
in_mode() {
	m=$1
	shift
	case "$m" in
	none) "$@" ;;
	sandbox-allow-all) /usr/bin/sandbox-exec -p '(version 1) (allow default)' "$@" ;;
	sandbox-nonet) "$root/scripts/ci/nonet-exec.sh" "$@" ;;
	*)
		echo "e2e: unknown mode $m" >&2
		return 2
		;;
	esac
}

# osa MODE TARGET_BUNDLE_ID COMMAND sends "tell application id TARGET to
# COMMAND" from osascript, in the environment the tests give their child
# processes (internal/e2e harness.env), with a 20 s Apple Event timeout.
osa() {
	in_mode "$1" /usr/bin/env -i HOME="$home" PATH=/usr/bin:/bin:/usr/sbin:/sbin LC_ALL=C NO_COLOR=1 TMPDIR="${TMPDIR:-/tmp}" \
		/usr/bin/perl -e 'alarm shift @ARGV; exec @ARGV or die "exec failed\n"' 60 \
		/usr/bin/osascript -e 'on run argv' -e 'with timeout of 20 seconds' \
		-e "tell application id (item 1 of argv) to $3" -e 'end timeout' -e 'end run' "$2"
}

# classify STATUS OUTPUT names the outcome of one osascript run:
#   ok                    the event was handled (exit 0)
#   delivered(N)          the app itself answered with error N, so the
#                         event reached it (-1708 not handled, -1728, ...)
#   blocked-tcc(N)        -1743 or -1744: Automation (TCC) refused it
#   blocked-privilege(N)  -10004 errAEPrivilegeViolation: refused before
#                         delivery, as for a sandboxed sender
#   timeout(N)            -1712, or timeout(alarm) when killed by the
#                         60 s alarm: an Automation prompt nobody can
#                         answer, or a stub that hangs; not counted as a
#                         refusal, since it cannot tell the two apart
#   not-running(N)        -600, -609, -903: the target is not running
#   error(...)            anything else
classify() {
	if [ "$1" = 0 ]; then
		echo ok
		return
	fi
	if [ "$1" = 142 ]; then
		echo "timeout(alarm)"
		return
	fi
	n=$(printf '%s\n' "$2" | sed -n 's/.*(\(-[0-9][0-9]*\))[[:space:]]*$/\1/p' | tail -n 1)
	case "$n" in
	-1700 | -1701 | -1708 | -1728 | -10000) echo "delivered($n)" ;;
	-1743 | -1744) echo "blocked-tcc($n)" ;;
	-10004) echo "blocked-privilege($n)" ;;
	-1712) echo "timeout($n)" ;;
	-600 | -609 | -903) echo "not-running($n)" ;;
	"") echo "error(exit $1)" ;;
	*) echo "error($n)" ;;
	esac
}

# probe_one LABEL MODE TARGET_NAME TARGET_ID COMMAND prints one probe and
# leaves its class in $class.
probe_one() {
	if out=$(osa "$2" "$4" "$5" 2>&1); then
		st=0
	else
		st=$?
	fi
	class=$(classify "$st" "$out")
	out=$(printf '%s' "$out" | tr '\n' ' ')
	echo "ae-probe[$1]: mode=$2 target=$3 event='$5' exit=$st class=$class output='$out'"
}

# verdict REOPEN_CLASS COUNT_CLASS: ok when both events reached the stub,
# blocked when both were refused before delivery, mixed otherwise
# (timeouts included).
verdict() {
	case "$1/$2" in
	ok/ok | ok/delivered*) echo ok ;;
	blocked*/blocked*) echo blocked ;;
	*) echo mixed ;;
	esac
}

# runner_refuses REOPEN_CLASS COUNT_CLASS FINDER_CLASS prints 1 when the
# classes outside any sandbox show the runner refusing Apple Events, not
# the stub failing: both stub events refused by Automation (TCC decides
# per sender and target; the target cannot cause -1743 or -1744), or both
# refused with -10004 while the Finder control is refused the same way.
# A -10004 for the stub alone, a timeout or a crash points at the stub
# and prints 0.
runner_refuses() {
	case "$1/$2/$3" in
	blocked-tcc*/blocked-tcc*/*) echo 1 ;;
	blocked-privilege*/blocked-privilege*/blocked-privilege*) echo 1 ;;
	*) echo 0 ;;
	esac
}

is_stub_pid() {
	if [ -z "$1" ] || ! kill -0 "$1" 2>/dev/null; then
		return 1
	fi
	cmd=$(ps -p "$1" -o command= 2>/dev/null || true)
	case "$cmd" in
	"$app/Contents/MacOS/stubapp"*) return 0 ;;
	*) return 1 ;;
	esac
}

# stop_stub quits the diagnostic stub: by Apple Event when that worked
# outside the sandbox, then, if it still runs, with SIGTERM and SIGKILL to
# its own verified pid only. It also unregisters the bundle (the tests
# register it again) and removes the diagnostic stubapp.conf.
stop_stub() {
	if is_stub_pid "$stub_pid"; then
		if [ "${v_none:-}" = ok ]; then
			with_timeout 30 /usr/bin/osascript -e 'on run argv' -e 'tell application id (item 1 of argv) to quit' -e 'end run' "$bundle_id" >/dev/null 2>&1 || true
			i=0
			while [ "$i" -lt 15 ] && is_stub_pid "$stub_pid"; do
				sleep 1
				i=$((i + 1))
			done
		fi
		if is_stub_pid "$stub_pid"; then
			kill -TERM "$stub_pid" 2>/dev/null || true
			sleep 3
		fi
		if is_stub_pid "$stub_pid"; then
			kill -KILL "$stub_pid" 2>/dev/null || true
		fi
	fi
	if [ -x "$lsregister" ]; then
		"$lsregister" -u "$app" >/dev/null 2>&1 || true
	fi
	rm -f "$conf" "$diag/state/stubapp.pid"
}

do_probe() {
	label=${1:-probe}
	case "$label" in
	*[!A-Za-z0-9-]* | "")
		echo "e2e: probe label may hold only letters, digits and hyphens" >&2
		exit 2
		;;
	esac
	require_prepared
	if [ "$(cd "$HOME" 2>/dev/null && pwd -P)" = "$home" ]; then
		echo "e2e: probe must run with the runner's own HOME (it reads the runner's TCC.db)" >&2
		exit 1
	fi

	echo "== runner"
	sw_vers || true
	echo "arch=$(uname -m) user=$(id -un) RUNNER_ENVIRONMENT=${RUNNER_ENVIRONMENT:-unset}"
	/usr/bin/csrutil status 2>&1 || true
	echo "== AppleEvents rows in the runner user's TCC.db (needs Full Disk Access to read)"
	/usr/bin/sqlite3 -readonly "$HOME/Library/Application Support/com.apple.TCC/TCC.db" \
		"SELECT client, client_type, auth_value, auth_reason, indirect_object_identifier FROM access WHERE service = 'kTCCServiceAppleEvents' ORDER BY indirect_object_identifier, client;" 2>&1 || true

	echo "== launching the stub (bundle id $bundle_id)"
	rm -rf "$diag"
	mkdir -p "$diag/data" "$diag/logs" "$diag/state"
	printf '# written by scripts/ci/e2e.sh probe\ndata_dir = %s\nlog_dir = %s\nstate_dir = %s\nveto_seconds = 0\nveto_mode = later\n' \
		"$diag/data/Claude" "$diag/logs" "$diag/state" >"$conf"
	if [ -x "$lsregister" ]; then
		"$lsregister" -f "$app"
	fi
	trap stop_stub EXIT
	since=$(date '+%Y-%m-%d %H:%M:%S')
	launched=0
	if env HOME="$home" /usr/bin/open -b "$bundle_id"; then
		i=0
		while [ "$i" -lt 60 ]; do
			stub_pid=$(cat "$diag/state/stubapp.pid" 2>/dev/null || true)
			if is_stub_pid "$stub_pid"; then
				launched=1
				break
			fi
			sleep 1
			i=$((i + 1))
		done
	fi
	if [ "$launched" != 1 ]; then
		echo "::error title=e2e probe::the stub did not launch; that is a problem of the stub or its build, not of the runner"
		output "stub_launched=0"
		output "outside=skipped"
		output "sandbox_allow_all=skipped"
		output "sandbox_nonet=skipped"
		output "e2e_nonet=1"
		output "runner_blocks_ae=0"
		summary "### Apple Events diagnostic ($label)" "" "The stub did not launch, so nothing was probed. A skip of the stub-app tests will fail the job." ""
		exit 0
	fi
	echo "stub pid $stub_pid"
	sleep 1

	echo "== probes"
	rows=
	for mode in none sandbox-allow-all sandbox-nonet; do
		probe_one "$label" "$mode" stub "$bundle_id" reopen
		c_reopen=$class
		probe_one "$label" "$mode" stub "$bundle_id" "count windows"
		c_count=$class
		probe_one "$label" "$mode" Finder com.apple.finder "count windows"
		c_finder=$class
		v=$(verdict "$c_reopen" "$c_count")
		case "$mode" in
		none)
			v_none=$v
			runner_blocks_ae=$(runner_refuses "$c_reopen" "$c_count" "$c_finder")
			;;
		sandbox-allow-all) v_allow=$v ;;
		sandbox-nonet) v_nonet=$v ;;
		esac
		rows="$rows| $mode | $c_reopen | $c_count | $c_finder | $v |
"
		if ! is_stub_pid "$stub_pid"; then
			echo "::warning title=e2e probe::the stub exited during the $mode probes"
		fi
	done

	echo "== unified log: TCC, appleeventsd and sandbox denials since $since (last 80 lines)"
	with_timeout 120 /usr/bin/log show --style compact --start "$since" --predicate \
		'(process == "tccd" AND (eventMessage CONTAINS "AppleEvents" OR eventMessage CONTAINS "AUTHREQ")) OR process == "appleeventsd" OR (eventMessage CONTAINS "Sandbox:" AND eventMessage CONTAINS[c] "osascript")' \
		2>&1 | tail -n 80 || true

	# What the tests will do, from the probes:
	#   e2e_nonet=1         Apple Events reach the stub inside the
	#                       network-deny sandbox: run the tests there.
	#   e2e_nonet=0         they do not: run the tests without sandbox-exec.
	#   runner_blocks_ae=1  the runner refuses them outside any sandbox,
	#                       to a stub that is running, in a way the stub
	#                       cannot cause (runner_refuses). A skip of the
	#                       stub-app tests is then reported instead of
	#                       failed, but only if a maintainer has also set
	#                       PARITYDESK_E2E_ALLOW_SKIP=1.
	if [ "$v_nonet" = ok ]; then
		e2e_nonet=1
	else
		e2e_nonet=0
	fi
	nonet_note="The paritydesk runs the stub-app tests start are then not under the network-deny sandbox; no other job drives the built binary under it (the checks job only proves there are no network packages, and the macOS test job's network-deny run skips the stub-app tests)."
	if [ "$v_none" = ok ] && [ "$v_nonet" = ok ]; then
		conclusion="Apple Events reach the stub, also inside the network-deny sandbox. The tests run under that sandbox."
	elif [ "$v_none" = ok ] && [ "$v_allow" != ok ]; then
		conclusion="Apple Events reach the stub outside sandbox-exec but are refused under any sandbox-exec profile, even one that allows everything: being sandboxed is the blocker, not a rule of the profile. The tests run without sandbox-exec. $nonet_note"
	elif [ "$v_none" = ok ]; then
		conclusion="Apple Events reach the stub outside sandbox-exec and under an allow-all profile, but not under (deny network*): a rule of that profile is the blocker, so a narrower profile could work. The tests run without sandbox-exec for now. $nonet_note"
	elif [ "$runner_blocks_ae" = 1 ]; then
		conclusion="The runner refuses Apple Events from osascript to the running stub even outside any sandbox, in a way the stub cannot cause (see the classes and the Finder control). The stub-app tests cannot run here. A skip still fails the job unless a maintainer has set the repository variable PARITYDESK_E2E_ALLOW_SKIP=1; then the manual run of docs/release-checklist.md is required before every release."
	else
		conclusion="Unexpected probe results (see the classes): the stub may hang, have crashed or answer wrongly, or the refusal may be the stub's own. That is not shown to be the runner's policy. The tests run and a skip fails the job."
	fi
	echo "ae-probe[$label]: $conclusion"

	output "stub_launched=1"
	output "outside=$v_none"
	output "sandbox_allow_all=$v_allow"
	output "sandbox_nonet=$v_nonet"
	output "e2e_nonet=$e2e_nonet"
	output "runner_blocks_ae=$runner_blocks_ae"
	summary "### Apple Events diagnostic ($label)" "" \
		"Stub bundle id \`$bundle_id\`, runner environment \`${RUNNER_ENVIRONMENT:-unset}\`." "" \
		"| mode | stub: reopen | stub: count windows | Finder: count windows (control) | stub verdict |" \
		"| --- | --- | --- | --- | --- |"
	summary "$rows" "$conclusion" ""
	exit 0
}

# ---------------------------------------------------------------- grant-tcc

do_grant_tcc() {
	if [ "${GITHUB_ACTIONS:-}" != "true" ] || [ "${RUNNER_ENVIRONMENT:-}" != "github-hosted" ]; then
		echo "e2e: grant-tcc runs only on GitHub-hosted runners; nothing written"
		return 0
	fi
	db="$HOME/Library/Application Support/com.apple.TCC/TCC.db"
	cols=$(/usr/bin/sqlite3 "$db" "SELECT count(*) FROM pragma_table_info('access');" 2>&1 || true)
	if [ "$cols" != 17 ]; then
		echo "::warning title=e2e grant-tcc::the user TCC.db could not be read or has an unknown layout ($cols); nothing written"
		summary "### Automation grant" "" "The runner user's TCC.db could not be read or has an unknown layout (\`$cols\`); nothing was written." ""
		return 0
	fi
	now=$(date +%s)
	failed=0
	# The clients the runner image grants AppleEvents for (by path), so
	# that whichever of them TCC attributes osascript to is covered. The
	# target is only the stub's bundle id.
	for client in /usr/bin/osascript /bin/bash /opt/hca/hosted-compute-agent /usr/local/opt/runner/provisioner/provisioner; do
		if ! /usr/bin/sqlite3 "$db" "INSERT OR REPLACE INTO access VALUES('kTCCServiceAppleEvents','$client',1,2,3,1,NULL,NULL,0,'$bundle_id',NULL,NULL,$now,NULL,NULL,'UNUSED',$now);"; then
			failed=1
		fi
	done
	/usr/bin/sqlite3 -readonly "$db" "SELECT client, auth_value, indirect_object_identifier FROM access WHERE service = 'kTCCServiceAppleEvents' AND indirect_object_identifier = '$bundle_id';" || true
	if [ "$failed" = 1 ]; then
		echo "::warning title=e2e grant-tcc::writing the user TCC.db failed (see the log)"
		summary "### Automation grant" "" "Writing AppleEvents rows for \`$bundle_id\` to the runner user's TCC.db failed." ""
	else
		summary "### Automation grant" "" "AppleEvents rows for \`$bundle_id\` were written to the runner user's TCC.db; the next diagnostic shows whether tccd honours them." ""
	fi
	return 0
}

# ---------------------------------------------------------------- run

do_run() {
	require_prepared
	# With PARITYDESK_E2E_NONET=1 the tests, and so every paritydesk run
	# they start, run under the network-deny sandbox. The stub is started
	# by Launch Services, outside the sandbox.
	wrap=
	sandbox=none
	if [ "${PARITYDESK_E2E_NONET:-}" = "1" ]; then
		wrap="$root/scripts/ci/nonet-exec.sh"
		sandbox=nonet-exec
	fi
	echo "e2e: HOME=$home bundle id $bundle_id sandbox=$sandbox"
	if [ "$sandbox" = none ]; then
		echo "::notice title=e2e sandbox::the stub-app tests run without the network-deny sandbox (PARITYDESK_E2E_NONET is not 1), so the paritydesk runs they start are not network-denied; the checks job only proves there are no network packages, and the macOS test job's network-deny run does not drive the built binary"
	fi
	cd "$root/internal/e2e"
	# Output is shown as it comes and kept for the skip check below.
	{
		set +e
		env HOME="$home" \
			PARITYDESK_E2E=1 \
			PARITYDESK_E2E_BUNDLE_ID="$bundle_id" \
			PARITYDESK_E2E_BINARY="$base/paritydesk" \
			PARITYDESK_E2E_SANDBOX="$sandbox" \
			${wrap:+"$wrap"} "$base/e2e.test" -test.v -test.count=1 -test.timeout=30m -test.run 'TestStubApp|TestCheckTarget'
		echo "$?" >"$base/status"
	} 2>&1 | tee "$base/e2e.log"
	status=$(cat "$base/status")
	result=passed
	if [ "$status" != 0 ]; then
		result=failed
	fi
	if /usr/bin/grep -q -- '--- SKIP: TestStubApp' "$base/e2e.log"; then
		# The tests did not run. A skipped run is not a pass: the job fails
		# unless both a maintainer allowed skips (PARITYDESK_E2E_ALLOW_SKIP=1)
		# and the Apple Events diagnostic showed that the runner refuses
		# Apple Events to the running stub outside any sandbox, in a way
		# the stub cannot cause (PARITYDESK_E2E_RUNNER_BLOCKS_AE=1).
		if [ "${PARITYDESK_E2E_ALLOW_SKIP:-}" = "1" ] && [ "${PARITYDESK_E2E_RUNNER_BLOCKS_AE:-}" = "1" ]; then
			result="NOT RUN (skipped; the diagnostic showed that this runner refuses Apple Events, and skips are allowed)"
			echo "::warning title=e2e not run::the stub-app tests did not run: this runner refuses Apple Events to the stub (see the Apple Events diagnostic). Nothing was tested. Before a release, run them on a self-hosted runner or a Mac or macOS VM without the real app (docs/release-checklist.md)."
		elif [ "${PARITYDESK_E2E_RUNNER_BLOCKS_AE:-}" = "1" ]; then
			result="FAILED (skipped; the diagnostic showed that this runner refuses Apple Events, but skips are not allowed)"
			echo "::error title=e2e skipped::the stub-app tests were skipped, so nothing was tested. The diagnostic shows that this runner refuses Apple Events; a maintainer may accept that by setting the repository variable PARITYDESK_E2E_ALLOW_SKIP=1, which makes the manual run of docs/release-checklist.md required before every release"
			[ "$status" = "0" ] && status=1
		else
			result="FAILED (skipped, although the diagnostic did not show that the runner refuses Apple Events)"
			echo "::error title=e2e skipped::the stub-app tests were skipped, so nothing was tested; see the e2e log and the Apple Events diagnostic"
			[ "$status" = "0" ] && status=1
		fi
	fi
	summary "### Stub-app end-to-end tests" "" "- sandbox: \`$sandbox\`" "- result: $result" ""
	exit "$status"
}

case "${1:-all}" in
prepare) do_prepare ;;
probe)
	shift
	do_probe "${1:-probe}"
	;;
grant-tcc) do_grant_tcc ;;
run) do_run ;;
all)
	do_prepare
	do_run
	;;
*)
	echo "usage: $0 [prepare | probe LABEL | grant-tcc | run]" >&2
	exit 2
	;;
esac
