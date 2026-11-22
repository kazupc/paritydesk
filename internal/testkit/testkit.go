package testkit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
)

// Canary is planted in token-bearing fields; tests assert it never reaches
// logs, state, generations, errors or --json output.
const Canary = "PARITYDESK-CANARY-TOKEN-7f3c"

// LSTimestamp is the envelope timestamp of every LSS item AddFolder
// writes.
const LSTimestamp int64 = 1700000000000

// LSTabID is the envelope tabId AddFolder writes.
const LSTabID = "tab-0001"

// StoreVersion is the persisted-store version AddFolder writes for
// dframe-store and the started-environments map.
const StoreVersion = 1

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
	// LS adds raw Local Storage items by name. They are written last and
	// replace generated items of the same name.
	LS map[string]string

	// SidebarView is the scope object of dframe-code-sections for the
	// replica (mirror, LSS envelope and dframe-store).
	SidebarView map[string]any
	// Backlog and ScheduledTasks fill backlog/tasks.json and
	// scheduled-tasks.json when non-nil.
	Backlog        []map[string]any
	ScheduledTasks []map[string]any
	// WaitingInput holds files under waiting-input/, by relative path.
	WaitingInput map[string][]byte
	// StartedEnvironments maps session ids to environment ids
	// (persisted.ccd-started-environments).
	StartedEnvironments map[string]string
	// TmpRecords holds app-written local_<id>.json.tmp files by session N.
	TmpRecords map[int]map[string]any
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

// AddFolder writes a folder from spec and returns its path.
func (d *DataRoot) AddFolder(t testing.TB, spec FolderSpec) string {
	t.Helper()
	if spec.Name == "" {
		spec.Name = "Claude"
	}
	folder := filepath.Join(d.Dir, spec.Name)
	w := &writer{t: t, folder: folder}
	w.mkdir(".")
	w.config(spec)
	w.sessions(spec)
	w.tasks(spec)
	ls := map[string]string{}
	w.presets(spec, ls)
	w.mirror(spec, ls)
	if spec.Worktrees != nil {
		w.json("git-worktrees.json", map[string]any{"worktrees": spec.Worktrees, "untrackedDirGc": map[string]any{}, "pendingTombstones": []any{}}, true)
	}
	if spec.WindowState != nil {
		w.file("window-state.json", spec.WindowState, 0o644)
	}
	for k, v := range spec.LS {
		ls[k] = v
	}
	if len(ls) > 0 {
		WriteLS(t, folder, ls)
	}
	return folder
}

type writer struct {
	t      testing.TB
	folder string
}

func (w *writer) mkdir(rel string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Join(w.folder, filepath.FromSlash(rel)), 0o700); err != nil {
		w.t.Fatal(err)
	}
}

func (w *writer) file(rel string, data []byte, mode fs.FileMode) {
	w.t.Helper()
	p := filepath.Join(w.folder, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, data, mode); err != nil {
		w.t.Fatal(err)
	}
}

func (w *writer) json(rel string, v any, indent bool) {
	w.t.Helper()
	w.file(rel, mustJSON(w.t, v, indent), 0o644)
}

