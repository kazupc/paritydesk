package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

const (
	configFile = "claude_desktop_config.json"
	storeKey   = "dframe-store"
)

var (
	configLoc = Location{File: configFile}
	storeLoc  = Location{LSKey: storeKey}
)

// memSource is a Source over in-memory files and encoded LS values.
type memSource struct {
	files map[string][]byte
	ls    map[string][]byte
	err   error
}

func (m memSource) read(loc Location) ([]byte, bool, error) {
	if m.err != nil {
		return nil, false, m.err
	}
	if loc.LSKey != "" {
		v, ok := m.ls[loc.LSKey]
		return v, ok, nil
	}
	v, ok := m.files[loc.File]
	return v, ok, nil
}

func hashOf(b []byte) string { return model.HashBytes(b) }

func baseConfig() []byte {
	return []byte(`{
  "preferences": {"epitaxyPrefs": {"groupOrderKey": "[\"a\",\"b\"]", "pinnedOrderKey": "[]"}, "other": 1},
  "mcpServers": {"one": {"command": "x"}}
}`)
}

func baseStoreText() string {
	return `{"state":{"pinnedOrder":[],"sectionsView":{"collapsed":false}},"version":1}`
}

func sharedTargetsFixture() (memSource, []Edit) {
	cfg := baseConfig()
	store := localstorage.EncodeString(baseStoreText())
	src := memSource{files: map[string][]byte{configFile: cfg}, ls: map[string][]byte{storeKey: store}}
	ch, sh := hashOf(cfg), hashOf(store)
	edits := []Edit{
		{Loc: configLoc, Row: model.RowGroupOrder, Item: "order", Kind: EditSet,
			Path: jsonx.Path{"preferences", "epitaxyPrefs", "groupOrderKey"}, Value: json.RawMessage(`"[\"b\",\"a\"]"`), BaseHash: ch},
		{Loc: storeLoc, Row: model.RowSidebarView, Item: "field:collapsed", Kind: EditSet,
			Path: jsonx.Path{"state", "sectionsView", "collapsed"}, Value: json.RawMessage(`true`), BaseHash: sh},
		{Loc: configLoc, Row: model.RowPinnedOrder, Item: "order", Kind: EditSet,
			Path: jsonx.Path{"preferences", "epitaxyPrefs", "pinnedOrderKey"}, Value: json.RawMessage(`"[\"s1\"]"`), BaseHash: ch},
		{Loc: storeLoc, Row: model.RowPinnedOrder, Item: "order", Kind: EditSet,
			Path: jsonx.Path{"state", "pinnedOrder"}, Value: json.RawMessage(`["s1"]`), BaseHash: sh},
		{Loc: configLoc, Row: model.RowMCPServers, Item: "two", Kind: EditSet,
			Path: jsonx.Path{"mcpServers", "two"}, Value: json.RawMessage(`{"command":"y"}`), BaseHash: ch},
	}
	return src, edits
}

