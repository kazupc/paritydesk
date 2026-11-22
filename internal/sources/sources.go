package sources

import (
	"context"
	"path/filepath"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// Well-known paths inside a data folder.
const (
	SessionsRoot      = "claude-code-sessions"
	LevelDBDir        = "Local Storage/leveldb"
	DesktopConfigFile = "claude_desktop_config.json"
	MainLogName       = "main.log"
)

// SessionsDir returns <folder>/claude-code-sessions/<acct>/<org>.
func SessionsDir(folder string, r model.Replica) string {
	return filepath.Join(folder, SessionsRoot, string(r.Account), string(r.Org))
}

// LevelDBPath returns <folder>/Local Storage/leveldb.
func LevelDBPath(folder string) string { return filepath.Join(folder, LevelDBDir) }

// ReadContext is what an adapter may read for one replica.
type ReadContext struct {
	Folder  string        // data folder root
	Replica model.Replica // the replica being read
	// LS is a Local Storage reader: a snapshot copy for previews, or the
	// quiesced original after P4. May be nil when the folder has no LS.
	LS kv.Reader
	// Spec is the row's manifest spec with templates expanded for Replica.
	Spec compat.RowSpec
	// Manifest gives access to classification and globs.
	Manifest *compat.Manifest
}

// OpsContext adds what producing ops needs.
type OpsContext struct {
	ReadContext
	// Now stamps envelope timestamps paritydesk writes; every such stamp
	// is reported back through Written so it never wins a tie-break (D7).
	NowMillis int64
	// Source is the leaving replica's read context, used to copy raw bytes
	// (for example secret values) directly between folders (D27).
	Source *ReadContext
}

// Adapter reads and writes one row.
type Adapter interface {
	Row() model.RowID
	// Read returns the row's snapshot. A row whose files and keys are all
	// absent returns an empty snapshot, not an error. Malformed data is an
	// error naming the file (never its content).
	Read(ctx context.Context, rc ReadContext) (*model.RowSnapshot, error)
	// Ops returns the operations that turn current into desired on the
	// folder of oc. current must be what Read returned for that folder.
	Ops(ctx context.Context, oc OpsContext, current, desired *model.RowSnapshot) (OpsResult, error)
}

// OpsResult is what Ops returns.
type OpsResult struct {
	Ops []plan.Op
	// WrittenStamps lists the app-time values paritydesk wrote, by item
	// key, so state can record them as tool-written.
	WrittenStamps map[string]int64
}

// Registry maps rows to adapters.
type Registry map[model.RowID]Adapter

// SessionInfo is the subset of a session record used outside row merges.
type SessionInfo struct {
	ID           ids.SessionID
	CLISessionID string // the Claude Code session id, if recorded
	Title        string // untrusted; sanitize before rendering
	Archived     bool
	Worktree     string // absolute path, if the session uses a worktree
}

// ListSessions reads every session record of a replica without writing.
// Used to resolve the invoking session for the relaunch link and to list
// busy sessions.
func ListSessions(ctx context.Context, folder string, r model.Replica) ([]SessionInfo, error) {
	return nil, pderr.ErrNotImplemented
}

// ResolveByCLISession finds the local session whose record carries the
// given Claude Code session id.
func ResolveByCLISession(ctx context.Context, folder string, r model.Replica, cliSessionID string) (ids.SessionID, error) {
	return "", pderr.ErrNotImplemented
}

// DiscoverReplicas lists the (account, org) directories under
// <folder>/claude-code-sessions. Only well-formed UUID directory names are
// returned.
func DiscoverReplicas(folder string) ([]model.Replica, error) {
	return nil, pderr.ErrNotImplemented
}

// Directory exposes the session helpers behind methods for the
// orchestrator.
type Directory struct{}

// ListSessions calls the package function.
func (Directory) ListSessions(ctx context.Context, folder string, r model.Replica) ([]SessionInfo, error) {
	return ListSessions(ctx, folder, r)
}

// ResolveByCLISession calls the package function.
func (Directory) ResolveByCLISession(ctx context.Context, folder string, r model.Replica, cliSessionID string) (ids.SessionID, error) {
	return ResolveByCLISession(ctx, folder, r, cliSessionID)
}

// DiscoverReplicas calls the package function.
func (Directory) DiscoverReplicas(folder string) ([]model.Replica, error) {
	return DiscoverReplicas(folder)
}
