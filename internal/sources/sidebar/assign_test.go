package sidebar

import (
	"encoding/json"
	"testing"

	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

func memLS(t *testing.T, name, text string) kv.Reader {
	t.Helper()
	s := kv.NewMem()
	key, err := localstorage.Key(name)
	if err != nil {
		t.Fatal(err)
	}
	var b kv.Batch
	b.Put(key, localstorage.EncodeString(text))
	if err := s.Write(&b); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCodeAssignments(t *testing.T) {
	scope := "00000000-0000-4000-8000-000000000001/00000000-0000-4000-8000-000000000002"
	path := []string{"state", "customGroupsByScope", scope}
	doc, _ := json.Marshal(map[string]any{"state": map[string]any{"customGroupsByScope": map[string]any{scope: map[string]any{
		"groups":      []any{map[string]any{"id": "g1", "name": "x"}},
		"assignments": map[string]any{"code:local_00000000-0000-4000-8000-000000000001": "g1"},
		"order":       []any{"g1"},
	}}}, "version": 3})
	a, g, err := CodeAssignments(memLS(t, "dframe-store", string(doc)), "dframe-store", path)
	if err != nil || a["code:local_00000000-0000-4000-8000-000000000001"] != "g1" || !g["g1"] {
		t.Fatalf("a=%v g=%v err=%v", a, g, err)
	}
	a, g, err = CodeAssignments(kv.NewMem(), "dframe-store", path)
	if err != nil || len(a) != 0 || len(g) != 0 {
		t.Fatalf("absent: %v %v %v", a, g, err)
	}
	if _, _, err := CodeAssignments(nil, "dframe-store", path); err != nil {
		t.Fatalf("nil reader: %v", err)
	}
	bad, _ := json.Marshal(map[string]any{"state": map[string]any{"customGroupsByScope": map[string]any{scope: map[string]any{"groups": []any{1}}}}})
	if _, _, err := CodeAssignments(memLS(t, "dframe-store", string(bad)), "dframe-store", path); err == nil {
		t.Fatal("bad groups accepted")
	}
}
