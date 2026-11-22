# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's private
vulnerability reporting ("Report a vulnerability" on the repository's
Security tab). Do not open a public issue for a security problem.

Include the paritydesk version (`paritydesk version`), the macOS version,
the Claude desktop app version, and the steps to reproduce. Never include
tokens, account or organization ids, session contents, or a copy of a
Claude data folder: paritydesk never needs them, and `paritydesk doctor`
output is redacted for this purpose.

You can expect an acknowledgement within a week. Fixes are released as
patch versions and credited in the CHANGELOG unless you prefer otherwise.

## Problems in Claude itself

paritydesk is unofficial. A vulnerability in the Claude desktop app,
Claude Code or any other Anthropic software belongs to Anthropic; report
it through Anthropic's own disclosure program, not here.

## Design rules that limit the impact of bugs

- No network access: no package in the dependency graph is `net`,
  `net/http` or `crypto/tls`, enforced by a test.
- No credential access: paritydesk never reads browser cookie stores,
  saved-password databases, the Keychain or OAuth tokens. A fixed deny
  floor in the code refuses any plan that would touch them.
- One place runs external programs (`internal/app/exec.go`), with fixed
  argument templates and validated arguments, never through a shell.
- The app is quit only with an Apple Event; paritydesk never sends it a
  signal.
- Every write is preceded by a backup generation; data folders are never
  created, merged or deleted.
- Text from app data (session titles, group names, paths) is sanitized
  before it reaches a terminal, and the SessionStart hook never prints
  untrusted text.
