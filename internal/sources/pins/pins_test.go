package pins

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/store/idb"
	"github.com/kazupc/paritydesk/internal/store/idb/idbtest"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var (
	a = idbtest.FakeID(1)
	b = idbtest.FakeID(2)
	c = idbtest.FakeID(3)
)

func pinsSpec(t *testing.T) compat.RowSpec {
	t.Helper()
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "7.x" {
			if r, ok := m.Row(model.RowPins); ok && r.IDB != nil {
				return r
			}
		}
	}
	t.Fatal("no 7.x pins row")
	return compat.RowSpec{}
}

// fake is an idb.Reader returning a fixed value or error.
func fake(text string, err error) idb.Reader {
	return func(context.Context, string, idb.Spec) (idb.Value, error) {
		if err != nil {
			return idb.Value{}, err
		}
		return idb.Value{Text: text}, nil
	}
}

type mirrorSpec struct {
	bucket []string // nil: absent
	ls     []string // nil: absent
	lsTime int64
}

// folder writes the mirror into a new data folder and returns its read
// context.
func folder(t *testing.T, m mirrorSpec) sources.ReadContext {
	t.Helper()
	dir := t.TempDir()
	cfg := map[string]any{}
	if m.bucket != nil {
		cfg["preferences"] = map[string]any{"epitaxyPrefs": map[string]any{"starred-local-code-sessions": m.bucket}}
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, sources.DesktopConfigFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := kv.NewMem()
	if m.ls != nil {
		env, _ := json.Marshal(map[string]any{"value": m.ls, "tabId": "x", "timestamp": m.lsTime})
		key, err := localstorage.Key("LSS-persisted.starred-local-code-sessions")
		if err != nil {
			t.Fatal(err)
		}
		var batch kv.Batch
		batch.Put(key, localstorage.EncodeString(string(env)))
		if err := store.Write(&batch); err != nil {
			t.Fatal(err)
		}
	}
	return sources.ReadContext{Folder: dir, LS: store, Spec: pinsSpec(t)}
}

func keys(s *model.RowSnapshot) string {
	var ks []string
	for k := range s.Items {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

func readWith(t *testing.T, r idb.Reader, rc sources.ReadContext) (*model.RowSnapshot, error) {
	t.Helper()
	return New(Options{Read: r}).Read(context.Background(), rc)
}

func wantUnavailable(t *testing.T, err error, reason idb.Reason) {
	t.Helper()
	if !errors.Is(err, sources.ErrSourceUnavailable) || ReasonOf(err) != reason {
		t.Fatalf("err = %v, want unavailable %s", err, reason)
	}
}

func TestAbsentRules(t *testing.T) {
	absent := fake("", idb.ErrKeyAbsent)
	invalid := &sources.SideFacts{}
	validEmpty := &sources.SideFacts{BaseValid: true, BaseEmpty: true}
	validFull := &sources.SideFacts{BaseValid: true}
	starred := &sources.SideFacts{StarredIDs: map[string]bool{a: true}}
	for _, tc := range []struct {
		name   string
		side   *sources.SideFacts
		mirror mirrorSpec
		want   string
		reason idb.Reason
		src    string
	}{
		{"invalid base, no isStarred, no mirror", invalid, mirrorSpec{}, "", "", SourceIDB},
		{"valid empty base", validEmpty, mirrorSpec{}, "", "", SourceIDB},
		{"valid non-empty base even with a mirror", validFull, mirrorSpec{bucket: []string{a}}, "", idb.ReasonKeyAbsent, ""},
		{"isStarred and no mirror", starred, mirrorSpec{}, "", idb.ReasonKeyAbsent, ""},
		{"invalid base and a mirror", invalid, mirrorSpec{ls: []string{a, b}, lsTime: 5}, a + "," + b, "", SourceMirror},
		{"no side facts", nil, mirrorSpec{}, "", idb.ReasonKeyAbsent, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := folder(t, tc.mirror)
			rc.Side = tc.side
			rc.Report = &sources.ReadReport{}
			s, err := readWith(t, absent, rc)
			if tc.reason != "" {
				wantUnavailable(t, err, tc.reason)
				return
			}
			if err != nil || keys(s) != tc.want || rc.Report.PinsSource != tc.src {
				t.Fatalf("snap %v err %v report %+v", s, err, rc.Report)
			}
		})
	}
	rc := folder(t, mirrorSpec{})
	rc.Side = invalid
	if _, err := readWith(t, fake("", idb.ErrDirAbsent), rc); err != nil {
		t.Fatalf("dir absent: %v", err)
	}
}

func TestUnknownFormatAndTransient(t *testing.T) {
	for _, e := range []error{idb.ErrBlob, idb.ErrVersion, idb.ErrBackend, idb.ErrUnsupported, idb.ErrTooLarge} {
		rc := folder(t, mirrorSpec{bucket: []string{c}})
		rc.Report = &sources.ReadReport{}
		s, err := readWith(t, fake("", e), rc)
		if err != nil || keys(s) != c || rc.Report.PinsSource != SourceMirror {
			t.Fatalf("%v with mirror: %v %v", e, s, err)
		}
		_, err = readWith(t, fake("", e), folder(t, mirrorSpec{}))
		wantUnavailable(t, err, idb.ReasonOf(e))
	}
	for _, e := range []error{idb.ErrTorn, idb.ErrPending, idb.ErrUnclean, idb.ErrAmbiguous, errors.New("io")} {
		_, err := readWith(t, fake("", e), folder(t, mirrorSpec{bucket: []string{c}}))
		wantUnavailable(t, err, idb.ReasonOf(e))
	}
	if _, err := New(Options{}).Read(context.Background(), sources.ReadContext{}); ReasonOf(err) != idb.ReasonFormat {
		t.Fatalf("no spec: %v", err)
	}
}

func TestMirrorPrecedence(t *testing.T) {
	rc := folder(t, mirrorSpec{bucket: []string{a}, ls: []string{b}, lsTime: 7})
	m, ok, err := ReadMirror(rc, rc.Spec.IDB)
	if err != nil || !ok || m.From != mirrorBucket || strings.Join(m.IDs, ",") != a || strings.Join(m.LSIDs, ",") != b || m.LSTime != 7 {
		t.Fatalf("mirror = %+v %v %v", m, ok, err)
	}
	rc = folder(t, mirrorSpec{ls: []string{b, "cloud_x", b}})
	m, ok, _ = ReadMirror(rc, rc.Spec.IDB)
	if !ok || m.From != mirrorLS || strings.Join(m.IDs, ",") != b {
		t.Fatalf("ls mirror = %+v", m)
	}
	if _, ok, _ := ReadMirror(folder(t, mirrorSpec{}), rc.Spec.IDB); ok {
		t.Fatal("empty mirror is ok")
	}
	rc.LS = nil
	if m, ok, _ := ReadMirror(rc, rc.Spec.IDB); ok {
		t.Fatalf("nil LS: %+v", m)
	}
}

func pinDoc(version any, ids any, updated any) string {
	d := map[string]any{"state": map[string]any{"starredIds": ids}, "version": version}
	if updated != nil {
		d["updatedAt"] = updated
	}
	out, _ := json.Marshal(d)
	return string(out)
}

func TestReadable(t *testing.T) {
	starred := &sources.SideFacts{StarredIDs: map[string]bool{a: true}}
	for _, tc := range []struct {
		name   string
		text   string
		want   string
		reason idb.Reason
	}{
		{"empty list even with isStarred", pinDoc(0, []string{}, 1), "", ""},
		{"version 1", pinDoc(1, []string{a}, 1), "", idb.ReasonVersion},
		{"ids missing", `{"state":{},"version":0}`, "", idb.ReasonFormat},
		{"ids not an array", pinDoc(0, "x", nil), "", idb.ReasonFormat},
		{"not an object", `[1]`, "", idb.ReasonFormat},
		{"bad json", `{"state":`, "", idb.ReasonFormat},
		{"trailing value", pinDoc(0, []string{a}, nil) + "{}", "", idb.ReasonFormat},
		{"non-local and non-string ids ignored", pinDoc(0, []any{a, "cloud_1", 7, nil, b, a}, nil), a + "," + b, ""},
		{"too deep", strings.Repeat("[", 20) + strings.Repeat("]", 20), "", idb.ReasonFormat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := folder(t, mirrorSpec{})
			rc.Side = starred
			s, err := readWith(t, fake(tc.text, nil), rc)
			if tc.reason != "" {
				wantUnavailable(t, err, tc.reason)
				return
			}
			if err != nil || keys(s) != tc.want {
				t.Fatalf("snap %v err %v", s, err)
			}
		})
	}
	spec := pinsSpec(t)
	ids := make([]string, spec.IDB.MaxItems+1)
	for i := range ids {
		ids[i] = idbtest.FakeID(i + 1)
	}
	_, err := readWith(t, fake(pinDoc(0, ids, nil), nil), folder(t, mirrorSpec{}))
	wantUnavailable(t, err, idb.ReasonTooLarge)
	// Two values that differ only in updatedAt give equal snapshots.
	s1, _ := readWith(t, fake(pinDoc(0, []string{a, b}, 1), nil), folder(t, mirrorSpec{}))
	s2, _ := readWith(t, fake(pinDoc(0, []string{b, a}, 2), nil), folder(t, mirrorSpec{}))
	if keys(s1) != keys(s2) || s1.Items[a].Hash != s2.Items[a].Hash {
		t.Fatal("updatedAt changed the snapshot")
	}
	// The mirror holds another set.
	rc := folder(t, mirrorSpec{bucket: []string{a}})
	rc.Report = &sources.ReadReport{}
	if _, err := readWith(t, fake(pinDoc(0, []string{a, b}, 1), nil), rc); err != nil || !rc.Report.MirrorDiffers || rc.Report.PinsSource != SourceIDB {
		t.Fatalf("mirror differs: %v %+v", err, rc.Report)
	}
}

func TestProbe(t *testing.T) {
	since := int64(1000)
	side := func() *sources.SideFacts { return &sources.SideFacts{BaseValid: true, ProbeSince: since} }
	for _, tc := range []struct {
		name    string
		text    string
		mirror  mirrorSpec
		want    string
		reason  idb.Reason
		src     string
		readErr error
	}{
		{"fresh store", pinDoc(0, []string{a}, since), mirrorSpec{}, a, "", SourceIDB, nil},
		{"older store, fresh ls mirror", pinDoc(0, []string{a}, since-1), mirrorSpec{ls: []string{b}, lsTime: since}, b, "", SourceMirror, nil},
		{"older store, stale mirror", pinDoc(0, []string{a}, since-1), mirrorSpec{ls: []string{b}, lsTime: since - 1}, "", idb.ReasonStaleStore, "", nil},
		{"older store, bucket differs from ls", pinDoc(0, []string{a}, since-1), mirrorSpec{bucket: []string{c}, ls: []string{b}, lsTime: since}, "", idb.ReasonStaleStore, "", nil},
		{"older store, bucket equals ls", pinDoc(0, []string{a}, since-1), mirrorSpec{bucket: []string{b}, ls: []string{b}, lsTime: since}, b, "", SourceMirror, nil},
		{"missing updatedAt, no mirror", pinDoc(0, []string{a}, nil), mirrorSpec{}, "", idb.ReasonStaleStore, "", nil},
		{"missing updatedAt, fresh mirror", pinDoc(0, []string{a}, nil), mirrorSpec{ls: []string{c}, lsTime: since + 5}, c, "", SourceMirror, nil},
		{"mirror source, fresh", "", mirrorSpec{ls: []string{c}, lsTime: since}, c, "", SourceMirror, idb.ErrBlob},
		{"mirror source, stale", "", mirrorSpec{ls: []string{c}, lsTime: since - 1}, "", idb.ReasonStaleStore, "", idb.ErrBlob},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := folder(t, tc.mirror)
			rc.Side, rc.Report = side(), &sources.ReadReport{}
			s, err := readWith(t, fake(tc.text, tc.readErr), rc)
			if tc.reason != "" {
				wantUnavailable(t, err, tc.reason)
				return
			}
			if err != nil || keys(s) != tc.want || rc.Report.PinsSource != tc.src || !rc.Report.ProbePassed {
				t.Fatalf("snap %v err %v report %+v", s, err, rc.Report)
			}
		})
	}
	// Absence never consults the probe.
	rc := folder(t, mirrorSpec{})
	rc.Side = &sources.SideFacts{ProbeSince: since}
	if s, err := readWith(t, fake("", idb.ErrKeyAbsent), rc); err != nil || len(s.Items) != 0 {
		t.Fatalf("absent with probe: %v %v", s, err)
	}
}

