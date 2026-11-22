// Package cli defines the paritydesk command line on the standard library
// flag package. It parses flags, builds the orchestrator's dependencies
// from the real implementations (wire.go), and renders results. It holds
// no logic of its own: every command maps to one orchestrator, gen or hook
// call.
package cli
