// Package e2e holds end-to-end tests that drive the real paritydesk
// binary against a stub app (testdata/stubapp) in a temporary HOME. They
// run only on CI, in the e2e job of .github/workflows/ci.yml, through
// scripts/ci/e2e.sh.
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
// The package has no production code besides CheckTarget and RealHome,
// which the tests use.
package e2e
