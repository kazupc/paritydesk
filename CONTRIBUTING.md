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

CI runs the same checks on Apple silicon and Intel, plus `govulncheck`.
The policy tests in `internal/policy` enforce project rules: no network
packages, external commands only in `internal/app/exec.go`, no advice to
end an account's session, no credential stores, only fake ids in
fixtures, and ASCII-only text.

## Rules for tests and fixtures

- Tests never touch the real app or its data: use `t.TempDir()`, the
  `testkit` data roots and the fakes. Constructors refuse real paths under
  `go test`. Anything that needs the real app is skipped unless
  `PARITYDESK_E2E=1` and belongs in `docs/release-checklist.md`.
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