func TestComposeSharedTargets(t *testing.T) {
	src, edits := sharedTargetsFixture()
	ops, err := Compose(edits, src.read)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("got %d ops, want one per target", len(ops))
	}
	cfg, store := ops[0], ops[1]
	if cfg.Kind != OpWriteFile || cfg.Path != configFile || store.Kind != OpLSPut || store.Key != storeKey {
		t.Fatalf("targets or order: %+v / %+v", cfg, store)
	}
	wantCfg := `{
  "preferences": {"epitaxyPrefs": {"groupOrderKey": "[\"b\",\"a\"]", "pinnedOrderKey": "[\"s1\"]"}, "other": 1},
  "mcpServers": {"one": {"command": "x"},"two":{"command":"y"}}
}`
	if string(cfg.Content) != wantCfg {
		t.Fatalf("config content:\n%s", cfg.Content)
	}
	wantStore := localstorage.EncodeString(`{"state":{"pinnedOrder":["s1"],"sectionsView":{"collapsed":true}},"version":1}`)
	if !bytes.Equal(store.Content, wantStore) {
		t.Fatalf("store content: %q", store.Content)
	}
	if cfg.PreHash != hashOf(src.files[configFile]) || cfg.PostHash != hashOf(cfg.Content) {
		t.Fatal("config hashes")
	}
	if store.PreHash != hashOf(src.ls[storeKey]) || store.PostHash != hashOf(store.Content) {
		t.Fatal("store hashes")
	}
	wantRows := []model.RowID{model.RowGroupOrder, model.RowPinnedOrder, model.RowMCPServers}
	if cfg.Row != model.RowGroupOrder || !equalRows(cfg.Rows, wantRows) {
		t.Fatalf("config rows %s %v", cfg.Row, cfg.Rows)
	}
	if store.Row != model.RowSidebarView || !equalRows(store.Rows, []model.RowID{model.RowSidebarView, model.RowPinnedOrder}) {
		t.Fatalf("store rows %s %v", store.Row, store.Rows)
	}
	if got := cfg.Items; len(got) != 2 || got[0] != "order" || got[1] != "two" {
		t.Fatalf("config items %v", got)
	}
}

func equalRows(a, b []model.RowID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestComposeOrderFollowsInput(t *testing.T) {
	src, edits := sharedTargetsFixture()
	// Move the store edit first: the store op must then come first.
	edits[0], edits[1] = edits[1], edits[0]
	ops, err := Compose(edits, src.read)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0].Key != storeKey || ops[1].Path != configFile {
		t.Fatalf("order: %+v", ops)
	}
	if ops[1].Row != model.RowGroupOrder {
		t.Fatalf("first contributing row: %s", ops[1].Row)
	}
}

func TestComposeSingleRowHasNoRows(t *testing.T) {
	src := memSource{files: map[string][]byte{}}
	ops, err := Compose([]Edit{{Loc: Location{File: "a/new.json"}, Row: model.RowTasks, Item: "backlog/1",
		Kind: EditReplace, Value: json.RawMessage(`{"x":1}`), Mode: 0o644}}, src.read)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Rows != nil || ops[0].PreHash != "" || ops[0].Mode != 0o644 {
		t.Fatalf("%+v", ops)
	}
}

func TestComposeRefusals(t *testing.T) {
	cfg := baseConfig()
	ch := hashOf(cfg)
	src := memSource{files: map[string][]byte{configFile: cfg},
		ls: map[string][]byte{storeKey: {localstorage.FlagUTF16, 0x00, 0xD8}}} // lone surrogate: no round trip
	set := func(row model.RowID, path ...string) Edit {
		return Edit{Loc: configLoc, Row: row, Kind: EditSet, Path: path, Value: json.RawMessage(`1`), BaseHash: ch}
	}
	del := func(row model.RowID, path ...string) Edit {
		e := set(row, path...)
		e.Kind, e.Value = EditDelete, nil
		return e
	}
	stale := set(model.RowGroupOrder, "preferences", "x")
	stale.BaseHash = hashOf([]byte("older"))
	tests := []struct {
		name  string
		edits []Edit
	}{
		{"stale base", []Edit{stale}},
		{"absent base claimed present", []Edit{{Loc: Location{File: "missing.json"}, Row: model.RowTasks, Kind: EditReplace, Value: json.RawMessage(`{}`), BaseHash: ch}}},
		{"equal paths from two rows", []Edit{set(model.RowGroupOrder, "preferences", "x"), set(model.RowPinnedOrder, "preferences", "x")}},
		{"prefix paths from two rows", []Edit{set(model.RowGroupOrder, "preferences"), del(model.RowPinnedOrder, "preferences", "other")}},
		{"nested reverse order", []Edit{del(model.RowGroupOrder, "preferences", "other"), set(model.RowPinnedOrder, "preferences")}},
		{"replace with another row", []Edit{{Loc: configLoc, Row: model.RowTasks, Kind: EditReplace, Value: json.RawMessage(`{}`), BaseHash: ch}, set(model.RowGroupOrder, "a")}},
		{"remove mixed with set", []Edit{{Loc: configLoc, Row: model.RowMCPServers, Kind: EditRemove, BaseHash: ch}, set(model.RowMCPServers, "a")}},
		{"set mixed with remove", []Edit{set(model.RowMCPServers, "a"), {Loc: configLoc, Row: model.RowMCPServers, Kind: EditRemove, BaseHash: ch}}},
		{"LS value without round trip", []Edit{{Loc: storeLoc, Row: model.RowPinnedOrder, Kind: EditSet, Path: jsonx.Path{"a"}, Value: json.RawMessage(`1`), BaseHash: hashOf(src.ls[storeKey])}}},
		{"set in absent document", []Edit{{Loc: Location{File: "missing.json"}, Row: model.RowTasks, Kind: EditSet, Path: jsonx.Path{"a"}, Value: json.RawMessage(`1`)}}},
		{"missing parent", []Edit{set(model.RowGroupOrder, "nope", "x")}},
		{"invalid value", []Edit{{Loc: configLoc, Row: model.RowGroupOrder, Kind: EditSet, Path: jsonx.Path{"a"}, Value: json.RawMessage(`{`), BaseHash: ch}}},
		{"unknown kind", []Edit{{Loc: configLoc, Row: model.RowGroupOrder, Kind: "patch", BaseHash: ch}}},
		{"no location", []Edit{{Row: model.RowGroupOrder, Kind: EditSet}}},
		{"two locations", []Edit{{Loc: Location{File: "a", LSKey: "b"}, Row: model.RowGroupOrder, Kind: EditSet}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ops, err := Compose(tc.edits, src.read)
			if err == nil {
				t.Fatalf("accepted: %+v", ops)
			}
			assertInternal(t, err)
		})
	}
}

