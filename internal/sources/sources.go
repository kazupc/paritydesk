package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
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

// OpsContext adds what producing edits needs.
type OpsContext struct {
	ReadContext
	// NowMillis stamps envelope timestamps paritydesk writes; every such
	// stamp is reported back through WrittenStamps so it never wins a
	// tie-break (D7).
	NowMillis int64
	// Source is the leaving replica's read context, used to copy raw bytes
	// (for example secret values) directly between folders (D27). When the
	// source no longer holds bytes matching an item's hash, the adapter
	// keeps the target's current value for that item and reports a
	// model.ChangeSkipped in OpsResult.Skipped; this never fails the run
	// (G8).
	Source *ReadContext
}

// Adapter reads and writes one row.
//
// Mirrored keys (rows group-order, pinned-order and sidebar-view keep a
// copy under preferences.epitaxyPrefs in claude_desktop_config.json): the
// mirror is authoritative (S1, S4). Read takes each such value from the
// mirror; a differing Local Storage copy is listed in RowSnapshot.Stale and
// never harvested. Stamp.AppTime comes from the LS envelope timestamp only
// when the LS and mirror values agree, else it is 0 (unknown) so the epoch
// decides.
type Adapter interface {
	Row() model.RowID
	// Read returns the row's snapshot. A row whose files and keys are all
	// absent returns an empty snapshot, not an error. Malformed data is an
	// error naming the file (never its content). Items are built with
	// model.NewItem, so Hash covers the normalized Value only.
	Read(ctx context.Context, rc ReadContext) (*model.RowSnapshot, error)
	// Ops returns the literal-path edits that turn current into desired on
	// the folder of oc. current must be what Read returned for that folder.
	// Edits name their location and carry the hash of the bytes they were
	// computed against; plan.Compose folds the edits of every row into one
	// op per location. Ops(current, current) returns no edits.
	Ops(ctx context.Context, oc OpsContext, current, desired *model.RowSnapshot) (OpsResult, error)
}

// OpsResult is what Ops returns.
type OpsResult struct {
	Edits []plan.Edit
	// WrittenStamps lists the app-time values paritydesk wrote, by item
	// key, so state can record them as tool-written.
	WrittenStamps map[string]int64
	// Skipped reports items left at the target's value (constant notes
	// only), for example a secret whose source bytes changed.
	Skipped []model.Change
}

// MirrorReader is implemented by the adapters of mirrored rows
// (group-order, pinned-order, sidebar-view). MirrorItems returns the items
// Read would produce from the config mirror alone, with the same hashes,
// so Verify (P12) and the contested rules compare like with like. spec is
// the row's manifest spec expanded for the replica; mirror is the bytes of
// claude_desktop_config.json.
type MirrorReader interface {
	MirrorItems(spec compat.RowSpec, mirror []byte) (*model.RowSnapshot, error)
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
	// Busy is true when the record's run-state fields (manifest
	// fields.run_state of the sessions row) say a turn is running or a
	// message is queued. The orchestrator combines it with
	// AppControl.BusySessions (main.log) for the P0 and P3 busy checks.
	Busy bool
}

// Record field names read outside the row merges. Adapters are the only
// layer that knows on-disk names; row modules take theirs from the
// manifest's field lists.
const (
	FieldTitle        = "title"
	FieldCLISessionID = "cliSessionId"
	FieldArchived     = "isArchived"
)

// WorktreeFields are the record fields that may hold a session's worktree
// path, either as a string or as an object with a "path" member.
var WorktreeFields = []string{"worktreePath", "worktree"}

// Field-list roles of compat.RowSpec.Fields.
const (
	RolePerField      = "per_field"
	RoleAccountBound  = "account_bound"
	RoleRunState      = "run_state"
	RoleMaxTime       = "max_time"
	RoleParent        = "parent"
	RoleSecret        = "secret"         // dotted paths of secret members (environments)
	RoleTombstoneTime = "tombstone_time" // members of a tombstone file holding its time
	RoleAllow         = "allow"          // app-prefs allow-list set by the orchestrator
)

