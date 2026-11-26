package rows

import (
	"slices"
	"strings"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
)

// FamilyRecord is one session record's family links: the ids its family
// fields name.
type FamilyRecord struct {
	ID      string
	Parents []string
	// Item is the record (for field tests such as isArchived).
	Item model.Item
}

// FamilyRecords collects the family links of the records of snaps, union
// by id (an earlier snapshot wins). fields are dotted paths into a record
// (manifest online.pins.family_fields); a field that is absent or not a
// non-empty string adds no link.
func FamilyRecords(snaps []*model.RowSnapshot, fields []string) []FamilyRecord {
	seen := map[string]bool{}
	var out []FamilyRecord
	for _, s := range snaps {
		if s == nil {
			continue
		}
		keys := s.Keys()
		for _, k := range keys {
			if seen[k] {
				continue
			}
			seen[k] = true
			it := s.Items[k]
			rec := FamilyRecord{ID: k, Item: it}
			for _, f := range fields {
				if p, ok := getStringPath(it.Value, jsonx.Path(strings.Split(f, "."))); ok && p != "" && p != k &&
					!slices.Contains(rec.Parents, p) {
					rec.Parents = append(rec.Parents, p)
				}
			}
			out = append(out, rec)
		}
	}
	return out
}

// Ancestors returns, for an id, every id reached by following any family
// link from it, transitively, up to maxDepth links, stopping at cycles
// and at ids that are not records of recs (they are not returned). The
// result is sorted. Bounded and cycle-safe.
func Ancestors(recs []FamilyRecord, maxDepth int) func(id string) []string {
	parents := make(map[string][]string, len(recs))
	for _, r := range recs {
		parents[r.ID] = r.Parents
	}
	return func(id string) []string {
		seen := map[string]bool{id: true}
		frontier := []string{id}
		var out []string
		for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
			var next []string
			for _, x := range frontier {
				for _, p := range parents[x] {
					if seen[p] {
						continue
					}
					seen[p] = true
					if _, known := parents[p]; !known {
						continue
					}
					out = append(out, p)
					next = append(next, p)
				}
			}
			frontier = next
		}
		slices.Sort(out)
		return out
	}
}
