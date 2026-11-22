package rows

import (
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// PostMerge runs once after every row's Merge, in model.AllRows order, and
// fixes cross-row effects that a single module cannot see: it removes
// environments items "started/<session id>" (from Desired, Canonical and
// both overlays) whose session is absent or tombstoned in the sessions
// row's Desired, and reports each removal as a change. Rows missing from
// outs are left alone.
//
// The started-environments map is one Local Storage item for the whole
// data folder, while the sessions row reads only the replica's own
// account and org directory. So an entry is removed only when its id is
// a well-formed local session id (ids.ParseSession) that the sessions
// row knew (in its inputs or tombstones). Entries of other orgs, other
// accounts and remote session_ ids are left as the environments merge
// decided.
func PostMerge(outs map[model.RowID]*Output) error {
	env, ses := outs[model.RowEnvironments], outs[model.RowSessions]
	if env == nil || ses == nil || ses.Desired == nil {
		return nil
	}
	known := knownKeys(ses.Desired, ses.Canonical, ses.LeavingOverlay, ses.EnteringOverlay)
	for k := range ses.Known {
		known[k] = true
	}
	alive := func(id string) bool {
		if _, err := ids.ParseSession(id); err != nil || !known[id] {
			return true
		}
		_, ok := ses.Desired.Items[id]
		return ok
	}
	removed := map[string]bool{}
	for _, s := range []*model.RowSnapshot{env.Desired, env.Canonical, env.LeavingOverlay, env.EnteringOverlay} {
		if s == nil {
			continue
		}
		for k := range s.Items {
			id, ok := strings.CutPrefix(k, startedPrefix)
			if !ok || alive(id) {
				continue
			}
			delete(s.Items, k)
			if s == env.Desired {
				removed[k] = true
			}
		}
	}
	env.Changes = startedChanges(env.Changes, removed)
	return nil
}

// startedChanges replaces the changes of removed started/ keys: an add
// becomes nothing (the target never had it), anything else a deletion.
func startedChanges(changes []model.Change, removed map[string]bool) []model.Change {
	if len(removed) == 0 {
		return changes
	}
	out := changes[:0:0]
	reported := map[string]bool{}
	for _, c := range changes {
		if !removed[c.Key] {
			out = append(out, c)
			continue
		}
		reported[c.Key] = true
		if c.Kind == model.ChangeAdded || c.Kind == model.ChangeRestored {
			continue
		}
		out = append(out, model.Change{Row: model.RowEnvironments, Kind: model.ChangeDeleted, Key: c.Key, Note: noteStartedGone})
	}
	for _, k := range sortedSet(removed) {
		if !reported[k] {
			out = append(out, model.Change{Row: model.RowEnvironments, Kind: model.ChangeDeleted, Key: k, Note: noteStartedGone})
		}
	}
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
