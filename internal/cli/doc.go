// Package cli defines the paritydesk command line on the standard library
// flag package. It parses flags into options, builds the orchestrator's
// dependencies from the real implementations (wire.go), and renders
// results: human lines and tables, or one JSON document with --json.
//
// Commands that switch, sync, plan, verify or recover map to one
// orchestrator.Service call. A few commands are local and run without the
// orchestrator: generations and backups (gen), config, log and history
// (the JSON-lines run logs), uninstall (paritydesk's own files only), and
// the plugin's hook entry point (package hook). Tests replace the outside
// world through the unexported system type, with a fake Service.
package cli
