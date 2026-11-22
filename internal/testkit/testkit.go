package testkit

import (
	"fmt"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// Canary is planted in token-bearing fields; tests assert it never reaches
// logs, state, generations, errors or --json output.
const Canary = "PARITYDESK-CANARY-TOKEN-7f3c"

// FakeUUID returns the n-th allow-listed fake UUID
// (00000000-0000-4000-8000-<12 hex digits of n>).
func FakeUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

// FakeSession returns the n-th fake local session id.
func FakeSession(n int) string { return "local_" + FakeUUID(n) }

// Session is one synthetic session record.
type Session struct {
	N              int // FakeSession(N)
	Title          string
	Archived       bool
	LastActivityAt int64
	PermissionMode string
	EnvScopeID     string
	Worktree       string
	CLISessionID   string
	Extra          map[string]any // unknown fields to prove preservation
}

// FolderSpec describes one data folder.
type FolderSpec struct {
	Name        string // "Claude" or "Claude-<x>"
	Replica     model.Replica
	WithToken   bool // plant Canary in config.json
	Sessions    []Session
	Tombstones  map[int]int64 // session N -> deletion time
	Presets     []map[string]any
	GroupOrder  []string
	PinnedOrder []string
	AppPrefs    map[string]any
	MCPServers  map[string]any
	Worktrees   map[string]any
	WindowState []byte
	// LS adds raw Local Storage items by name.
	LS map[string]string
}

// DataRoot is a temporary data root.
type DataRoot struct {
	Dir string
}

// NewDataRoot creates an empty data root in t.TempDir().
func NewDataRoot(t testing.TB) *DataRoot {
	t.Helper()
	return &DataRoot{Dir: t.TempDir()}
}

// AddFolder writes a folder from spec.
func (d *DataRoot) AddFolder(t testing.TB, spec FolderSpec) string {
	t.Helper()
	t.Fatal(pderr.ErrNotImplemented)
	return ""
}

// Tree returns every file under the data root with its sha256, for golden
// comparisons and byte-preservation assertions.
func (d *DataRoot) Tree(t testing.TB) map[string]string {
	t.Helper()
	t.Fatal(pderr.ErrNotImplemented)
	return nil
}
