// Package orchestrator runs the switch phases P0–P12 over state intents,
// and the commands built from the same parts: plan (preview), sync,
// verify, recover, rollback, rebase, status, doctor, init and the profile
// lifecycle.
//
// Every dependency is an interface declared here, so the whole state
// machine is tested with fakes: no real app, folder or launchd is needed.
// Compile-time assertions below keep the real implementations in step
// with these contracts.
package orchestrator