func assertInternal(t *testing.T, err error) {
	t.Helper()
	var pe *pderr.Error
	if !errors.As(err, &pe) || pe.Code != pderr.Internal {
		t.Fatalf("want *pderr.Error Internal, got %T %v", err, err)
	}
}

func TestComposeSourceError(t *testing.T) {
	_, err := Compose([]Edit{{Loc: configLoc, Row: model.RowGroupOrder, Kind: EditDelete, Path: jsonx.Path{"a"}}},
		memSource{err: errors.New("boom")}.read)
	assertInternal(t, err)
	if _, err := Compose(nil, nil); err == nil {
		t.Fatal("nil source accepted")
	}
}

func TestComposeSameRowMayNest(t *testing.T) {
	src := memSource{files: map[string][]byte{configFile: []byte(`{"a":1}`)}}
	h := hashOf(src.files[configFile])
	ops, err := Compose([]Edit{
		{Loc: configLoc, Row: model.RowEnvironments, Item: "preset/1", Kind: EditSet, Path: jsonx.Path{"p"}, Value: json.RawMessage(`{}`), BaseHash: h},
		{Loc: configLoc, Row: model.RowEnvironments, Item: "preset/1", Kind: EditSet, Path: jsonx.Path{"p", "q"}, Value: json.RawMessage(`2`), BaseHash: h},
	}, src.read)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || string(ops[0].Content) != `{"a":1,"p":{"q":2}}` || len(ops[0].Items) != 1 {
		t.Fatalf("%+v %s", ops, ops[0].Content)
	}
}

