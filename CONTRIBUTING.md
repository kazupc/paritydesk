# Contributing

Thank you for helping. paritydesk edits application data, so the bar for
changes is correctness first.

## Developer Certificate of Origin

Every commit must be signed off under the
[Developer Certificate of Origin 1.1](https://developercertificate.org/):
by adding a `Signed-off-by:` line with your name and the email you commit
with, you certify that you wrote the change or have the right to submit
it under the project's MIT license. `git commit -s` adds the line. Pull
requests with unsigned commits cannot be merged.

## Before you open a pull request

```
gofmt -l .            # must print nothing
go vet ./...
go test -race ./...
```

CI runs the same checks on Apple silicon and Intel, plus `govulncheck`
and the extra jobs described under [Testing](#testing).
The policy tests in `internal/policy` enforce project rules: no network
packages, external commands only in `internal/app/exec.go`, no advice to
end an account's session, no credential stores, only fake ids in
fixtures, and no non-ASCII letters in text. A few non-ASCII symbols
(for example the ellipsis in masked ids) remain; prefer ASCII in new
text.

## Testing

All scripts named here live in `scripts/ci/` and are POSIX `sh`.

### Unit tests

```
go test -race -count=1 ./...
```

Unit tests use synthetic fixtures in temporary directories only. They
are safe to run on any Mac. On Linux, the packages printed by
`scripts/ci/linux-packages.sh` run (the others drive the macOS-only
filesystem layer):

```
go test -race -count=1 $(scripts/ci/linux-packages.sh)
```

`scripts/ci/check-gofmt.sh` and `scripts/ci/check-no-network.sh` run the
gofmt check and the `go list -deps` check for `net`, `net/http` and
`crypto/tls` (darwin and linux, with and without tests). Tests may not
import `net` either; the network-deny probe uses raw syscalls.

The tests in `internal/cli` run `shellcheck` on the integration scripts
and the generated bash completion when it is installed
(`brew install shellcheck`); CI installs it.

### Fuzz targets

Fuzz targets cover the Local Storage codec, the JSON editor, the hook
payload parser, the table checker, plan composition and the output
sanitizers. List them with `grep -rn '^func Fuzz' internal` and run one
at a time:

```
go test -run '^$' -fuzz '^FuzzDecode$' -fuzztime 60s ./internal/localstorage/
```

A failing input is saved under the package's `testdata/fuzz/`; commit it
with the fix so it stays a regression test.

### Differential LevelDB test

`internal/store/kv/leveldb/differential_test.go` compares paritydesk's
LevelDB code with the C++ LevelDB library in both directions: databases
written by C++ (with and without Snappy, many keys, deletions and
compactions) are read through the snapshot path and the table checker,
and a batch written through `OpenWritable` is read back by C++. The C++
library usually leaves a journal whose first batches are numbered below
its manifest's last sequence, as the app may leave its own database on
quit; the tests log it, and both the snapshot path and `OpenWritable`
must read every key of that database as C++ left it. The tests also
check that C++ reads the state a crash leaves right after `OpenWritable`
added its replay journal, that a retry adds no second one, and that an
unfinished table file the manifest does not list (as C++ leaves when it
closes during a compaction) does not stop `OpenWritable`. The C++
helper is built from `testdata/leveldb-cxx/`. Without
`PARITYDESK_LEVELDB_CXX` the tests skip.

```
brew install leveldb snappy
scripts/ci/build-leveldb-cxx.sh "$TMPDIR/leveldb-cxx"
PARITYDESK_LEVELDB_CXX="$TMPDIR/leveldb-cxx" go test -count=1 -v -run TestDifferential ./internal/store/kv/leveldb/
```

The CI job "leveldb differential" does the same.

### Network-deny run

On macOS, CI runs every test binary under `sandbox-exec` with all
network access denied:

```
PARITYDESK_NONET_PROBE=1 go test -count=1 -exec "$PWD/scripts/ci/nonet-exec.sh" ./...
```

`scripts/ci/nonet-exec.sh` fails when `sandbox-exec` is missing.
`TestNetworkDenied` in `internal/e2e` runs only with
`PARITYDESK_NONET_PROBE=1` and proves that the sandbox works: a loopback
listen, a TCP connect and a UDP connect must fail with a permission
error.

### End-to-end job (CI only)

The e2e job builds a stub app from `testdata/stubapp/StubApp.swift` with
the test bundle id `dev.paritydesk.stubapp`, installs it as
`Applications/Claude.app` inside a temporary HOME, and drives the real
`paritydesk` binary against it, under the network-deny sandbox when the
runner delivers Apple Events there (see below). The tests
in `internal/e2e` cover a switch from a to b and back, `recover` after a
run stopped at each phase or killed with SIGKILL, waiting for a vetoed
quit, and a vetoed quit that times out without writing anything. The
stub answers the quit Apple Event through NSApplication, can veto a
quit for a set number of seconds, holds the LevelDB lock and an open
file in its data folder, and writes main.log lines in the formats of
`internal/compat/manifests/6.x.toml`.

The job runs `scripts/ci/e2e.sh` in steps:

1. `e2e.sh prepare` builds paritydesk, the test binary and the stub and
   installs the stub in the temporary HOME.
2. `e2e.sh probe LABEL` is the Apple Events diagnostic. It launches the
   stub once and sends it two harmless events (`reopen`, `count
   windows`) from `osascript`: outside any sandbox, under an allow-all
   `sandbox-exec` profile and under the network-deny profile, with a
   Finder `count windows` as a control. It prints each exit code and
   AppleScript error number with a class (`ok`, `delivered(N)`,
   `blocked-tcc(N)`, `blocked-privilege(N)`, `timeout(N)`,
   `not-running(N)`, `error(...)`), the AppleEvents rows of the runner
   user's TCC.db and the matching TCC, appleeventsd and sandbox log
   lines, and writes a table and a conclusion to the job summary.
3. Only when the stub launched but Apple Events did not reach it outside
   the sandbox, `e2e.sh grant-tcc` adds Automation rows for the stub's
   bundle id to the runner user's TCC.db (GitHub-hosted runners only, in
   the runner image's own format), and the diagnostic runs again.
4. `e2e.sh run` runs the tests. The last diagnostic decides the sandbox:
   under the network-deny sandbox when Apple Events reach the stub there
   (`PARITYDESK_E2E_NONET=1`), otherwise without `sandbox-exec`, which
   the log and the summary state. The paritydesk runs the tests start
   are then not network-denied, and no other job drives the built
   binary under the sandbox: the checks job only proves that no network
   package is linked (`go list -deps`), and the network-deny run of the
   macOS test job skips the stub-app tests. `e2e.sh` with no argument
   runs `prepare` and `run` without the diagnostic, so it uses the
   sandbox only when `PARITYDESK_E2E_NONET=1` is set.

If the stub-app tests are skipped, the job fails, since nothing was
tested, with one exception that needs two things. The diagnostic must
show that the runner refuses Apple Events to the running stub outside
any sandbox in a way the stub cannot cause: both stub events refused by
Automation (`blocked-tcc`), or both refused with -10004 while the
Finder control is refused the same way. A timeout, a crash or a -10004
for the stub alone never counts, since a hung or broken stub looks the
same. And a maintainer must have set the repository variable
`PARITYDESK_E2E_ALLOW_SKIP` to `1`. The skip is then a warning and a
"NOT RUN" line in the job summary. That is a limit of GitHub-hosted
runners, not a pass: before every release, the stub-app tests must then
be run on a self-hosted runner, or on a Mac or macOS virtual machine
that has no `/Applications/Claude.app` and no real data, as
`docs/release-checklist.md` describes (`e2e.sh` refuses to run while
`/Applications/Claude.app` exists, so another user account on a Mac
with the real app is not enough). Never run them on a Mac that has the
real app or real data. The detached
switch, `profile add` and
`rollback` are not covered yet. The stub stands in for the real app;
passing e2e runs do not show that paritydesk works with the real app.
Those checks are the steps of `docs/release-checklist.md`.

**The e2e tests must never target the real app.** They:

- run only when `PARITYDESK_E2E=1` and `PARITYDESK_E2E_BUNDLE_ID` are set,
  and `scripts/ci/e2e.sh` runs only when `CI=true`;
- refuse the real app's bundle id (`com.anthropic.claudefordesktop`, or
  any `com.anthropic.` id), a HOME that is the real user's home, and a
  machine where `/Applications/Claude.app` exists;
- check that the bundle they drive is the stub before sending it any
  Apple Event.

Never set `PARITYDESK_E2E=1` on a Mac you use, and never point the
tests or the stub at real data folders. The stub itself refuses to start
with the real bundle id or with paths under the real
`~/Library/Application Support/Claude*` or `~/Library/Logs/Claude`.

## Rules for tests and fixtures

- Tests never touch the real app or its data: use `t.TempDir()`, the
  `testkit` data roots and the fakes. Constructors refuse real paths under
  `go test`. No test needs the real app: checks against it are steps of
  `docs/release-checklist.md`, and the e2e tests (`PARITYDESK_E2E=1`, CI
  only) drive the stub app.
- Use only `testkit.FakeUUID(n)` ids (`00000000-0000-4000-8000-...`).
  Never commit real account, organization or session ids, names, email
  addresses, folder or project names, or anything copied from the app
  bundle.
- Personal labels can be checked locally with a deny-list that never
  enters the repository: put one label per line in
  `testdata/private/pii.txt` (git-ignored) or point
  `PARITYDESK_PII_DENYLIST_FILE` at a file elsewhere.

## Code style

Idiomatic Go, small functions, doc comments on exported identifiers,
errors wrapped with context, table-driven tests. Code comments may cite
the decision, finding and spike ids indexed in
[docs/architecture.md](docs/architecture.md). Every message a person
reads comes from the catalog in `internal/i18n` (see "Adding a message"
below); command short texts and flag usages are catalog ids too. Printed
text must stay ASCII-safe under `--ascii` (paritydesk then prints English).

## Capability claims

The README claims a capability only after a runtime spike on the real app
is recorded in `docs/adr/0003-online-reconcile.md` (decision D26).

## Writing a log signal pattern

Signal patterns in `internal/compat/manifests/*.toml` (`[signals]`) are
Go regular expressions matched only on record lines of Claude's log (a
line that starts with `<timestamp> [<level>] `), only against the
message after that header, and anchored at its start by the loader. So:

