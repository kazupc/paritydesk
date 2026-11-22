package rows

import (
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
)

// keyedModule merges a row of independent keyed items: Scalar per key,
// with a field merge over Fields[per_field] and Fields[max_time] when the
// manifest lists them (tasks, worktrees), or none at all (app-prefs per
// key, window-state as one whole-file item decided by Prefer).
type keyedModule struct {
	row          model.RowID
	noFieldMerge bool
}

func (k keyedModule) Row() model.RowID { return k.row }

// Merge implements Module.
func (k keyedModule) Merge(in Input) (Output, error) {
	if err := checkRow(in, k.row); err != nil {
		return Output{}, err
	}
	spec := keyedSpec{row: k.row}
	if !k.noFieldMerge {
		spec.fieldMerge = recordMerger(in.Policy.Fields[RolePerField], in.Policy.Fields[RoleMaxTime])
	}
	return runKeyed(in, spec)
}

// waitingInputModule merges waiting-input files as a keyed set with
// deletions. Files of an unknown kind reach the target only when absent
// there; the target's own copy is never replaced or removed.
type waitingInputModule struct{}

func (waitingInputModule) Row() model.RowID { return model.RowWaitingInput }

// kindMember is the member of a waiting-input value naming its kind (plan
// section 5 schema).
const kindMember = "kind"

// Merge implements Module. Known kinds come from Fields[known_kinds]; when
// the manifest gives no list every kind is known.
func (waitingInputModule) Merge(in Input) (Output, error) {
	if err := checkRow(in, model.RowWaitingInput); err != nil {
		return Output{}, err
	}
	known, listed := in.Policy.Fields[RoleKnownKinds]
	isKnown := func(it *model.Item) bool {
		if !listed || it == nil {
			return true
		}
		kind, _ := getString(it.Value, kindMember)
		for _, k := range known {
			if k == kind {
				return true
			}
		}
		return false
	}
	spec := keyedSpec{
		row: model.RowWaitingInput,
		keepTarget: func(_ string, cand, cur *model.Item) bool {
			return cur != nil && (!isKnown(cur) || !isKnown(cand))
		},
	}
	return runKeyed(in, spec)
}

// mcpModule merges local MCP servers keyed by name. Their env values are
// secret: hashed in Value, copied as raw bytes, and never carried across a
// managed edge unless every changed variable is allowed as
// "<server>/<VAR>".
type mcpModule struct{}

func (mcpModule) Row() model.RowID { return model.RowMCPServers }

// Merge implements Module.
func (mcpModule) Merge(in Input) (Output, error) {
	if err := checkRow(in, model.RowMCPServers); err != nil {
		return Output{}, err
	}
	f := in.Policy.Fields
	spec := keyedSpec{
		row:           model.RowMCPServers,
		fieldMerge:    recordMerger(f[RolePerField], f[RoleMaxTime]),
		secretAllowed: secretAllow(in.Policy.SecretsAllow, f[RoleSecret], "", ""),
	}
	return runKeyed(in, spec)
}

// tasksModule merges backlog and scheduled tasks as keyed items, and
// applies sync.exclude: a task whose project path (Fields[project_path])
// matches an exclude glob, or whose originating session
// (Fields[session_ref]) is an excluded session id, never leaves its
// profile. A task without a project path is matched by its originating
// session only.
type tasksModule struct{}

func (tasksModule) Row() model.RowID { return model.RowTasks }

// Merge implements Module.
func (tasksModule) Merge(in Input) (Output, error) {
	if err := checkRow(in, model.RowTasks); err != nil {
		return Output{}, err
	}
	f := in.Policy.Fields
	excl := taskExcluder(in.Policy.Exclude, f[RoleProjectPath], f[RoleSessionRef])
	spec := keyedSpec{
		row:        model.RowTasks,
		fieldMerge: recordMerger(f[RolePerField], f[RoleMaxTime]),
		excluded:   excl,
		keepTarget: func(k string, cand, cur *model.Item) bool {
			return excl(k, cand) || excl(k, cur)
		},
	}
	return runKeyed(in, spec)
}

