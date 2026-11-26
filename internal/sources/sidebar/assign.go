package sidebar

import (
	"encoding/json"
	"fmt"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// CodeAssignments reads, read-only, the custom-group assignments of one
// sidebar scope: the Local Storage item storeKey (a zustand persisted
// store) at path (already expanded for the replica), members
// "assignments" (row key -> group id) and "groups" (objects with a string
// "id"). It is used only to predict which unpins the app would refuse
// because the session's own section is hidden (spec-pins 8.3). An absent
// item or scope gives empty maps; any other shape is an error.
func CodeAssignments(ls kv.Reader, storeKey string, path []string) (assign map[string]string, groups map[string]bool, err error) {
	assign, groups = map[string]string{}, map[string]bool{}
	doc, err := sources.LoadLS(ls, storeKey)
	if err != nil {
		return nil, nil, err
	}
	raw, found, err := doc.Get(jsonx.Path(path))
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return assign, groups, nil
	}
	var scope struct {
		Assignments map[string]json.RawMessage `json:"assignments"`
		Groups      []json.RawMessage          `json:"groups"`
	}
	if err := json.Unmarshal(raw, &scope); err != nil {
		return nil, nil, fmt.Errorf("sidebar: %s: custom groups: unexpected shape", storeKey)
	}
	for k, v := range scope.Assignments {
		var id string
		if json.Unmarshal(v, &id) != nil {
			return nil, nil, fmt.Errorf("sidebar: %s: custom groups: unexpected shape", storeKey)
		}
		assign[k] = id
	}
	for _, g := range scope.Groups {
		var obj struct {
			ID *string `json:"id"`
		}
		if json.Unmarshal(g, &obj) != nil || obj.ID == nil {
			return nil, nil, fmt.Errorf("sidebar: %s: custom groups: unexpected shape", storeKey)
		}
		groups[*obj.ID] = true
	}
	return assign, groups, nil
}