// Session file name parts.
const (
	RecordPrefix    = "local_"
	RecordSuffix    = ".json"
	TmpSuffix       = ".tmp"
	TombstonePrefix = "deleted_"
	ArchiveIndex    = "archived-sessions.idx"
)

// ListSessions reads every session record of a replica without writing.
// spec is the sessions row's manifest spec (field lists). Used to resolve
// the invoking session for the relaunch link and to list busy sessions.
func ListSessions(ctx context.Context, folder string, r model.Replica, spec compat.RowSpec) ([]SessionInfo, error) {
	recs, err := ReadRecordFiles(ctx, folder, r)
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, 0, len(recs))
	for _, rec := range recs {
		info, err := sessionInfo(rec, spec.Fields[RoleRunState])
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// ResolveByCLISession finds the local session whose record carries the
// given Claude Code session id.
func ResolveByCLISession(ctx context.Context, folder string, r model.Replica, cliSessionID string) (ids.SessionID, error) {
	if cliSessionID == "" {
		return "", errors.New("sources: empty CLI session id")
	}
	recs, err := ReadRecordFiles(ctx, folder, r)
	if err != nil {
		return "", err
	}
	for _, rec := range recs {
		info, err := sessionInfo(rec, nil)
		if err != nil {
			return "", err
		}
		if info.CLISessionID == cliSessionID {
			return info.ID, nil
		}
	}
	return "", errors.New("sources: no session record carries the given CLI session id")
}

// DiscoverReplicas lists the (account, org) directories under
// <folder>/claude-code-sessions. Only well-formed UUID directory names are
// returned.
func DiscoverReplicas(folder string) ([]model.Replica, error) {
	root := filepath.Join(folder, SessionsRoot)
	accts, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sources: list %s: %w", SessionsRoot, err)
	}
	var out []model.Replica
	for _, a := range accts {
		acct, err := ids.ParseAccount(a.Name())
		if err != nil || !a.IsDir() {
			continue
		}
		orgs, err := os.ReadDir(filepath.Join(root, a.Name()))
		if err != nil {
			return nil, fmt.Errorf("sources: list %s/<account>: %w", SessionsRoot, err)
		}
		for _, o := range orgs {
			org, err := ids.ParseOrg(o.Name())
			if err != nil || !o.IsDir() {
				continue
			}
			out = append(out, model.Replica{Account: acct, Org: org})
		}
	}
	return out, nil
}

// Directory exposes the session helpers behind methods for the
// orchestrator.
type Directory struct{}

// ListSessions calls the package function.
func (Directory) ListSessions(ctx context.Context, folder string, r model.Replica, spec compat.RowSpec) ([]SessionInfo, error) {
	return ListSessions(ctx, folder, r, spec)
}

// ResolveByCLISession calls the package function.
func (Directory) ResolveByCLISession(ctx context.Context, folder string, r model.Replica, cliSessionID string) (ids.SessionID, error) {
	return ResolveByCLISession(ctx, folder, r, cliSessionID)
}

// DiscoverReplicas calls the package function.
func (Directory) DiscoverReplicas(folder string) ([]model.Replica, error) {
	return DiscoverReplicas(folder)
}

// RecordFile is one session record file as read from disk.
type RecordFile struct {
	ID   ids.SessionID
	Rel  string // path relative to the data folder
	Data []byte
}

// ReadRecordFiles reads every local_<uuid>.json of a replica in id order.
// Names that are not well-formed session ids are ignored. A missing
// directory yields no records.
func ReadRecordFiles(ctx context.Context, folder string, r model.Replica) ([]RecordFile, error) {
	dir := SessionsDir(folder, r)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sources: list session directory: %w", err)
	}
	var out []RecordFile
	for _, e := range ents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id, ok := RecordID(e.Name())
		if !ok {
			continue
		}
		rel := SessionRel(r, e.Name())
		data, present, err := ReadFile(folder, rel)
		if err != nil {
			return nil, err
		}
		if present {
			out = append(out, RecordFile{ID: id, Rel: rel, Data: data})
		}
	}
	return out, nil
}

