package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// draft stands in for a value a unit stores as an object (the online
// draft, the scratch post-hashes).
type draft struct {
	Queue string            `json:"queue"`
	Hash  map[string]string `json:"hash"`
	Snap  *model.RowSnapshot
	Note  string `json:"note,omitempty"`
}

func TestObjectRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	in := draft{Queue: "run-1", Hash: map[string]string{"path:scratch-workspaces/a": "h1", "dir:scratch-workspaces/b": "h2"},
		Snap: snapshot(t, model.RowTasks, map[string]string{"k": `{"b":2,"a":1}`})}
	in.Snap.Stale = nil
	ref, err := s.PutObject("scratch-post-hashes", in)
	if err != nil {
		t.Fatal(err)
	}
	if !ref.valid() {
		t.Fatalf("ref %q", ref)
	}
	var out draft
	if err := s.GetObject(ref, "scratch-post-hashes", &out); err != nil {
		t.Fatal(err)
	}
	// Raw is memory only, and the stored bytes are canonical (item values
	// too), so compare the canonical encodings.
	canon := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		c, err := model.Canonical(b)
		if err != nil {
			t.Fatal(err)
		}
		return string(c)
	}
	if canon(out) != canon(in) || out.Snap == nil || out.Snap.Items["k"].Hash != in.Snap.Items["k"].Hash {
		t.Fatalf("round trip:\n got %s\nwant %s", canon(out), canon(in))
	}
	// Content-addressed: the same value gives the same ref, written once.
	again, err := s.PutObject("scratch-post-hashes", in)
	if err != nil || again != ref {
		t.Fatalf("second put %q, %v", again, err)
	}
	// The stored bytes are the canonical envelope, mode 0600.
	p := filepath.Join(s.Dir(), objectsDir, string(ref))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	c, err := model.Canonical(b)
	if err != nil || string(c) != string(b) || model.HashBytes(b) != string(ref) {
		t.Fatalf("stored bytes not canonical or not hashed: %s", b)
	}
	if !strings.HasPrefix(string(b), `{"kind":"scratch-post-hashes","schema":1,"value":`) {
		t.Fatalf("envelope %s", b)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != filePerm {
		t.Fatalf("object mode %v, %v", fi.Mode().Perm(), err)
	}
	// A different kind of the same value is a different object.
	other, err := s.PutObject("online-draft", in)
	if err != nil || other == ref {
		t.Fatalf("other kind %q, %v", other, err)
	}
}