// taskExcluder reports tasks that sync.exclude keeps private.
func taskExcluder(exclude, projectFields, sessionFields []string) func(string, *model.Item) bool {
	if len(exclude) == 0 {
		return func(string, *model.Item) bool { return false }
	}
	byID := map[string]bool{}
	var globs []string
	for _, pat := range exclude {
		if _, err := ids.ParseSession(pat); err == nil {
			byID[pat] = true
		} else {
			globs = append(globs, pat)
		}
	}
	return func(_ string, it *model.Item) bool {
		if it == nil {
			return false
		}
		for _, fld := range sessionFields {
			if sid, ok := getString(it.Value, fld); ok && byID[sid] {
				return true
			}
		}
		for _, fld := range projectFields {
			proj, ok := getString(it.Value, fld)
			if !ok || proj == "" {
				continue
			}
			for _, g := range globs {
				if globMatch(g, proj) {
					return true
				}
			}
		}
		return false
	}
}

// worktreesModule merges worktree registry entries, keyed by worktree
// name. An entry the target already has under the same name with another
// directory (Fields[worktree_path]) is never replaced: the target keeps
// its own registration, so the app's worktree cleanup never sees that
// directory as unregistered. Trust grants (Fields[grant]) and leases
// (Fields[run_state]) are kept from the target and dropped on entries new
// to it, so they never cross accounts.
type worktreesModule struct{}

func (worktreesModule) Row() model.RowID { return model.RowWorktrees }

// Merge implements Module.
func (worktreesModule) Merge(in Input) (Output, error) {
	if err := checkRow(in, model.RowWorktrees); err != nil {
		return Output{}, err
	}
	f := in.Policy.Fields
	pathField := first(f[RoleWorktreePath])
	spec := keyedSpec{
		row:        model.RowWorktrees,
		fieldMerge: recordMerger(f[RolePerField], f[RoleMaxTime]),
		keepTarget: func(_ string, cand, cur *model.Item) bool {
			return otherDirectory(pathField, cand, cur)
		},
		transform: grantTransform(model.RowWorktrees, f),
	}
	out, err := runKeyed(in, spec)
	if err != nil {
		return out, err
	}
	if in.Entering.Snapshot != nil && out.Canonical != nil {
		for _, k := range out.Canonical.Keys() {
			cand := out.Canonical.Items[k]
			cur, ok := in.Entering.Snapshot.Items[k]
			if ok && otherDirectory(pathField, &cand, &cur) {
				out.Changes = append(out.Changes, model.Change{Row: model.RowWorktrees, Kind: model.ChangeSkipped,
					Key: k, Note: noteOtherDirectory})
			}
		}
	}
	return out, nil
}

// noteOtherDirectory explains a worktree entry left as the target has it.
const noteOtherDirectory = "this profile registers another directory under the same worktree name; its own entry is kept"

// otherDirectory reports whether cand and cur are both present and name
// different directories.
func otherDirectory(field string, cand, cur *model.Item) bool {
	if field == "" || cand == nil || cur == nil {
		return false
	}
	a, _ := getString(cand.Value, field)
	b, _ := getString(cur.Value, field)
	return a != b
}

// grantTransform keeps Fields[grant] and Fields[run_state] from the
// target's own item and drops them from items new to it, and keeps
// Fields[account_bound] from the target's own item.
func grantTransform(row model.RowID, f map[string][]string) func(string, model.Item, *model.Item) (model.Item, []model.Change, error) {
	cleared := append(append([]string(nil), f[RoleRunState]...), f[RoleGrant]...)
	bound := f[RoleAccountBound]
	if len(cleared) == 0 && len(bound) == 0 {
		return nil
	}
	return func(k string, d model.Item, cur *model.Item) (model.Item, []model.Change, error) {
		if !isObject(d.Value) {
			return d, nil, nil
		}
		doc := newEditDoc(d)
		for _, fld := range cleared {
			if err := keepOrClear(doc, jsonx.Path{fld}, cur, true); err != nil {
				return d, nil, err
			}
		}
		for _, fld := range bound {
			if err := keepOrClear(doc, jsonx.Path{fld}, cur, false); err != nil {
				return d, nil, err
			}
		}
		if string(doc.val) == string(d.Value) {
			return d, nil, nil
		}
		out, err := doc.item(k, d.Stamp, d.Secret)
		return out, nil, err
	}
}
