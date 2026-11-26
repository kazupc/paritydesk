package online

import (
	"encoding/json"
	"slices"
	"sort"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/state"
)

// ConfirmBases are the entering profile's bases and observations that a
// confirmation works on (16.4). The pins unit adds its own part.
type ConfirmBases struct {
	// SidebarBase is T's sidebar-view base (nil: none).
	SidebarBase *model.RowSnapshot
	// Observed are T's sidebar-view items read from the config mirror now
	// (nil: not readable; the sidebar part waits for the next holder).
	Observed *model.RowSnapshot
	// Contested are T's contested sidebar-view entries by item key.
	Contested map[string]model.Contested
	Spec      compat.OnlineSidebarSpec
}

// ConfirmEdits are what a confirmation changes in root.
type ConfirmEdits struct {
	// SidebarBase is T's new sidebar-view base (nil: unchanged).
	SidebarBase *model.RowSnapshot
	// ContestedDelete are item keys whose contested entry goes;
	// ContestedUpdate the entries whose ServerHash moves to the live
	// state.
	ContestedDelete []string
	ContestedUpdate map[string]model.Contested
	// Confirmed are the step ids to append to root.Online.Confirmed.
	Confirmed []string
	// BasesAdvanced counts base items moved; ContestedCleared contested
	// entries removed.
	BasesAdvanced, ContestedCleared int
}

// ConfirmPlan decides, for the done steps not confirmed yet, which base
// items move to the applied values and which contested entries go
// (16.4). It is pure and idempotent: steps listed in rec.Confirmed are
// skipped.
func ConfirmPlan(rec *state.OnlineRecord, steps []Step, b ConfirmBases) ConfirmEdits {
	var e ConfirmEdits
	if rec == nil {
		return e
	}
	var base *model.RowSnapshot
	if b.SidebarBase != nil {
		base = b.SidebarBase.Clone()
	}
	var observed map[string]storedSection
	var observedKey map[string]string
	if b.Observed != nil {
		observed, observedKey = observedSections(b.Observed, b.Spec)
	}
	changed := false
	for _, s := range steps {
		if s.Status != StatusDone || s.Kind != KindSidebar || slices.Contains(rec.Confirmed, string(s.ID)) {
			continue
		}
		if b.Observed == nil {
			continue // retried by the next holder of the run lock
		}
		var d SidebarData
		if json.Unmarshal(s.Data, &d) != nil {
			e.Confirmed = append(e.Confirmed, string(s.ID))
			continue
		}
		itemKey, ok := observedKey[d.Key]
		if !ok {
			e.Confirmed = append(e.Confirmed, string(s.ID))
			continue
		}
		obs := observed[d.Key]
		obsItem := b.Observed.Items[itemKey]
		if base != nil {
			if it, ok := base.Items[itemKey]; ok {
				if patched, n := patchMembers(it, obsItem, obs, d, b.Spec); n > 0 {
					base.Items[itemKey] = patched
					e.BasesAdvanced += n
					changed = true
				}
			}
		}
		if c, ok := b.Contested[itemKey]; ok {
			if obsItem.Hash == c.IntendedHash {
				e.ContestedDelete = append(e.ContestedDelete, itemKey)
				e.ContestedCleared++
			} else if c.ServerHash != obsItem.Hash {
				c.ServerHash = obsItem.Hash
				if e.ContestedUpdate == nil {
					e.ContestedUpdate = map[string]model.Contested{}
				}
				e.ContestedUpdate[itemKey] = c
			}
		}
		e.Confirmed = append(e.Confirmed, string(s.ID))
	}
	if changed {
		e.SidebarBase = base
	}
	sort.Strings(e.ContestedDelete)
	return e
}

// observedSections maps step keys to observed sections and their item keys.
func observedSections(snap *model.RowSnapshot, sp compat.OnlineSidebarSpec) (map[string]storedSection, map[string]string) {
	secs, _, dup := sections(snap, sp)
	keys := map[string]string{}
	for k, s := range secs {
		if dup[k] {
			delete(secs, k)
			continue
		}
		keys[k] = sectionItemPrefix + s.id
	}
	return secs, keys
}

// patchMembers copies, for each field the step intended, the observed raw
// member into the base item when the observed value (reported spelling)
// equals the intent. Other members of the base item stay.
func patchMembers(baseItem, obsItem model.Item, obs storedSection, d SidebarData, sp compat.OnlineSidebarSpec) (model.Item, int) {
	var baseObj, obsObj map[string]json.RawMessage
	if json.Unmarshal(baseItem.Value, &baseObj) != nil || json.Unmarshal(obsItem.Value, &obsObj) != nil {
		return baseItem, 0
	}
	n := 0
	if d.Intended.GroupBy != nil && obs.groupBy != nil {
		if rep, ok := sp.GroupByReport[*obs.groupBy]; ok && rep == *d.Intended.GroupBy {
			if string(baseObj[sp.GroupByField]) != string(obsObj[sp.GroupByField]) {
				baseObj[sp.GroupByField] = obsObj[sp.GroupByField]
				n++
			}
		}
	}
	if d.Intended.SortBy != nil && obs.sortBy != nil && *obs.sortBy == *d.Intended.SortBy {
		if string(baseObj[sp.SortByField]) != string(obsObj[sp.SortByField]) {
			baseObj[sp.SortByField] = obsObj[sp.SortByField]
			n++
		}
	}
	if n == 0 {
		return baseItem, 0
	}
	v, err := json.Marshal(baseObj)
	if err != nil {
		return baseItem, 0
	}
	c, err := model.Canonical(v)
	if err != nil {
		return baseItem, 0
	}
	it, err := model.NewItem(baseItem.Key, c, c, baseItem.Stamp, baseItem.Secret)
	if err != nil {
		return baseItem, 0
	}
	return it, n
}
