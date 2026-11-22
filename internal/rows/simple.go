package rows

import (
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
