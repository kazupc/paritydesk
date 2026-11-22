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
//     strings, and no tracked file contains non-ASCII letters (PII guard);
//   - no tracked file contains a personal label from the deny-list. The
//     deny-list is never in the repository: it is read at test time from
//     the file named by PARITYDESK_PII_DENYLIST_FILE, or from the
//     git-ignored testdata/private/pii.txt, and the test is skipped when
//     neither exists. Labels are compared as sha256 hashes of lower-cased
//     tokens, so even failure messages never print them;
//   - no _test.go file references os.UserHomeDir or /Applications.
//
// The credential-store rule allowlists internal/compat/manifests/ and
// internal/denylist/ (the fixed deny floor) and this package.
package policy