func TestOpsNeverEdits(t *testing.T) {
	cur, _ := snapshot([]string{a})
	des, _ := snapshot([]string{b})
	res, err := New(Options{}).Ops(context.Background(), sources.OpsContext{}, cur, des)
	if err != nil || len(res.Edits) != 0 {
		t.Fatalf("ops = %+v %v", res, err)
	}
}

// TestRealReaderShape reads a fixture built with idbtest through the real
// in-place reader.
func TestRealReaderShape(t *testing.T) {
	dir := t.TempDir()
	if _, err := idbtest.Write(filepath.Join(dir, idbtest.Dir), idbtest.PinDB(idbtest.PinValue([]string{a, b}))); err != nil {
		t.Fatal(err)
	}
	rc := sources.ReadContext{Folder: dir, Spec: pinsSpec(t), Report: &sources.ReadReport{}}
	s, err := New(Options{}).Read(context.Background(), rc)
	if err != nil || keys(s) != a+","+b || s.Items[a].Value == nil || string(s.Items[a].Value) != "true" {
		t.Fatalf("snap %v err %v", s, err)
	}
}

// TestValueShapeGolden pins the observed value shape (record version,
// Blink 21 with its zero trailer, V8 16, a one-byte string, the zustand
// envelope) with fake ids and the fixed updatedAt.
func TestValueShapeGolden(t *testing.T) {
	got := hex.EncodeToString(idbtest.PinValue([]string{a, b})) + "\n"
	path := filepath.Join("testdata", "value-shape.golden.hex")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Fatalf("golden differs; run with -update")
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(want)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hex.EncodeToString(raw), "01ff15fe000000000000000000000000ff1022") {
		t.Fatalf("shape prefix %x", raw[:20])
	}
	spec := pinsSpec(t)
	read := func(context.Context, string, idb.Spec) (idb.Value, error) {
		dir := t.TempDir()
		if _, err := idbtest.Write(filepath.Join(dir, idbtest.Dir), idbtest.PinDB(raw)); err != nil {
			return idb.Value{}, err
		}
		return idb.ReadString(context.Background(), dir, spec.IDB.Reader())
	}
	s, err := readWith(t, read, sources.ReadContext{Spec: spec})
	if err != nil || keys(s) != a+","+b {
		t.Fatalf("golden does not parse: %v %v", s, err)
	}
}