func mustJSON(t testing.TB, v any, indent bool) []byte {
	t.Helper()
	var b []byte
	var err error
	if indent {
		b, err = json.MarshalIndent(v, "", "  ")
	} else {
		b, err = json.Marshal(v)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (w *writer) config(spec FolderSpec) {
	cfg := map[string]any{"lastKnownAccountUuid": string(spec.Replica.Account), "locale": "en-US"}
	if spec.WithToken {
		cfg["oauth:tokenCacheV2"] = Canary
	}
	w.json("config.json", cfg, true)
}

func (w *writer) sessDir(spec FolderSpec) string {
	return "claude-code-sessions/" + string(spec.Replica.Account) + "/" + string(spec.Replica.Org)
}

func record(s Session) map[string]any {
	rec := map[string]any{
		"sessionId":      FakeSession(s.N),
		"title":          s.Title,
		"isArchived":     s.Archived,
		"lastActivityAt": s.LastActivityAt,
		"cwd":            "/tmp/project",
	}
	if s.PermissionMode != "" {
		rec["permissionMode"] = s.PermissionMode
	}
	if s.EnvScopeID != "" {
		rec["envScopeId"] = s.EnvScopeID
	}
	if s.Worktree != "" {
		rec["worktreePath"] = s.Worktree
	}
	if s.CLISessionID != "" {
		rec["cliSessionId"] = s.CLISessionID
	}
	for k, v := range s.Extra {
		rec[k] = v
	}
	return rec
}

func (w *writer) sessions(spec FolderSpec) {
	dir := w.sessDir(spec)
	w.mkdir(dir)
	archived := []string{}
	for _, s := range spec.Sessions {
		w.json(dir+"/"+FakeSession(s.N)+".json", record(s), true)
		if s.Archived {
			archived = append(archived, FakeSession(s.N))
		}
	}
	for n, rec := range spec.TmpRecords {
		w.json(dir+"/"+FakeSession(n)+".json.tmp", rec, false)
	}
	for n, at := range spec.Tombstones {
		rel := dir + "/deleted_" + FakeUUID(n)
		w.json(rel, map[string]any{"deletedAt": at}, false)
		mt := time.UnixMilli(at)
		if err := os.Chtimes(filepath.Join(w.folder, filepath.FromSlash(rel)), mt, mt); err != nil {
			w.t.Fatal(err)
		}
	}
	sort.Strings(archived)
	w.json(dir+"/archived-sessions.idx", struct {
		V        int      `json:"v"`
		Archived []string `json:"archived"`
	}{1, archived}, false)
	for rel, data := range spec.WaitingInput {
		w.file(dir+"/waiting-input/"+rel, data, 0o600)
	}
}

func (w *writer) tasks(spec FolderSpec) {
	dir := w.sessDir(spec)
	if spec.Backlog != nil {
		w.json(dir+"/backlog/tasks.json", map[string]any{"items": spec.Backlog, "version": 1}, true)
	}
	if spec.ScheduledTasks != nil {
		w.json(dir+"/scheduled-tasks.json", map[string]any{"scheduledTasks": spec.ScheduledTasks, "lastCheck": 1}, true)
	}
}

func (w *writer) presets(spec FolderSpec, ls map[string]string) {
	acct := string(spec.Replica.Account)
	if spec.Presets != nil {
		state := map[string]any{"presets": spec.Presets, "placeDefaults": map[string]any{}, "seededFromDraft": true}
		ls["persisted.ccd-session-presets."+acct] = string(mustJSON(w.t, map[string]any{"state": state, "version": 0}, false))
		file := map[string]any{"byAccount": map[string]any{acct: map[string]any{"presets": spec.Presets, "seededFromDraft": true}}}
		w.file("ccd-session-presets.json", mustJSON(w.t, file, true), 0o600)
	}
	if spec.StartedEnvironments != nil {
		st := map[string]any{"state": map[string]any{"bySessionId": spec.StartedEnvironments}, "version": StoreVersion}
		ls["persisted.ccd-started-environments"] = string(mustJSON(w.t, st, false))
	}
}

func envelope(t testing.TB, v any) string {
	return string(mustJSON(t, map[string]any{"value": v, "tabId": LSTabID, "timestamp": LSTimestamp}, false))
}

// mirror writes claude_desktop_config.json and the LS copies of every
// mirrored key.
func (w *writer) mirror(spec FolderSpec, ls map[string]string) {
	if spec.AppPrefs == nil && spec.GroupOrder == nil && spec.PinnedOrder == nil && spec.SidebarView == nil && spec.MCPServers == nil {
		return
	}
	acct := string(spec.Replica.Account)
	scope := acct + "/" + string(spec.Replica.Org)
	prefs := map[string]any{}
	for k, v := range spec.AppPrefs {
		prefs[k] = v
	}
	ep := map[string]any{"claude-code-preferred-editor": "default"}
	store := map[string]any{"lastSeen": 1}
	if spec.GroupOrder != nil {
		ep["code-projects-order."+acct] = spec.GroupOrder
		ls["LSS-persisted.code-projects-order."+acct] = envelope(w.t, spec.GroupOrder)
	}
	if spec.PinnedOrder != nil {
		slice := map[string]any{"pinnedOrder": spec.PinnedOrder, "collapsedGroups": []string{}}
		ep["dframe-local-slice"] = slice
		ls["LSS-persisted.dframe-local-slice"] = envelope(w.t, slice)
		store["pinnedOrder"] = spec.PinnedOrder
	}
	if spec.SidebarView != nil {
		secs := map[string]any{scope: spec.SidebarView}
		ep["dframe-code-sections"] = secs
		ls["LSS-persisted.dframe-code-sections"] = envelope(w.t, secs)
		store["codeSidebarByScope"] = secs
	}
	if spec.PinnedOrder != nil || spec.SidebarView != nil {
		ls["dframe-store"] = string(mustJSON(w.t, map[string]any{"state": store, "version": StoreVersion}, false))
	}
	prefs["epitaxyPrefs"] = ep
	cfg := map[string]any{"preferences": prefs}
	if spec.MCPServers != nil {
		cfg["mcpServers"] = spec.MCPServers
	}
	w.json("claude_desktop_config.json", cfg, true)
}

// Tree returns every file under the data root with its sha256, for golden
// comparisons and byte-preservation assertions.
func (d *DataRoot) Tree(t testing.TB) map[string]string {
	t.Helper()
	return TreeOf(t, d.Dir)
}

// TreeOf returns every regular file under dir (relative slash path) with
// its sha256.
func TreeOf(t testing.TB, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		h := sha256.Sum256(b)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(h[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// LSValue encodes a text the way Chromium stores it.
func LSValue(text string) []byte { return localstorage.EncodeString(text) }