// RecordID returns the session id of a record file name local_<uuid>.json.
func RecordID(name string) (ids.SessionID, bool) {
	if !strings.HasPrefix(name, RecordPrefix) || !strings.HasSuffix(name, RecordSuffix) {
		return "", false
	}
	id, err := ids.ParseSession(strings.TrimSuffix(name, RecordSuffix))
	if err != nil {
		return "", false
	}
	return id, true
}

// SessionRel returns the folder-relative slash path of a file in the
// replica's session directory.
func SessionRel(r model.Replica, name string) string {
	return SessionsRoot + "/" + string(r.Account) + "/" + string(r.Org) + "/" + name
}

func sessionInfo(rec RecordFile, runState []string) (SessionInfo, error) {
	obj, err := DecodeObject(rec.Data, rec.Rel)
	if err != nil {
		return SessionInfo{}, err
	}
	info := SessionInfo{ID: rec.ID}
	info.Title, _ = StringMember(obj, FieldTitle)
	info.CLISessionID, _ = StringMember(obj, FieldCLISessionID)
	info.Archived = BoolMember(obj, FieldArchived)
	info.Worktree = RecordWorktree(obj)
	for _, f := range runState {
		if raw, ok := obj[f]; ok && busyValue(raw) {
			info.Busy = true
		}
	}
	return info, nil
}

// busyStates are run-state string values that mean a turn is running or a
// message is queued.
var busyStates = map[string]bool{"running": true, "working": true, "busy": true, "queued": true, "streaming": true, "pending": true}

// busyValue reports whether a run-state field's value says busy: true, a
// non-empty array or object (for example queued messages), or a busy
// status string. Numbers are times (for example an interrupted mark) and
// never mean busy.
func busyValue(raw json.RawMessage) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return busyStates[strings.ToLower(x)]
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return false
}

// RecordWorktree returns the worktree path a decoded record names, or "".
func RecordWorktree(obj map[string]json.RawMessage) string {
	for _, f := range WorktreeFields {
		raw, ok := obj[f]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
		var o struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(raw, &o) == nil && o.Path != "" {
			return o.Path
		}
	}
	return ""
}

// MaxFileSize bounds every file an adapter reads.
const MaxFileSize = 64 << 20

// ReadFile reads root/rel (rel is slash-separated). An absent file returns
// present=false and no error. Errors name rel, never content.
func ReadFile(root, rel string) (data []byte, present bool, err error) {
	path := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("sources: stat %s: %w", rel, unwrapPath(err))
	}
	if !fi.Mode().IsRegular() {
		return nil, false, fmt.Errorf("sources: %s is not a regular file", rel)
	}
	if fi.Size() > MaxFileSize {
		return nil, false, fmt.Errorf("sources: %s is too large", rel)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("sources: read %s: %w", rel, unwrapPath(err))
	}
	return data, true, nil
}

// FileMTimeMillis returns the file's modification time in Unix ms, or 0.
func FileMTimeMillis(root, rel string) int64 {
	fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return 0
	}
	return fi.ModTime().UnixMilli()
}

// unwrapPath drops the path from *fs.PathError so errors stay relative.
func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// BaseHash returns the sha256 of a location's bytes, or "" when absent.
func BaseHash(data []byte, present bool) string {
	if !present {
		return ""
	}
	return model.HashBytes(data)
}

// SecretHash returns the "sha256:<hex>" placeholder of a secret value. The
// hash covers the canonical form, so formatting does not matter.
func SecretHash(v json.RawMessage) (string, error) {
	h, err := model.ItemHash(v)
	if err != nil {
		return "", errors.New("sources: secret value is not valid JSON")
	}
	return "sha256:" + h, nil
}

