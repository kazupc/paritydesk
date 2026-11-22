// Package e2e holds end-to-end tests that drive the real paritydesk
// binary against a stub app (testdata/stubapp) in a temporary HOME. They
// run only on CI, in the e2e job of .github/workflows/ci.yml, through
// scripts/ci/e2e.sh. In that job the script's Apple Events diagnostic
// (e2e.sh probe) runs first, and its result decides whether the tests run
// under the network-deny sandbox and whether a skip may be reported
// instead of failed (only if a maintainer also allows it). e2e.sh with no
// argument runs no diagnostic and uses the sandbox only when
// PARITYDESK_E2E_NONET=1.
//
// The tests are skipped unless PARITYDESK_E2E=1 and
// PARITYDESK_E2E_BUNDLE_ID are both set. They refuse to run (CheckTarget)
// when the bundle id is the real app's, or when HOME is the real user's
// home: they must never quit, launch or read the real app or its data.
// The stub must be installed at Applications/Claude.app inside that
// temporary HOME, because paritydesk looks for a bundle with that file
// name (/Applications is not searched by the tests; scripts/ci/e2e.sh
// refuses to run when /Applications/Claude.app exists).
//
// This package also holds the network-deny probe run by the macOS test
// job under scripts/ci/nonet-exec.sh (PARITYDESK_NONET_PROBE=1).
//
// Besides the tests, the package holds only CheckTarget and RealHome.
// They are production code and ship in the release binary: internal/cli
// imports them to guard the test-only fault PARITYDESK_FAULT=pause:P7
// (pause inside P7 after the first write, to be killed by TestStubApp),
// which it honours only when PARITYDESK_E2E=1 is set, CheckTarget accepts
// the configured bundle id and $HOME (as given and with symlinks
// resolved), and the data root lies inside $HOME. Keep them free of
// test-only dependencies.
package e2e
