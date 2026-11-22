// Package policy holds tests that enforce project-wide rules. They run with `go test ./...`:
//
//   - no package outside an explicit allowlist imports net, net/http or
//     crypto/tls (zero network, D22);
//   - exec.Command / exec.CommandContext appear only in internal/app/exec.go;
//   - leveldb.Recover and RecoverFile are never referenced;
//   - no user-facing string advises signing out ("sign out", "log out",
//     "logout"), with an allowlist for this very rule's text;
//   - no reads of credential stores (Cookies, Login Data,
//     find-generic-password, CLAUDE_CODE_OAUTH_TOKEN) outside an allowlist;
//   - fixtures contain only allow-listed fake UUIDs and no email-like
//     strings, and no file contains real account labels or non-ASCII
//     letters from personal data (PII guard).
package policy