// DecodeObject decodes a JSON object into its members, refusing
// malformed JSON and duplicate keys. name labels errors; content never
// appears in them.
func DecodeObject(data []byte, name string) (map[string]json.RawMessage, error) {
	if err := validJSON(data, name); err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil || obj == nil {
		return nil, fmt.Errorf("sources: %s: not a JSON object", name)
	}
	return obj, nil
}

// DecodeArray decodes a JSON array into its raw elements.
func DecodeArray(data []byte, name string) ([]json.RawMessage, error) {
	if err := validJSON(data, name); err != nil {
		return nil, err
	}
	var arr []json.RawMessage
	if json.Unmarshal(data, &arr) != nil || arr == nil {
		return nil, fmt.Errorf("sources: %s: not a JSON array", name)
	}
	return arr, nil
}

// StringMember returns obj[key] as a string.
func StringMember(obj map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := obj[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// BoolMember returns obj[key] as a bool (false if absent or not a bool).
func BoolMember(obj map[string]json.RawMessage, key string) bool {
	var b bool
	if raw, ok := obj[key]; ok && json.Unmarshal(raw, &b) == nil {
		return b
	}
	return false
}

// IntMember returns obj[key] as an integer (Unix ms values and counters).
func IntMember(obj map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := obj[key]
	if !ok {
		return 0, false
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil {
		return 0, false
	}
	return int64(f), true
}

// MaxTime returns the largest numeric member among fields, or 0.
func MaxTime(obj map[string]json.RawMessage, fields []string) int64 {
	var m int64
	for _, f := range fields {
		if v, ok := IntMember(obj, f); ok && v > m {
			m = v
		}
	}
	return m
}

// IDString returns an "id" member as a key string: strings as is, other
// scalars in their JSON form.
func IDString(obj map[string]json.RawMessage) (string, bool) {
	raw, ok := obj["id"]
	if !ok {
		return "", false
	}
	if s, ok := StringMember(obj, "id"); ok {
		return s, s != ""
	}
	c, err := model.Canonical(raw)
	if err != nil || len(c) == 0 || c[0] == '{' || c[0] == '[' || string(c) == "null" {
		return "", false
	}
	return string(c), true
}

func validJSON(data []byte, name string) error {
	if err := jsonx.Validate(data); err != nil {
		return fmt.Errorf("sources: %s: malformed JSON: %w", name, err)
	}
	return nil
}

// NewSnapshot returns an empty schema-1 snapshot of row.
func NewSnapshot(row model.RowID) *model.RowSnapshot {
	return &model.RowSnapshot{Row: row, Schema: 1, Items: map[string]model.Item{}}
}

// SameItems reports whether a and b hold the same item keys with the same
// hashes (and, when order is true, the same Order).
func SameItems(a, b *model.RowSnapshot, order bool) bool {
	ai, bi := items(a), items(b)
	if len(ai) != len(bi) {
		return false
	}
	for k, x := range ai {
		y, ok := bi[k]
		if !ok || x.Hash != y.Hash {
			return false
		}
	}
	if order {
		return equalStrings(orderOf(a), orderOf(b))
	}
	return true
}

func items(s *model.RowSnapshot) map[string]model.Item {
	if s == nil {
		return nil
	}
	return s.Items
}

func orderOf(s *model.RowSnapshot) []string {
	if s == nil {
		return nil
	}
	return s.Order
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ItemsWithPrefix returns the item keys of s that start with prefix,
// ordered by s.Order first (entries present in s), then the remaining keys
// in fallback order, then the rest sorted.
func ItemsWithPrefix(s *model.RowSnapshot, prefix string, fallback []string) []string {
	if s == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		if seen[k] || !strings.HasPrefix(k, prefix) {
			return
		}
		if _, ok := s.Items[k]; !ok {
			return
		}
		seen[k] = true
		out = append(out, k)
	}
	for _, k := range s.Order {
		add(k)
	}
	for _, k := range fallback {
		add(k)
	}
	rest := s.Keys()
	sort.Strings(rest)
	for _, k := range rest {
		add(k)
	}
	return out
}

// SourceItem returns the leaving replica's item for key when its hash
// equals want, read through adapter a from oc.Source. It returns nil when
// there is no source, no such item, or the hash differs (the source moved
// on since the merge).
func SourceItem(ctx context.Context, a Adapter, src *ReadContext, cache **model.RowSnapshot, key, want string) (*model.Item, error) {
	if src == nil {
		return nil, nil
	}
	if *cache == nil {
		s, err := a.Read(ctx, *src)
		if err != nil {
			return nil, fmt.Errorf("sources: read source: %w", err)
		}
		*cache = s
	}
	it, ok := (*cache).Items[key]
	if !ok || it.Hash != want || it.Raw == nil {
		return nil, nil
	}
	return &it, nil
}

// Skip returns the ChangeSkipped reported when an item keeps the target's
// value. note must be a constant.
func Skip(row model.RowID, key, note string) model.Change {
	return model.Change{Row: row, Kind: model.ChangeSkipped, Key: key, Note: note}
}

// Constant notes for skipped items.
const (
	NoteSourceChanged = "source value changed since the merge; target value kept"
	NoteMirrorMissing = "config mirror container missing; row left unchanged"
	NoteFileMissing   = "target file missing; row left unchanged"
)

// Doc is one location's current content: a file, or a Local Storage item
// (stored bytes plus decoded text).
type Doc struct {
	Loc     plan.Location
	Present bool
	// Stored is the file's bytes or the encoded LS value; BaseHash covers
	// it.
	Stored []byte
	// Text is the file's bytes or the decoded LS text; edits address it.
	Text []byte
}

// BaseHash returns the hash edits on this location carry.
func (d Doc) BaseHash() string { return BaseHash(d.Stored, d.Present) }

// Name labels the location in errors.
func (d Doc) Name() string {
	if d.Loc.LSKey != "" {
		return "Local Storage item " + d.Loc.LSKey
	}
	return d.Loc.File
}

// LoadFile reads folder/rel as a Doc.
func LoadFile(folder, rel string) (Doc, error) {
	data, present, err := ReadFile(folder, rel)
	if err != nil {
		return Doc{}, err
	}
	return Doc{Loc: plan.Location{File: rel}, Present: present, Stored: data, Text: data}, nil
}

// LoadLS reads a Local Storage item by name. A nil reader or a missing key
// gives an absent Doc. Values that do not round-trip are refused.
func LoadLS(r kv.Reader, name string) (Doc, error) {
	d := Doc{Loc: plan.Location{LSKey: name}}
	if r == nil {
		return d, nil
	}
	key, err := localstorage.Key(name)
	if err != nil {
		return Doc{}, fmt.Errorf("sources: Local Storage item %s: %w", name, err)
	}
	raw, found, err := r.Get(key)
	if err != nil {
		return Doc{}, fmt.Errorf("sources: read Local Storage item %s: %w", name, err)
	}
	if !found {
		return d, nil
	}
	text, err := localstorage.DecodeText(raw)
	if err != nil {
		return Doc{}, fmt.Errorf("sources: Local Storage item %s: %w", name, err)
	}
	d.Present, d.Stored, d.Text = true, append([]byte(nil), raw...), []byte(text)
	return d, nil
}

// Get returns the raw value at path in the Doc's text, or found=false when
// the Doc or the path is absent. A non-object parent is an error.
func (d Doc) Get(path jsonx.Path) (json.RawMessage, bool, error) {
	if !d.Present {
		return nil, false, nil
	}
	if len(path) == 0 {
		if err := jsonx.Validate(d.Text); err != nil {
			return nil, false, fmt.Errorf("sources: %s: malformed JSON: %w", d.Name(), err)
		}
		return json.RawMessage(d.Text), true, nil
	}
	v, err := jsonx.Get(d.Text, path)
	switch {
	case errors.Is(err, jsonx.ErrNotFound):
		return nil, false, nil
	case errors.Is(err, jsonx.ErrNotObject):
		return nil, false, fmt.Errorf("sources: %s: unexpected shape", d.Name())
	case err != nil:
		return nil, false, fmt.Errorf("sources: %s: malformed JSON: %w", d.Name(), err)
	}
	return v, true, nil
}

// Builder accumulates the edits of one row on one location. It tracks the
// paths it creates and deletes so parents are created once, with an
// EditSet of {} before the first edit below them.
type Builder struct {
	Doc   Doc
	Row   model.RowID
	edits []plan.Edit
	made  map[string]bool
	gone  map[string]bool
}

// NewBuilder returns a Builder for doc.
func NewBuilder(doc Doc, row model.RowID) *Builder {
	return &Builder{Doc: doc, Row: row, made: map[string]bool{}, gone: map[string]bool{}}
}

func pathKey(p jsonx.Path) string { return strings.Join(p, "\x00") }

// Has reports whether path exists at this point of the builder's edits.
func (b *Builder) Has(path jsonx.Path) (bool, error) {
	k := pathKey(path)
	if b.made[k] {
		return true, nil
	}
	for i := 1; i <= len(path); i++ {
		if b.gone[pathKey(path[:i])] {
			return false, nil
		}
	}
	_, found, err := b.Doc.Get(path)
	return found, err
}

// Ensure creates every missing object along path (path itself included)
// with EditSet of {}. Existing members must be objects.
func (b *Builder) Ensure(path jsonx.Path, item string) error {
	if !b.Doc.Present {
		return fmt.Errorf("sources: %s: absent", b.Doc.Name())
	}
	for i := 1; i <= len(path); i++ {
		p := path[:i]
		ok, err := b.Has(p)
		if err != nil {
			return err
		}
		if ok {
			if !b.made[pathKey(p)] {
				v, _, err := b.Doc.Get(p)
				if err != nil {
					return err
				}
				if t := bytesTrimLeft(v); len(t) == 0 || t[0] != '{' {
					return fmt.Errorf("sources: %s: unexpected shape", b.Doc.Name())
				}
			}
			continue
		}
		b.Set(append(jsonx.Path(nil), p...), json.RawMessage("{}"), item, false)
		b.made[pathKey(p)] = true
		delete(b.gone, pathKey(p))
	}
	return nil
}

func bytesTrimLeft(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
		b = b[1:]
	}
	return b
}

// Set adds an EditSet.
func (b *Builder) Set(path jsonx.Path, v json.RawMessage, item string, secret bool) {
	b.edits = append(b.edits, plan.Edit{Loc: b.Doc.Loc, Row: b.Row, Item: item, Kind: plan.EditSet,
		Path: append(jsonx.Path(nil), path...), Value: v, BaseHash: b.Doc.BaseHash(), Secret: secret})
	b.made[pathKey(path)] = true
	delete(b.gone, pathKey(path))
}

// Delete adds an EditDelete when path exists. destructive marks the
// removal of a whole item.
func (b *Builder) Delete(path jsonx.Path, item string, destructive bool) error {
	ok, err := b.Has(path)
	if err != nil || !ok {
		return err
	}
	b.edits = append(b.edits, plan.Edit{Loc: b.Doc.Loc, Row: b.Row, Item: item, Kind: plan.EditDelete,
		Path: append(jsonx.Path(nil), path...), BaseHash: b.Doc.BaseHash(), Destructive: destructive})
	delete(b.made, pathKey(path))
	b.gone[pathKey(path)] = true
	return nil
}

// Replace adds an EditReplace of the whole document.
func (b *Builder) Replace(v []byte, item string, mode fs.FileMode, secret bool) {
	b.edits = append(b.edits, plan.Edit{Loc: b.Doc.Loc, Row: b.Row, Item: item, Kind: plan.EditReplace,
		Value: v, Mode: mode, BaseHash: b.Doc.BaseHash(), Secret: secret})
}

// Remove adds an EditRemove (quarantine or LS delete) of a present
// location. reason is "" for user data, else one of
// plan.NonUserDataReasons.
func (b *Builder) Remove(item, reason string) {
	if !b.Doc.Present {
		return
	}
	b.edits = append(b.edits, plan.Edit{Loc: b.Doc.Loc, Row: b.Row, Item: item, Kind: plan.EditRemove,
		BaseHash: b.Doc.BaseHash(), Destructive: true, Reason: reason})
}

// Edits returns the accumulated edits.
func (b *Builder) Edits() []plan.Edit { return b.edits }

// JoinArray builds a JSON array from raw elements.
func JoinArray(elems []json.RawMessage) json.RawMessage {
	out := []byte{'['}
	for i, e := range elems {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, e...)
	}
	return append(out, ']')
}

// IsEnvelopeKey reports whether an LS item name uses the LSS envelope
// {"value","tabId","timestamp"}; other persisted items use the
// {"state","version"} wrapper.
func IsEnvelopeKey(name string) bool { return strings.HasPrefix(name, "LSS-persisted.") }

// OrEmpty returns s, or an empty snapshot of row when s is nil, so Ops can
// treat a missing snapshot as "no items".
func OrEmpty(s *model.RowSnapshot, row model.RowID) *model.RowSnapshot {
	if s == nil {
		return NewSnapshot(row)
	}
	if s.Items == nil {
		c := *s
		c.Items = map[string]model.Item{}
		return &c
	}
	return s
}

// HashSecrets returns obj with every value of each named member (a
// dotted path to an object of secret values) replaced by "sha256:<hex>",
// and whether any value was replaced. Objects are re-encoded with sorted
// keys. name labels errors.
func HashSecrets(raw json.RawMessage, paths []string, name string) (json.RawMessage, bool, error) {
	obj, err := DecodeObject(raw, name)
	if err != nil {
		return nil, false, err
	}
	changed := false
	for _, p := range paths {
		c, err := hashAt(obj, strings.Split(p, "."), name)
		if err != nil {
			return nil, false, err
		}
		changed = changed || c
	}
	if !changed {
		return raw, false, nil
	}
	out, err := jsonx.Marshal(obj)
	if err != nil {
		return nil, false, fmt.Errorf("sources: %s: encode: %w", name, err)
	}
	return out, true, nil
}

// hashAt replaces, in place, the values of the object at path.
func hashAt(obj map[string]json.RawMessage, path []string, name string) (bool, error) {
	raw, ok := obj[path[0]]
	if !ok {
		return false, nil
	}
	inner, err := DecodeObject(raw, name)
	if err != nil {
		return false, nil // not an object: nothing to hash at this path
	}
	var changed bool
	if len(path) > 1 {
		changed, err = hashAt(inner, path[1:], name)
		if err != nil {
			return false, err
		}
	} else {
		for k, v := range inner {
			h, err := SecretHash(v)
			if err != nil {
				return false, fmt.Errorf("sources: %s: secret value is malformed", name)
			}
			b, _ := jsonx.Marshal(h)
			inner[k] = b
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	b, err := jsonx.Marshal(inner)
	if err != nil {
		return false, err
	}
	obj[path[0]] = b
	return true, nil
}

// FileItem builds a whole-file item: Value {"sha256": <hex>} plus "kind"
// when non-empty, AppTime the file's mtime, Raw the bytes.
func FileItem(key string, data []byte, kind string, mtime int64) (model.Item, error) {
	v := struct {
		Kind   string `json:"kind,omitempty"`
		SHA256 string `json:"sha256"`
	}{kind, model.HashBytes(data)}
	b, err := jsonx.Marshal(v)
	if err != nil {
		return model.Item{}, err
	}
	return model.NewItem(key, json.RawMessage(b), data, model.Stamp{AppTime: mtime}, false)
}