func FuzzPinsJSON(f *testing.F) {
	f.Add(pinDoc(0, []string{a}, 1))
	f.Add(`{"state":{"starredIds":[1,"x"]},"version":0}`)
	spec := compat.RowSpec{IDB: &compat.IDBSpec{IDsPath: []string{"state", "starredIds"}, VersionPath: []string{"version"},
		UpdatedPath: []string{"updatedAt"}, Versions: []int{0}, MaxItems: 100,
		IDPattern: `^local_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`}}
	f.Fuzz(func(t *testing.T, text string) {
		s, err := New(Options{Read: fake(text, nil)}).Read(context.Background(), sources.ReadContext{Spec: spec})
		if err != nil {
			return
		}
		for k := range s.Items {
			if !strings.HasPrefix(k, "local_") || len(k) != len(a) {
				t.Fatalf("item %q from non-matching text", k)
			}
		}
	})
}

func FuzzMirror(f *testing.F) {
	f.Add(`["` + a + `"]`)
	f.Add(`{"x":1}`)
	f.Fuzz(func(t *testing.T, raw string) {
		ids, ok := idList(json.RawMessage(raw), regexpOf(t))
		if !ok {
			return
		}
		for _, id := range ids {
			if !strings.HasPrefix(id, "local_") {
				t.Fatalf("id %q", id)
			}
		}
	})
}

func regexpOf(t *testing.T) *regexp.Regexp {
	t.Helper()
	return regexp.MustCompile(`^local_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
}
