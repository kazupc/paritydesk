package testkit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
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

// Session is one synthetic session record. Records carry the member names
// and nesting observed on app 6.28899.2 (ADR 0004).
type Session struct {
	N              int // FakeSession(N)
	Title          string
	Archived       bool
	LastActivityAt int64
	PermissionMode string
	EnvScopeID     string
	// Worktree is the worktree's absolute path; the record then also
	// names the worktree (its base name) and its branches.
	Worktree     string
	CLISessionID string
	Extra        map[string]any // more fields, or unknown fields to prove preservation
}

// Decoy is another account's session directory left in a data folder
// (claude-code-sessions/<acct>/<org> of a replica that is not the
// folder's). Adapters must never read, merge or write it.
type Decoy struct {
	Replica  model.Replica
	Sessions []Session
	// Tombstones maps session N to deletion time, as in FolderSpec.
	Tombstones map[int]int64
}

// FolderSpec describes one data folder.
type FolderSpec struct {
	Name        string // "Claude" or "Claude-<x>"
	Replica     model.Replica
	WithToken   bool // plant Canary in config.json
	Sessions    []Session
	Tombstones  map[int]int64 // session N -> deletion time (a bare JSON number)
	Presets     []map[string]any
	GroupOrder  []string
	PinnedOrder []string
	AppPrefs    map[string]any
	MCPServers  map[string]any
	// Worktrees are git-worktrees.json entries keyed by worktree name.
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
	// WaitingInput holds files under waiting-input/, by relative path. The
	// directory itself is always created, as the app does.
	WaitingInput map[string][]byte
	// StartedEnvironments maps session ids to environment ids
	// (persisted.ccd-started-environments).
	StartedEnvironments map[string]string
	// TmpRecords holds app-written local_<id>.json.tmp files by session N.
	TmpRecords map[int]map[string]any
	// Decoys are other accounts' session directories in this folder.
	Decoys []Decoy
	// Extras are top-level files the app keeps that paritydesk never
	// touches (relative path -> content).
	Extras map[string][]byte
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
	w.sessions(spec.Replica, spec.Sessions, spec.Tombstones)
	w.sessionExtras(spec)
	for _, dec := range spec.Decoys {
		w.sessions(dec.Replica, dec.Sessions, dec.Tombstones)
		w.mkdir(sessDir(dec.Replica) + "/waiting-input")
	}
	w.tasks(spec)
	ls := map[string]string{}
	w.presets(spec, ls)
	w.mirror(spec, ls)
	if spec.Worktrees != nil {
		w.json("git-worktrees.json", map[string]any{"schemaVersion": 2, "worktrees": spec.Worktrees,
			"originPins": map[string]any{}, "originUrls": map[string]any{},
			"pendingTombstones": []any{}, "untrackedDirGc": map[string]any{}}, true)
	}
	if spec.WindowState != nil {
		w.file("window-state.json", spec.WindowState, 0o644)
	}
	for rel, data := range spec.Extras {
		w.file(rel, data, 0o600)
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

func sessDir(r model.Replica) string {
	return "claude-code-sessions/" + string(r.Account) + "/" + string(r.Org)
}

// Record returns the record object AddFolder writes for s.
func Record(s Session) map[string]any {
	mode := s.PermissionMode
	if mode == "" {
		mode = "default"
	}
	cli := s.CLISessionID
	if cli == "" {
		cli = FakeUUID(50000 + s.N)
	}
	rec := map[string]any{
		"sessionId":                 FakeSession(s.N),
		"cliSessionId":              cli,
		"cwd":                       "/tmp/project",
		"originCwd":                 "/tmp/project",
		"createdAt":                 s.LastActivityAt / 2,
		"lastActivityAt":            s.LastActivityAt,
		"lastFocusedAt":             s.LastActivityAt,
		"isArchived":                s.Archived,
		"title":                     s.Title,
		"titleSource":               "user",
		"model":                     "model-a",
		"effort":                    "high",
		"permissionMode":            mode,
		"chromePermissionMode":      "skip_all_permission_checks",
		"classifierSummaryEnabled":  false,
		"alwaysAllowedReasons":      []any{},
		"sessionPermissionUpdates":  []any{},
		"remoteMcpServersConfig":    []any{},
		"spawnSeed":                 map[string]any{},
		"interruptedAwaitingPerson": nil,
		"steeredByRemoteClient":     nil,
	}
	if s.EnvScopeID != "" {
		rec["envScopeId"] = s.EnvScopeID
	}
	if s.Worktree != "" {
		name := path.Base(s.Worktree)
		rec["worktreeName"] = name
		rec["worktreePath"] = s.Worktree
		rec["branch"] = "wt/" + name
		rec["sourceBranch"] = "main"
	}
	for k, v := range s.Extra {
		rec[k] = v
	}
	return rec
}

// sessions writes one replica's records, tombstones and archive index.
func (w *writer) sessions(r model.Replica, list []Session, tombs map[int]int64) {
	dir := sessDir(r)
	w.mkdir(dir)
	archived := []string{}
	for _, s := range list {
		w.json(dir+"/"+FakeSession(s.N)+".json", Record(s), true)
		if s.Archived {
			archived = append(archived, FakeSession(s.N))
		}
	}
	for n, at := range tombs {
		rel := dir + "/deleted_" + FakeUUID(n)
		w.file(rel, []byte(fmt.Sprint(at)), 0o644)
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
}

// sessionExtras writes tmp candidates and waiting-input/ (always a
// directory) of the folder's own replica.
func (w *writer) sessionExtras(spec FolderSpec) {
	dir := sessDir(spec.Replica)
	for n, rec := range spec.TmpRecords {
		w.json(dir+"/"+FakeSession(n)+".json.tmp", rec, false)
	}
	w.mkdir(dir + "/waiting-input")
	for rel, data := range spec.WaitingInput {
		w.file(dir+"/waiting-input/"+rel, data, 0o600)
	}
}

func (w *writer) tasks(spec FolderSpec) {
	dir := sessDir(spec.Replica)
	if spec.Backlog != nil {
		w.json(dir+"/backlog/tasks.json", map[string]any{"version": 1, "items": spec.Backlog}, true)
	}
	if spec.ScheduledTasks != nil {
		w.json(dir+"/scheduled-tasks.json", map[string]any{"scheduledTasks": spec.ScheduledTasks,
			"recordedSkips": map[string]any{}, "dayFieldsOrBoundaryStamped": true, "sundayAliasBoundaryStamped": true}, true)
	}
}

// PresetsVersion is the persisted version of the presets items.
const PresetsVersion = 4

func builtIns() map[string]any {
	return map[string]any{"local": map[string]any{"settings": map[string]any{"effort": "high", "modelId": "model-a"}}}
}

// presets writes the presets item, its file mirror byAccount.<acct>, the
// per-org presets item (which paritydesk never reads) and the started
// environments map.
func (w *writer) presets(spec FolderSpec, ls map[string]string) {
	acct := string(spec.Replica.Account)
	if spec.Presets != nil {
		state := map[string]any{"presets": spec.Presets, "builtIns": builtIns(),
			"placeDefaults": map[string]any{"local::/tmp/project": map[string]any{"effort": "high"}},
			"autoStartedAt": 0, "fileMirrored": true, "seededFromDraft": true, "seededFromHistory": true,
			"startedFrom": []any{}, "startedPlaces": []any{}}
		ls["persisted.ccd-session-presets."+acct] = string(mustJSON(w.t, map[string]any{"state": state, "version": PresetsVersion}, false))
		org := map[string]any{"presets": []any{map[string]any{"id": "per-org-only", "name": "per org"}},
			"builtIns": builtIns(), "placeDefaults": map[string]any{}, "roamingUnsentAt": 0}
		ls["persisted.ccd-session-presets."+acct+"."+string(spec.Replica.Org)] = string(mustJSON(w.t, map[string]any{"state": org, "version": PresetsVersion}, false))
		file := map[string]any{"byAccount": map[string]any{acct: map[string]any{"presets": spec.Presets, "builtIns": builtIns(), "seededFromDraft": true}}}
		w.file("ccd-session-presets.json", mustJSON(w.t, file, true), 0o600)
	}
	if spec.StartedEnvironments != nil {
		st := map[string]any{"state": map[string]any{"bySessionId": spec.StartedEnvironments,
			"backfill": map[string]any{"before": 0, "version": 1}}, "version": StoreVersion}
		ls["persisted.ccd-started-environments"] = string(mustJSON(w.t, st, false))
	}
}

func envelope(t testing.TB, v any) string {
	return string(mustJSON(t, map[string]any{"value": v, "tabId": LSTabID, "timestamp": LSTimestamp}, false))
}

// StoreBackupKey is the app's backup copy of dframe-store, written next
// to it by AddFolder; paritydesk never writes it.
const StoreBackupKey = "dframe-store.backup.v1"

// mirror writes claude_desktop_config.json and the LS copies of every
// mirrored key, including dframe-store and its backup copy.
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
	ep := map[string]any{"dframe-group-scopes": map[string]any{}, "starred-local-code-sessions": []any{}}
	store := map[string]any{"homeProjectsPinnedOrder": []any{}, "collapsedGroups": []any{}, "collapsedFamilies": []any{},
		"openedGroups": []any{}, "sidebarWidth": 280, "lastKnownMode": "code",
		"customGroupsByScope": map[string]any{scope: map[string]any{"groups": []any{}, "assignments": map[string]any{}, "order": []any{}}}}
	if spec.GroupOrder != nil {
		ep["code-projects-order."+acct] = spec.GroupOrder
		ls["LSS-persisted.code-projects-order."+acct] = envelope(w.t, spec.GroupOrder)
	}
	if spec.PinnedOrder != nil {
		slice := map[string]any{"pinnedOrder": spec.PinnedOrder, "homeProjectsPinnedOrder": []string{}}
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
		doc := string(mustJSON(w.t, map[string]any{"version": StoreVersion, "shared": 1, "state": store}, false))
		ls["dframe-store"] = doc
		ls[StoreBackupKey] = doc
	}
	prefs["epitaxyPrefs"] = ep
	cfg := map[string]any{"preferences": prefs, "coworkUserFilesPath": "/Users/test/cowork"}
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
