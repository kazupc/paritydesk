// Package pderr defines paritydesk's exit codes, its error type and the
// error catalog.
//
// Exit codes are part of the public API (SemVer). Every code maps to a
// catalog entry PD-E<code> that says what happened, what is safe right now,
// the exact next command, and a docs anchor. No catalog text may advise
// signing out of an account.
package pderr
