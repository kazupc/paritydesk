package worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
)

// FileName is the worktree registry inside a data folder.
const FileName = "git-worktrees.json"

// member is the registry's member holding the entries, keyed by worktree
// name on app 6.x (schemaVersion 2; each entry carries name, path and
// branches). Other members (schemaVersion, originPins, originUrls, GC
// state such as untrackedDirGc and pendingTombstones) stay per folder and
// are never edited.
const member = "worktrees"

// Worktrees is the adapter for row model.RowWorktrees.
type Worktrees struct{}

var _ sources.Adapter = Worktrees{}

// New returns the adapter.
func New() sources.Adapter { return Worktrees{} }

// Row implements sources.Adapter.
func (Worktrees) Row() model.RowID { return model.RowWorktrees }

// Read implements sources.Adapter. Items are keyed by the member names of
// the object form ({"<name>": entry}, the form observed on app 6.x) or by
// the "path" member of the array form [{"path": ..., ...}]; the value is
// the registration entry.
func (Worktrees) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowWorktrees)
	doc, err := sources.LoadFile(rc.Folder, FileName)
	if err != nil {
		return nil, err
	}
	v, found, err := doc.Get(jsonx.Path{member})
	if err != nil || !found {
		return snap, err
	}
	entries, order, _, err := decodeEntries(v)
	if err != nil {
		return nil, err
	}
	for _, p := range order {
		it, err := model.NewItem(p, entries[p], entries[p], model.Stamp{}, false)
		if err != nil {
			return nil, fmt.Errorf("worktrees: %s: %w", FileName, err)
		}
		snap.Items[p] = it
	}
	snap.Order = order
	return snap, nil
}

// decodeEntries returns entries by path in file order, and whether the
// registry uses the array form.
func decodeEntries(v json.RawMessage) (map[string]json.RawMessage, []string, bool, error) {
	entries := map[string]json.RawMessage{}
	var order []string
	if arr, err := sources.DecodeArray(v, FileName); err == nil {
		for _, e := range arr {
			obj, err := sources.DecodeObject(e, FileName)
			if err != nil {
				return nil, nil, false, err
			}
			p, ok := sources.StringMember(obj, "path")
			if !ok || p == "" || entries[p] != nil {
				return nil, nil, false, fmt.Errorf("worktrees: %s: entry without a unique path", FileName)
			}
			entries[p] = e
			order = append(order, p)
		}
		return entries, order, true, nil
	}
	obj, err := sources.DecodeObject(v, FileName)
	if err != nil {
		return nil, nil, false, err
	}
	for p, e := range obj {
		entries[p] = e
		order = append(order, p)
	}
	sort.Strings(order)
	return entries, order, false, nil
}

// Ops implements sources.Adapter: entries are set or deleted at
// ["worktrees", <path>] (object form) or the array is rewritten (array
// form); every other member of the file stays byte-identical.
func (Worktrees) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	var res sources.OpsResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	current, desired = sources.OrEmpty(current, model.RowWorktrees), sources.OrEmpty(desired, model.RowWorktrees)
	if sources.SameItems(current, desired, false) {
		return res, nil
	}
	doc, err := sources.LoadFile(oc.Folder, FileName)
	if err != nil {
		return res, err
	}
	b := sources.NewBuilder(doc, model.RowWorktrees)
	if !doc.Present {
		return replaceFile(b, desired, res)
	}
	v, found, err := doc.Get(jsonx.Path{member})
	if err != nil {
		return res, err
	}
	if found {
		if _, _, isArray, err := decodeEntries(v); err != nil {
			return res, err
		} else if isArray {
			return setArray(b, current, desired, res)
		}
	}
	if err := b.Ensure(jsonx.Path{member}, ""); err != nil {
		return res, err
	}
	for _, k := range desired.Keys() {
		if c, ok := current.Items[k]; !ok || c.Hash != desired.Items[k].Hash {
			b.Set(jsonx.Path{member, k}, desired.Items[k].Value, k, false)
		}
	}
	for _, k := range current.Keys() {
		if _, ok := desired.Items[k]; !ok {
			if err := b.Delete(jsonx.Path{member, k}, k, true); err != nil {
				return res, err
			}
		}
	}
	res.Edits = b.Edits()
	return res, nil
}