- write the pattern from the first character of the message, and never
  start it with `^`, `.*` or `.+`;
- give it a fixed text of at least 8 characters that every match
  contains (`compat.RequiredLiteral`; the reader skips lines without it);
- use `{session}` at most once (it becomes a group that captures a
  validated `local_<uuid>`), and a named group where the key needs one
  (`reason`, `from`/`to`, `state`, `org`/`account`);
- mark it "observed" or "from code" in its comment, and test it in
  `internal/compat` against a message built from the app's text, with
  fake ids only. Continuation lines of multi-line messages are never
  matched, because they can hold text Claude does not control.

The native open-file scan (`internal/fdscan`) calls libSystem through
assembly trampolines and a linknamed `syscall.syscall6`; if a Go release
ever refuses that, build with `-tags pd_nofdscan` to compile the stub
(paritydesk then uses `lsof`), and keep every native-only file behind
`//go:build darwin && !pd_nofdscan` (the policy tests check it).

## Adding a message

Every text a person reads is a catalog message; the rules and the tests
are in [docs/dev/i18n.md](docs/dev/i18n.md). In short:

- Add the id and the English text to `internal/i18n/catalog.go`, and the
  Japanese to `internal/i18n/catalog_ja.go` at the same place (the files
  keep the same order; the style rules and the glossary are the comment
  at the top of `catalog_ja.go`). `hook.*`, `guard.*` and `notify.*` ids
  are English only.
- Pass values as arguments of a format, never by joining strings, and
  keep catalog text out of `Printer.Line` (use `Printer.Msg` or
  `Printer.Text`; `Line` takes a constant format for data).
- Errors a person can act on use `pderr.NewT` or `pderr.WrapT`; a broken
  invariant is `pderr.Bug` or `pderr.Bugf` (English). A package-level
  sentinel a person reads is an `i18n.NewErr`.
- Fields that reach people (`Detail`, `Message`, `Warnings`, `Lines`,
  `Actions`, `Problem`) hold `i18n.Text`; JSON encodes them in English.
- Run `go test ./internal/i18n ./internal/policy` (completeness, verbs,
  layout, protected tokens, style, order) and regenerate the goldens with
  `go test ./internal/cli ./internal/render -run Golden -update`, then read
  the diff, including `testdata/ja/`.
- Tests write Japanese only as `\u` escapes; only `catalog_ja.go`,
  `README.ja.md` and the `testdata/ja/` goldens hold Japanese letters.
- Maintainers add the kana and kanji forms of every personal label to the
  private deny-list too: the Japanese check compares every window of the
  Japanese files with them.
