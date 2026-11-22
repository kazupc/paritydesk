// Package config loads and validates the user's config.toml (D21) and resolves paritydesk's own directories.
//
// The config file holds user choices only. It never holds account or
// organization ids (those are discovered and live in state), so it can be
// pasted into an issue safely.
package config
