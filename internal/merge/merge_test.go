package merge

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
)

// fatalHelper is the part of testing.TB and *rapid.T the helpers use.
type fatalHelper interface {
	Helper()
	Fatal(args ...any)
}

func item(t fatalHelper, key, value string, st model.Stamp) *model.Item {
	t.Helper()
	it, err := model.NewItem(key, json.RawMessage(value), nil, st, false)
	if err != nil {
		t.Fatal(err)
	}
	return &it
}

func at(ms int64) model.Stamp { return model.Stamp{AppTime: ms} }

func TestVersionChanged(t *testing.T) {
	a := item(t, "k", `1`, model.Stamp{})
	b := item(t, "k", `2`, model.Stamp{})
	tests := []struct {
		name string
		v    Version
		want bool
	}{
		{"valid same", Version{Item: a, Base: a, BaseValid: true}, false},
		{"valid edited", Version{Item: b, Base: a, BaseValid: true}, true},
		{"valid added", Version{Item: a, BaseValid: true}, true},
		{"valid removed", Version{Base: a, BaseValid: true}, true},
		{"valid both absent", Version{BaseValid: true}, false},
		{"invalid present", Version{Item: a, Base: a}, true},
		{"invalid absent", Version{Base: a}, false},
		{"invalid absent tomb", Version{Tomb: &model.Tombstone{Key: "k", At: 5}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.Changed(); got != tc.want {
				t.Fatalf("Changed() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrefer(t *testing.T) {
	v := func(st model.Stamp, valid bool) Version {
		return Version{Item: item(t, "k", `"x"`, st), BaseValid: valid}
	}
	tests := []struct {
		name   string
		l, e   Version
		want   Side
		reason string
	}{
		{"higher epoch entering", v(model.Stamp{Epoch: 3, AppTime: 9}, true), v(model.Stamp{Epoch: 4, AppTime: 1}, true), Entering, ReasonEpoch},
		{"higher epoch leaving", v(model.Stamp{Epoch: 5}, true), v(model.Stamp{Epoch: 4, AppTime: 99}, true), Leaving, ReasonEpoch},
		{"equal epoch falls to time", v(model.Stamp{Epoch: 4, AppTime: 1}, true), v(model.Stamp{Epoch: 4, AppTime: 2}, true), Entering, ReasonAppTime},
		{"invalid base uses time", v(model.Stamp{Epoch: 9, AppTime: 1}, false), v(model.Stamp{Epoch: 1, AppTime: 2}, true), Entering, ReasonAppTime},
		{"tool written never wins", v(at(10), false), v(model.Stamp{AppTime: 99, ToolWritten: true}, false), Leaving, ReasonAppTime},
		{"tool written leaving loses", v(model.Stamp{AppTime: 99, ToolWritten: true}, false), v(at(10), false), Entering, ReasonAppTime},
		{"zero time never wins", v(at(0), false), v(at(3), false), Entering, ReasonAppTime},
		{"both unusable", v(model.Stamp{AppTime: 7, ToolWritten: true}, false), v(at(0), false), Leaving, ReasonLeavingSide},
		{"equal times", v(at(5), false), v(at(5), false), Leaving, ReasonLeavingSide},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, why := Prefer(tc.l, tc.e)
			if got != tc.want || why != tc.reason {
				t.Fatalf("Prefer = %v %q, want %v %q", got, why, tc.want, tc.reason)
			}
		})
	}
}

func TestScalar(t *testing.T) {
	base := item(t, "k", `"base"`, at(10))
	lEdit := item(t, "k", `"l"`, model.Stamp{Epoch: 5, AppTime: 20})
	eEdit := item(t, "k", `"e"`, model.Stamp{Epoch: 2, AppTime: 30})
	tomb := func(ms int64) *model.Tombstone { return &model.Tombstone{Key: "k", At: ms} }
	tests := []struct {
		name   string
		l, e   Version
		mode   string
		result *model.Item
		kind   model.ChangeKind
		loser  *model.Item
		reason string
	}{
		{"neither changed", Version{Item: base, Base: base, BaseValid: true}, Version{Item: base, Base: base, BaseValid: true}, "", base, "", nil, ""},
		{"only leaving", Version{Item: lEdit, Base: base, BaseValid: true}, Version{Item: base, Base: base, BaseValid: true}, "", lEdit, model.ChangeUpdated, nil, ""},
		{"only entering", Version{Item: base, Base: base, BaseValid: true}, Version{Item: eEdit, Base: base, BaseValid: true}, "", eEdit, "", nil, ""},
		{"both changed epoch", Version{Item: lEdit, Base: base, BaseValid: true}, Version{Item: eEdit, Base: base, BaseValid: true}, "", lEdit, model.ChangeUpdated, eEdit, ReasonEpoch},
		{"both changed invalid time", Version{Item: lEdit}, Version{Item: eEdit}, "", eEdit, "", lEdit, ReasonAppTime},
		{"same edit both sides", Version{Item: lEdit, Base: base, BaseValid: true}, Version{Item: lEdit, Base: base, BaseValid: true}, "", lEdit, "", nil, ""},
		{"tombstone newer deletes", Version{Base: base, BaseValid: true, Tomb: tomb(50)}, Version{Item: eEdit, Base: eEdit, BaseValid: true}, "", nil, model.ChangeDeleted, nil, ReasonTombstoneNewer},
		{"tombstone older revives", Version{Base: base, BaseValid: true, Tomb: tomb(25)}, Version{Item: eEdit, Base: eEdit, BaseValid: true}, "", eEdit, model.ChangeRevived, nil, ReasonRevived},
		{"tombstone equal revives", Version{Base: base, BaseValid: true, Tomb: tomb(30)}, Version{Item: eEdit, Base: eEdit, BaseValid: true}, "", eEdit, model.ChangeRevived, nil, ReasonRevived},
		{"delete wins", Version{Base: base, BaseValid: true, Tomb: tomb(1)}, Version{Item: eEdit, Base: eEdit, BaseValid: true}, TombstoneDeleteWins, nil, model.ChangeDeleted, nil, ReasonDeleteWins},
		{"tombstone vs edited copy keeps loser", Version{Base: base, BaseValid: true, Tomb: tomb(99)}, Version{Item: eEdit, Base: base, BaseValid: true}, "", nil, model.ChangeDeleted, eEdit, ReasonTombstoneNewer},
		{"removal without tombstone", Version{Base: base, BaseValid: true}, Version{Item: base, Base: base, BaseValid: true}, "", nil, model.ChangeDeleted, nil, ""},
		{"older removal vs edit keeps edit", Version{Base: base, BaseValid: true}, Version{Item: eEdit, Base: base, BaseValid: true}, "", eEdit, "", nil, ReasonEpoch},
		{"later removal beats an older edit", Version{Base: base, BaseValid: true, Epoch: 7}, Version{Item: eEdit, Base: base, BaseValid: true, Epoch: 2}, "", nil, model.ChangeDeleted, eEdit, ReasonEpoch},
		{"missing restored", Version{Item: base, Base: base, BaseValid: true}, Version{BaseValid: false}, "", base, model.ChangeRestored, nil, ReasonRestored},
		{"missing valid base restored", Version{Item: base, Base: base, BaseValid: true}, Version{BaseValid: true}, "", base, model.ChangeRestored, nil, ReasonRestored},
		{"invalid entering never deleted", Version{Base: base, BaseValid: true, Tomb: tomb(99)}, Version{Item: eEdit}, "", eEdit, "", nil, ReasonRestored},
		{"entering own removal stands", Version{Item: base, Base: base, BaseValid: true}, Version{Base: base, BaseValid: true}, "", nil, "", nil, ""},
		{"entering tombstone older revives", Version{Item: lEdit, Base: lEdit, BaseValid: true}, Version{Base: base, BaseValid: true, Tomb: tomb(5)}, "", lEdit, model.ChangeRevived, nil, ReasonRevived},
		{"both removed", Version{Base: base, BaseValid: true}, Version{Base: base, BaseValid: true}, "", nil, "", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := Scalar("k", tc.l, tc.e, tc.mode)
			if !sameItem(o.Result, tc.result) {
				t.Fatalf("Result = %v, want %v", o.Result, tc.result)
			}
			if o.Kind != tc.kind {
				t.Fatalf("Kind = %q, want %q", o.Kind, tc.kind)
			}
			if !sameItem(o.Loser, tc.loser) {
				t.Fatalf("Loser = %v, want %v", o.Loser, tc.loser)
			}
			if o.Reason != tc.reason {
				t.Fatalf("Reason = %q, want %q", o.Reason, tc.reason)
			}
		})
	}
}

func sameItem(a, b *model.Item) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Hash == b.Hash
}

func TestKeyedFieldMerge(t *testing.T) {
	base := item(t, "r", `{"a":1,"b":1}`, at(1))
	l := item(t, "r", `{"a":2,"b":1}`, model.Stamp{Epoch: 3})
	e := item(t, "r", `{"a":1,"b":2}`, model.Stamp{Epoch: 2})
	mergeAB := func(base, lv, ev []byte) ([]byte, bool) {
		var b, x, y map[string]int
		for _, p := range []struct {
			raw []byte
			dst *map[string]int
		}{{base, &b}, {lv, &x}, {ev, &y}} {
			if err := json.Unmarshal(p.raw, p.dst); err != nil {
				return nil, false
			}
		}
		out := map[string]int{}
		for _, f := range []string{"a", "b"} {
			out[f] = x[f]
			if x[f] == b[f] {
				out[f] = y[f]
			}
		}
		raw, _ := json.Marshal(out)
		return raw, true
	}
	ver := func(it *model.Item) func(string) Version {
		return func(string) Version { return Version{Item: it, Base: base, BaseValid: true} }
	}
	tests := []struct {
		name  string
		field FieldMerger
		want  string
		loser bool
	}{
		{"field merge", mergeAB, `{"a":2,"b":2}`, false},
		{"no merger", nil, `{"a":2,"b":1}`, true},
		{"merger declines", func(_, _, _ []byte) ([]byte, bool) { return nil, false }, `{"a":2,"b":1}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outs := Keyed([]string{"r"}, ver(l), ver(e), tc.field, TombstoneNewerWins)
			if len(outs) != 1 {
				t.Fatalf("got %d outcomes", len(outs))
			}
			want := item(t, "r", tc.want, model.Stamp{})
			if !sameItem(outs[0].Result, want) {
				t.Fatalf("Result = %s, want %s", outs[0].Result.Value, tc.want)
			}
			if (outs[0].Loser != nil) != tc.loser {
				t.Fatalf("Loser = %v, want present %v", outs[0].Loser, tc.loser)
			}
		})
	}
}

func TestSet(t *testing.T) {
	tests := []struct {
		name           string
		base, l, e     []string
		lValid, eValid bool
		want           []string
	}{
		{"adds both", []string{"a"}, []string{"a", "b"}, []string{"a", "c"}, true, true, []string{"a", "b", "c"}},
		{"valid removes", []string{"a", "b"}, []string{"a"}, []string{"a", "b"}, true, true, []string{"a"}},
		{"invalid never removes", []string{"a", "b"}, []string{"a"}, []string{"a", "b"}, false, true, []string{"a", "b"}},
		{"removed and readded elsewhere", []string{"a"}, nil, []string{"a"}, true, true, nil},
		{"both invalid union", []string{"x"}, []string{"b"}, []string{"a"}, false, false, []string{"a", "b", "x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Set(tc.base, tc.l, tc.e, tc.lValid, tc.eValid)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Set = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOrderedList(t *testing.T) {
	tests := []struct {
		name                string
		base, winner, loser []string
		wValid, lValid      bool
		want                []string
	}{
		{"winner order kept", []string{"a", "b", "c"}, []string{"c", "b", "a"}, []string{"a", "b", "c"}, true, true, []string{"c", "b", "a"}},
		{"loser insert after neighbour", []string{"a", "b"}, []string{"b", "a"}, []string{"a", "x", "b"}, true, true, []string{"b", "a", "x"}},
		{"loser insert first", []string{"a", "b"}, []string{"b", "a"}, []string{"x", "a", "b"}, true, true, []string{"x", "b", "a"}},
		{"loser run keeps order", []string{"a"}, []string{"a"}, []string{"a", "x", "y"}, true, true, []string{"a", "x", "y"}},
		{"winner removed drops loser copy", []string{"a", "b"}, []string{"a"}, []string{"a", "b"}, true, true, []string{"a"}},
		{"winner invalid keeps loser item", []string{"a", "b"}, []string{"a"}, []string{"a", "b"}, false, true, []string{"a", "b"}},
		{"loser removed drops", []string{"a", "b"}, []string{"a", "b"}, []string{"a"}, true, true, []string{"a"}},
		{"loser invalid never removes", []string{"a", "b"}, []string{"a", "b"}, []string{"a"}, true, false, []string{"a", "b"}},
		{"neighbour removed falls back", []string{"a", "b"}, []string{"b"}, []string{"b", "a", "x"}, true, true, []string{"b", "x"}},
		{"duplicates dropped", nil, []string{"a", "a"}, []string{"b", "b"}, false, false, []string{"b", "a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := OrderedList(tc.base, tc.winner, tc.loser, tc.wValid, tc.lValid)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("OrderedList = %v, want %v", got, tc.want)
			}
		})
	}
}
