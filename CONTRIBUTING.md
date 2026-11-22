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
`paritydesk` binary against it under the network-deny sandbox. The tests
in `internal/e2e` cover a switch from a to b and back, `recover` after a
run stopped at each phase or killed with SIGKILL, waiting for a vetoed
quit, and a vetoed quit that times out without writing anything. The
stub answers the quit Apple Event through NSApplication, can veto a
quit for a set number of seconds, holds the LevelDB lock and an open
file in its data folder, and writes main.log lines in the formats of
`internal/compat/manifests/6.x.toml`.

The job runs `scripts/ci/e2e.sh`. If the stub-app tests are skipped
(for example because the runner blocks Apple Events), the job fails,
since nothing was tested; set `PARITYDESK_E2E_ALLOW_SKIP=1` only for a
runner known to block them. The detached switch, `profile add` and
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
[docs/architecture.md](docs/architecture.md). Every message that
`internal/cli` and `internal/render` print goes through `internal/i18n`;
command and flag help text lives in the command tree in `internal/cli`.
The progress, doctor and recover details built in `internal/orchestrator`
are still English-only strings and move into the catalog with the v0.2
translations. Printed text must stay ASCII-safe under `--ascii`.

## Capability claims

The README claims a capability only after a runtime spike on the real app
is recorded in `docs/adr/0003-online-reconcile.md` (decision D26).