func TestComposeDeleteAbsentIsNoOp(t *testing.T) {
	doc := []byte(`{"a":{"b":1},"c":2}`)
	src := memSource{files: map[string][]byte{configFile: doc}}
	h := hashOf(doc)
	tests := []struct {
		name  string
		edits []Edit
		want  string // "" = no op
	}{
		{"absent member", []Edit{{Loc: configLoc, Row: model.RowMCPServers, Kind: EditDelete, Path: jsonx.Path{"zz"}, BaseHash: h}}, ""},
		{"absent parent", []Edit{{Loc: configLoc, Row: model.RowMCPServers, Kind: EditDelete, Path: jsonx.Path{"zz", "y"}, BaseHash: h}}, ""},
		{"absent file", []Edit{{Loc: Location{File: "gone.json"}, Row: model.RowMCPServers, Kind: EditDelete, Path: jsonx.Path{"zz"}}}, ""},
		{"absent among real", []Edit{
			{Loc: configLoc, Row: model.RowMCPServers, Kind: EditDelete, Path: jsonx.Path{"zz"}, BaseHash: h},
			{Loc: configLoc, Row: model.RowMCPServers, Kind: EditDelete, Path: jsonx.Path{"c"}, BaseHash: h},
		}, `{"a":{"b":1}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ops, err := Compose(tc.edits, src.read)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(ops) != 0 {
					t.Fatalf("got ops %+v", ops)
				}
				return
			}
			if len(ops) != 1 || string(ops[0].Content) != tc.want {
				t.Fatalf("got %+v", ops)
			}
		})
	}
}

func TestComposeNoChangeYieldsNoOp(t *testing.T) {
	cfg := baseConfig()
	store := localstorage.EncodeString(baseStoreText())
	src := memSource{files: map[string][]byte{configFile: cfg}, ls: map[string][]byte{storeKey: store}}
	ch, sh := hashOf(cfg), hashOf(store)
	tests := []struct {
		name  string
		edits []Edit
	}{
		{"set same value", []Edit{{Loc: configLoc, Row: model.RowMCPServers, Kind: EditSet,
			Path: jsonx.Path{"mcpServers", "one"}, Value: json.RawMessage(`{"command": "x"}`), BaseHash: ch}}},
		{"set then restore", []Edit{
			{Loc: configLoc, Row: model.RowMCPServers, Kind: EditSet, Path: jsonx.Path{"mcpServers", "one"}, Value: json.RawMessage(`2`), BaseHash: ch},
			{Loc: configLoc, Row: model.RowMCPServers, Kind: EditSet, Path: jsonx.Path{"mcpServers", "one"}, Value: json.RawMessage(`{"command": "x"}`), BaseHash: ch},
		}},
		{"replace same bytes", []Edit{{Loc: configLoc, Row: model.RowTasks, Kind: EditReplace, Value: cfg, BaseHash: ch}}},
		{"LS replace same text", []Edit{{Loc: storeLoc, Row: model.RowPinnedOrder, Kind: EditReplace, Value: json.RawMessage(baseStoreText()), BaseHash: sh}}},
		{"remove absent", []Edit{{Loc: Location{File: "gone.json"}, Row: model.RowSessions, Kind: EditRemove}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ops, err := Compose(tc.edits, src.read)
			if err != nil {
				t.Fatal(err)
			}
			if len(ops) != 0 {
				t.Fatalf("got %+v", ops)
			}
		})
	}
}

// wideText is a JSON string that needs UTF-16 storage (code units above 0xFF).
const wideText = "\"\u00e9\u2603\""

func TestComposeRemoveAndLSReplace(t *testing.T) {
	file := []byte("x")
	store := localstorage.EncodeString("old")
	src := memSource{files: map[string][]byte{"s/local_1.json": file}, ls: map[string][]byte{storeKey: store}}
	ops, err := Compose([]Edit{
		{Loc: Location{File: "s/local_1.json"}, Row: model.RowSessions, Item: "local_1", Kind: EditRemove, BaseHash: hashOf(file), Destructive: true},
		{Loc: storeLoc, Row: model.RowSidebarView, Kind: EditRemove, BaseHash: hashOf(store)},
		{Loc: Location{LSKey: "new-key"}, Row: model.RowSidebarView, Kind: EditReplace, Value: json.RawMessage(wideText), Secret: true},
	}, src.read)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 {
		t.Fatalf("%+v", ops)
	}
	if ops[0].Kind != OpQuarantine || ops[0].PreHash != hashOf(file) || ops[0].PostHash != "" || !ops[0].Destructive || ops[0].Content != nil {
		t.Fatalf("quarantine %+v", ops[0])
	}
	if ops[1].Kind != OpLSDelete || ops[1].PreHash != hashOf(store) || ops[1].PostHash != "" {
		t.Fatalf("ls delete %+v", ops[1])
	}
	want := localstorage.EncodeString(wideText)
	if want[0] != localstorage.FlagUTF16 {
		t.Fatal("fixture should need UTF-16")
	}
	if ops[2].Kind != OpLSPut || !bytes.Equal(ops[2].Content, want) || !ops[2].Secret || ops[2].PreHash != "" {
		t.Fatalf("ls put %+v", ops[2])
	}
}

// FuzzCompose applies random set and delete edits to valid JSON objects.
// Compose must never panic, and when it succeeds every byte outside the
// edited top-level members must be unchanged: deleting the touched
// members from the original and from the result yields the same bytes.
func FuzzCompose(f *testing.F) {
	f.Add([]byte(`{"a":1,"b":{"c":[1,2]},"d":"x"}`), []byte{0, 1, 2, 1, 0, 3, 2, 2, 1})
	f.Add([]byte(` { "k" : { } , "z":null } `), []byte{0, 0, 1, 1, 1, 0})
	f.Add([]byte(`{}`), []byte{0, 5, 0, 0, 5, 1})
	f.Add(baseConfig(), []byte{0, 0, 1, 2, 1, 1, 1, 2, 0})
	f.Fuzz(func(t *testing.T, doc, script []byte) {
		if jsonx.Validate(doc) != nil {
			return
		}
		var top map[string]json.RawMessage
		if json.Unmarshal(doc, &top) != nil || top == nil {
			return
		}
		keys := make([]string, 0, len(top)+2)
		for k := range top {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		keys = append(keys, "new1", "new2")
		edits, touched := fuzzEdits(doc, keys, script)
		src := memSource{files: map[string][]byte{configFile: doc}}
		ops, err := Compose(edits, src.read)
		if err != nil {
			return
		}
		got := doc
		if len(ops) == 1 {
			got = ops[0].Content
		}
		if a, b := stripMembers(t, doc, touched), stripMembers(t, got, touched); !bytes.Equal(a, b) {
			t.Fatalf("bytes outside edited members changed:\n%s\n%s", a, b)
		}
	})
}

// fuzzEdits decodes script into edits on top-level members (and one level
// below them) of doc, three bytes per edit: kind, key index, depth/value.
func fuzzEdits(doc []byte, keys []string, script []byte) ([]Edit, map[string]bool) {
	values := []string{`1`, `{}`, `"s"`, `[true,null]`, `{"n":{}}`}
	rows := []model.RowID{model.RowGroupOrder, model.RowPinnedOrder}
	h := hashOf(doc)
	touched := map[string]bool{}
	var edits []Edit
	for i := 0; i+2 < len(script) && len(edits) < 16; i += 3 {
		k := keys[int(script[i+1])%len(keys)]
		path := jsonx.Path{k}
		if script[i+2]&0x80 != 0 {
			path = append(path, "sub")
		}
		e := Edit{Loc: configLoc, Row: rows[int(script[i]>>1)%len(rows)], Path: path, BaseHash: h, Kind: EditSet,
			Value: json.RawMessage(values[int(script[i+2]&0x7f)%len(values)])}
		if script[i]&1 == 1 {
			e.Kind, e.Value = EditDelete, nil
		}
		touched[k] = true
		edits = append(edits, e)
	}
	return edits, touched
}

// stripMembers deletes the touched top-level members present in doc.
func stripMembers(t *testing.T, doc []byte, touched map[string]bool) []byte {
	t.Helper()
	names := make([]string, 0, len(touched))
	for k := range touched {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		out, err := jsonx.Delete(doc, jsonx.Path{k})
		if errors.Is(err, jsonx.ErrNotFound) {
			continue
		}
		if err != nil {
			t.Fatalf("strip %q: %v", k, err)
		}
		doc = out
	}
	return doc
}
