package rows

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// fatalHelper is the part of testing.TB and *rapid.T the helpers use.
type fatalHelper interface {
	Helper()
	Fatalf(format string, args ...any)
}

func fakeUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

func fakeSession(n int) string { return "local_" + fakeUUID(n) }

const (
	aliasA ids.Alias = "alpha"
	aliasB ids.Alias = "beta"
)

func mk(t fatalHelper, key, value string, appTime int64) model.Item {
	t.Helper()
	it, err := model.NewItem(key, json.RawMessage(value), []byte(value), model.Stamp{AppTime: appTime}, false)
	if err != nil {
		t.Fatalf("item %s: %v", key, err)
	}
	return it
}

func snap(row model.RowID, items ...model.Item) *model.RowSnapshot {
	s := &model.RowSnapshot{Row: row, Schema: 1, Items: map[string]model.Item{}}
	for _, it := range items {
		s.Items[it.Key] = it
	}
	return s
}

func withTombs(s *model.RowSnapshot, ts ...model.Tombstone) *model.RowSnapshot {
	if s.Tombstones == nil {
		s.Tombstones = map[string]model.Tombstone{}
	}
	for _, t := range ts {
		s.Tombstones[t.Key] = t
	}
	return s
}

func openPolicy() Policy {
	return Policy{TombstoneMode: "newer-wins", LeavingExports: true, EnteringExports: true, EnteringImports: true}
}

// input builds a two-replica input where both sides have valid bases.
func input(row model.RowID, lCur, lBase, eCur, eBase, canon *model.RowSnapshot, pol Policy) Input {
	return Input{
		Row:       row,
		Leaving:   Side{Alias: aliasA, Snapshot: lCur, Base: lBase, BaseValid: true, LiveEpoch: 7},
		Entering:  Side{Alias: aliasB, Snapshot: eCur, Base: eBase, BaseValid: true, LiveEpoch: 3},
		Canonical: canon,
		Policy:    pol,
	}
}

func mustMerge(t *testing.T, row model.RowID, in Input) Output {
	t.Helper()
	out, err := Registry()[row].Merge(in)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	checkHashes(t, out)
	return out
}

// checkHashes asserts the item hash contract on every output snapshot.
func checkHashes(t fatalHelper, out Output) {
	t.Helper()
	for name, s := range map[string]*model.RowSnapshot{"desired": out.Desired, "canonical": out.Canonical, "lo": out.LeavingOverlay, "eo": out.EnteringOverlay} {
		if s == nil {
			continue
		}
		for k, it := range s.Items {
			h, err := model.ItemHash(it.Value)
			if err != nil || h != it.Hash || it.Key != k {
				t.Fatalf("%s item %q: hash/key contract broken (err %v)", name, k, err)
			}
		}
	}
}

func field(t fatalHelper, it model.Item, name string) any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(it.Value, &m); err != nil {
		t.Fatalf("decode %s: %v", it.Key, err)
	}
	return m[name]
}

func hasChange(out Output, kind model.ChangeKind, key string) bool {
	for _, c := range out.Changes {
		if c.Kind == kind && c.Key == key {
			return true
		}
	}
	return false
}