func TestObjectKindMismatchAndErrors(t *testing.T) {
	s, _ := newStore(t)
	ref, err := s.PutObject("online-draft", map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]int
	if err := s.GetObject(ref, "scratch-post-hashes", &v); !errors.Is(err, ErrKindMismatch) {
		t.Fatalf("kind mismatch = %v", err)
	}
	if v != nil {
		t.Fatal("value decoded despite the kind mismatch")
	}
	for _, kind := range []string{"", "Online", "a/b", "-x", strings.Repeat("a", 65)} {
		if _, err := s.PutObject(kind, 1); !errors.Is(err, ErrBadKind) {
			t.Errorf("PutObject(%q) = %v", kind, err)
		}
	}
	if err := s.GetObject("nothex", "online-draft", &v); !errors.Is(err, ErrBadRef) {
		t.Fatalf("bad ref = %v", err)
	}
	if err := s.GetObject(Ref(strings.Repeat("0", 64)), "online-draft", &v); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing = %v", err)
	}
	if _, err := s.PutObject("online-draft", func() {}); err == nil {
		t.Fatal("unencodable value stored")
	}
	// A tampered object fails its hash check.
	p := filepath.Join(s.Dir(), objectsDir, string(ref))
	if err := os.WriteFile(p, []byte(`{"kind":"online-draft","schema":1,"value":{"n":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.GetObject(ref, "online-draft", &v); !errors.Is(err, ErrCorruptObject) {
		t.Fatalf("tampered = %v", err)
	}
	// An object is not a snapshot: reading one as a base fails cleanly.
	ref2, err := s.PutObject("online-draft", map[string]int{"n": 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSnapshot(ref2); !errors.Is(err, ErrUnknownSchema) {
		t.Fatalf("object read as snapshot = %v", err)
	}
}

func TestObjectUnknownSchema(t *testing.T) {
	s, _ := newStore(t)
	b := []byte(`{"kind":"online-draft","schema":2,"value":{}}`)
	ref := Ref(model.HashBytes(b))
	if err := os.MkdirAll(filepath.Join(s.Dir(), objectsDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), objectsDir, string(ref)), b, 0o600); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := s.GetObject(ref, "online-draft", &v); !errors.Is(err, ErrUnknownSchema) {
		t.Fatalf("schema 2 = %v", err)
	}
}

func TestObjectSecretsCheck(t *testing.T) {
	s, _ := newStore(t)
	raw := []byte(`{"name":"srv","token":"` + canary + `"}`)
	hashed := json.RawMessage(`{"name":"srv","token":"sha256:` + model.HashBytes([]byte(canary)) + `"}`)
	it, err := model.NewItem("srv", hashed, raw, model.Stamp{}, true)
	if err != nil {
		t.Fatal(err)
	}
	snap := &model.RowSnapshot{Row: model.RowMCPServers, Schema: SnapshotSchema, Items: map[string]model.Item{"srv": it}}
	// A hashed secret item stores fine, without its raw bytes.
	if _, err := s.PutObject("online-draft", draft{Snap: snap}); err != nil {
		t.Fatal(err)
	}
	for rel := range tree(t, s.Dir()) {
		b, err := os.ReadFile(filepath.Join(s.Dir(), rel))
		if err == nil && strings.Contains(string(b), canary) {
			t.Fatalf("canary found in %s", rel)
		}
	}
	// The raw bytes in an item's Value are refused, as by PutSnapshot,
	// wherever the item sits.
	leaky := it
	leaky.Value = json.RawMessage(`{"wrapped":` + string(raw) + `}`)
	for name, v := range map[string]any{
		"item":          leaky,
		"pointer":       &leaky,
		"in a map":      map[string]model.Item{"x": leaky},
		"in a slice":    []any{1, leaky},
		"in a snapshot": draft{Snap: &model.RowSnapshot{Row: model.RowMCPServers, Schema: 1, Items: map[string]model.Item{"srv": leaky}}},
	} {
		if _, err := s.PutObject("online-draft", v); !errors.Is(err, ErrSecretInValue) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The raw bytes in any other string value of the object are refused.
	if _, err := s.PutObject("online-draft", draft{Snap: snap, Note: "copied " + string(raw)}); !errors.Is(err, ErrSecretInValue) {
		t.Fatalf("raw in a string = %v", err)
	}
	if _, err := s.PutObject("online-draft", map[string]any{"snap": snap, string(raw): 1}); !errors.Is(err, ErrSecretInValue) {
		t.Fatalf("raw in a key = %v", err)
	}
	// A pointer to a struct's first field shares the struct's address; the
	// struct's other fields are still checked.
	type wrapper struct{ First, Leak model.Item }
	type holder struct {
		B *model.Item
		W *wrapper
	}
	w := &wrapper{Leak: leaky}
	if _, err := s.PutObject("online-draft", holder{B: &w.First, W: w}); !errors.Is(err, ErrSecretInValue) {
		t.Fatalf("aliased pointer = %v", err)
	}
	// A value too deep to walk is refused, not stored unchecked.
	var deep any = leaky
	for range maxSecretsDepth + 1 {
		deep = []any{deep}
	}
	if _, err := s.PutObject("online-draft", deep); !errors.Is(err, ErrObjectTooDeep) {
		t.Fatalf("too deep = %v", err)
	}
	for rel := range tree(t, s.Dir()) {
		b, err := os.ReadFile(filepath.Join(s.Dir(), rel))
		if err == nil && strings.Contains(string(b), canary) {
			t.Fatalf("canary found in %s after refused puts", rel)
		}
	}
}

// TestObjectSecretsCheckDeepButFine: a deeply nested value without secrets
// below the limit is stored.
func TestObjectSecretsCheckDeepButFine(t *testing.T) {
	s, _ := newStore(t)
	var v any = "leaf"
	for range 100 {
		v = map[string]any{"x": v}
	}
	if _, err := s.PutObject("online-draft", v); err != nil {
		t.Fatal(err)
	}
}

func TestGCKeepsRunObjects(t *testing.T) {
	s, _ := newStore(t)
	online, err := s.PutObject("online-draft", map[string]string{"q": "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	post, err := s.PutObject("scratch-post-hashes", map[string]string{"path:x": "h"})
	if err != nil {
		t.Fatal(err)
	}
	garbage, err := s.PutObject("online-draft", map[string]string{"q": "old"})
	if err != nil {
		t.Fatal(err)
	}
	r := NewRoot("/data")
	r.Run = &Run{ID: "run-1", Online: online, PostHashesRef: post}
	if err := s.GC(r); err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	if err := s.GetObject(online, "online-draft", &v); err != nil {
		t.Errorf("online draft removed: %v", err)
	}
	if err := s.GetObject(post, "scratch-post-hashes", &v); err != nil {
		t.Errorf("post-hashes removed: %v", err)
	}
	if err := s.GetObject(garbage, "online-draft", &v); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("garbage kept: %v", err)
	}
	// Once the run is closed, the draft is no longer kept.
	r.Run = nil
	if err := s.GC(r); err != nil {
		t.Fatal(err)
	}
	if err := s.GetObject(online, "online-draft", &v); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("draft kept after the run closed: %v", err)
	}
}

func TestV1RootLoadsAndIsWrittenAsV2(t *testing.T) {
	s, _ := newStore(t)
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	v1 := []byte(`{"v": 1, "data_root": "/data", "epoch": 4, "active": "work", "profiles": {"work": {"alias": "work",
		"replica": {"account": "` + fakeUUID(1) + `", "org": "` + fakeUUID(2) + `"}, "folder_id": {}, "marker_base": "m", "live_epoch": 4}},
		"bases": {}, "canonical": {}, "overlays": {}, "last_app_version": "7.1.0"}`)
	if err := os.WriteFile(filepath.Join(s.Dir(), rootFile), v1, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != 2 || RootVersion != 2 || r.Epoch != 4 || r.Active != "work" || r.LastAppVersion != "7.1.0" {
		t.Fatalf("loaded %+v", r)
	}
	if r.Online != nil || r.PinsProbes != nil || !r.LastCommitAt.IsZero() || r.Profiles["work"].SeededFrom != "" {
		t.Fatalf("v0.2 fields not empty: %+v", r)
	}
	if err := s.Commit(r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.Dir(), rootFile))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := versionOf(b); err != nil || v != 2 {
		t.Fatalf("written version %d, %v", v, err)
	}
	// Absent v0.2 fields are not written.
	for _, k := range []string{`"online"`, `"pins_probes"`, `"last_commit_at"`, `"seeded_from"`, `"purpose"`, `"post_hashes_ref"`} {
		if strings.Contains(string(b), k) {
			t.Errorf("empty %s written", k)
		}
	}
	if pre, err := os.ReadFile(filepath.Join(s.Dir(), preMigrationPrefix+"1")); err != nil || string(pre) != string(v1) {
		t.Fatalf("pre-migration copy %q, %v", pre, err)
	}
}

func TestV02FieldsRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	r := sampleRoot()
	at := time.Date(2026, 11, 1, 2, 3, 4, 0, time.UTC)
	r.LastCommitAt = at
	r.PinsProbes = map[ids.Alias]int64{"work": 1700000000000}
	r.Online = &OnlineRecord{Queue: "run-1", Kind: "artifacts", Profile: "home", From: "work", Replica: rep(1, 10), Epoch: 8,
		Created: at, State: "pending", Counts: model.OnlineCounts{PinsAdd: 2, Failed: 1}, StepsSHA: "abc", Confirmed: []string{"p1"}}
	r.Profiles["work"].SeededFrom = SeededLegacy
	r.Generations = []GenerationRef{{N: 3, RunID: "run-0", Target: "work", At: at, Purpose: string(RunQuarantine)}}
	r.Run.Online = Ref(strings.Repeat("1", 64))
	r.Run.PostHashesRef = Ref(strings.Repeat("2", 64))
	r.Run.Kind = RunQuarantine
	r.Run.Intents = []Intent{{Name: IntentQuarantining, At: at, Data: map[string]string{"n": "3", "alias": "work"}}}
	if err := s.Commit(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, r) {
		a, _ := json.Marshal(r)
		b, _ := json.Marshal(got)
		t.Fatalf("round trip differs:\n%s\n%s", a, b)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir(), rootFile))
	for _, k := range []string{`"last_commit_at"`, `"pins_probes"`, `"seeded_from": "legacy"`, `"purpose": "legacy-quarantine"`,
		`"post_hashes_ref"`, `"kind": "artifacts"`, `"pins_add": 2`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("root.json lacks %s", k)
		}
	}
}