func replaceFile(b *sources.Builder, desired *model.RowSnapshot, res sources.OpsResult) (sources.OpsResult, error) {
	if len(desired.Items) == 0 {
		return res, nil
	}
	m := map[string]json.RawMessage{}
	for k, it := range desired.Items {
		m[k] = it.Value
	}
	out, err := jsonx.Marshal(map[string]any{member: m})
	if err != nil {
		return res, fmt.Errorf("worktrees: encode: %w", err)
	}
	b.Replace(out, "", 0, false)
	res.Edits = b.Edits()
	return res, nil
}

func setArray(b *sources.Builder, current, desired *model.RowSnapshot, res sources.OpsResult) (sources.OpsResult, error) {
	keys := sources.ItemsWithPrefix(desired, "", current.Order)
	elems := make([]json.RawMessage, len(keys))
	for i, k := range keys {
		elems[i] = desired.Items[k].Value
	}
	b.Set(jsonx.Path{member}, sources.JoinArray(elems), "", false)
	res.Edits = b.Edits()
	return res, nil
}

// Referenced returns the worktree paths that the session records in s
// reference (s is a sessions-row snapshot). fields are the sessions row's
// manifest fields.worktree_path (sources.DefaultWorktreePathFields when
// empty).
func Referenced(sessions *model.RowSnapshot, fields []string) ([]string, error) {
	if sessions == nil {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, k := range sessions.Keys() {
		obj, err := sources.DecodeObject(sessions.Items[k].Value, "session "+k)
		if err != nil {
			return nil, err
		}
		if p := sources.RecordWorktree(obj, fields); p != "" {
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// LostRegistrations returns the worktree paths referenced by the leaving
// replica's sessions that the entering replica will not reference after
// apply. The orchestrator runs a read-only git status on each that still
// exists.
func LostRegistrations(leavingSessions, desiredSessions *model.RowSnapshot, fields []string) ([]string, error) {
	leaving, err := Referenced(leavingSessions, fields)
	if err != nil {
		return nil, err
	}
	keep, err := Referenced(desiredSessions, fields)
	if err != nil {
		return nil, err
	}
	kept := map[string]bool{}
	for _, p := range keep {
		kept[p] = true
	}
	var out []string
	for _, p := range leaving {
		if !kept[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

// LostEntries returns the directories that the entering replica's
// registry (current, a worktrees-row snapshot) registers and that no
// entry of desired will register after apply, compared by directory, not
// by name: entries the merge deletes, and entries replaced by one with
// another directory. pathFields name the entry's directory member ("path"
// when empty); an entry without one is keyed by its directory (the array
// form).
func LostEntries(current, desired *model.RowSnapshot, pathFields []string) ([]string, error) {
	if current == nil {
		return nil, nil
	}
	if len(pathFields) == 0 {
		pathFields = []string{"path"}
	}
	dirOf := func(it model.Item) (string, error) {
		obj, err := sources.DecodeObject(it.Value, "worktree "+it.Key)
		if err != nil {
			return "", err
		}
		for _, f := range pathFields {
			if p, ok := sources.StringMember(obj, f); ok && p != "" {
				return p, nil
			}
		}
		return it.Key, nil
	}
	kept := map[string]bool{}
	if desired != nil {
		for _, k := range desired.Keys() {
			p, err := dirOf(desired.Items[k])
			if err != nil {
				return nil, err
			}
			kept[p] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, k := range current.Keys() {
		p, err := dirOf(current.Items[k])
		if err != nil {
			return nil, err
		}
		if !kept[p] && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Guard exposes the package functions behind methods for the
// orchestrator.
type Guard struct{}

// LostRegistrations calls the package function.
func (Guard) LostRegistrations(leavingSessions, desiredSessions *model.RowSnapshot, fields []string) ([]string, error) {
	return LostRegistrations(leavingSessions, desiredSessions, fields)
}

// LostEntries calls the package function.
func (Guard) LostEntries(current, desired *model.RowSnapshot, pathFields []string) ([]string, error) {
	return LostEntries(current, desired, pathFields)
}
