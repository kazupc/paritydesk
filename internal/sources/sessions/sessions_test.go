package sessions_test

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/sessions"
	"github.com/kazupc/paritydesk/internal/testkit"
)

var rep = testkit.Replica(1)

func rc(t *testing.T, m *compat.Manifest, folder string, row model.RowID) sources.ReadContext {
	t.Helper()
	return sources.ReadContext{Folder: folder, Replica: rep, LS: testkit.LS(t, folder), Spec: testkit.Spec(t, m, row, rep), Manifest: m}
}

func read(t *testing.T, a sources.Adapter, c sources.ReadContext) *model.RowSnapshot {
	t.Helper()
	s, err := a.Read(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ops(t *testing.T, a sources.Adapter, c sources.ReadContext, src *sources.ReadContext, cur, des *model.RowSnapshot) sources.OpsResult {
	t.Helper()
	res, err := a.Ops(context.Background(), sources.OpsContext{ReadContext: c, NowMillis: 1, Source: src}, cur, des)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func sessDir(folder string) string { return sources.SessionsDir(folder, rep) }

func TestReadRecords(t *testing.T) {
	m := testkit.Manifest(t)
	spec := testkit.Rich("Claude", 1)
	spec.Replica = rep
	folder := testkit.NewDataRoot(t).AddFolder(t, spec)
	s := read(t, sessions.Sessions{}, rc(t, m, folder, model.RowSessions))
	want := []string{testkit.FakeSession(1), testkit.FakeSession(2), testkit.FakeSession(3), testkit.FakeSession(11)}
	if strings.Join(s.Keys(), ",") != strings.Join(want, ",") {
		t.Fatalf("keys %v", s.Keys())
	}
	it := s.Items[testkit.FakeSession(1)]
	if it.Stamp.AppTime != 1001 || !strings.Contains(string(it.Value), "futureField") {
		t.Errorf("record 1: stamp %d, unknown fields kept: %v", it.Stamp.AppTime, strings.Contains(string(it.Value), "futureField"))
	}
	ts, ok := s.Tombstones[testkit.FakeSession(21)]
	if !ok || ts.At != 5001 {
		t.Errorf("tombstone %+v", s.Tombstones)
	}
}

func TestTombstoneTimeFallsBackToMTime(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep})
	p := filepath.Join(sessDir(folder), "deleted_"+testkit.FakeUUID(5))
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	s := read(t, sessions.Sessions{}, rc(t, m, folder, model.RowSessions))
	if got := s.Tombstones[testkit.FakeSession(5)].At; got != fi.ModTime().UnixMilli() {
		t.Fatalf("tombstone time %d", got)
	}
}

func TestTmpCandidate(t *testing.T) {
	m := testkit.Manifest(t)
	tests := []struct {
		name     string
		tmp      map[string]any
		wantFrom string
	}{
		{"newer tmp wins", map[string]any{"title": "from tmp", "lastActivityAt": 9999}, "from tmp"},
		{"older tmp loses", map[string]any{"title": "from tmp", "lastActivityAt": 1}, "record"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
				Sessions:   []testkit.Session{{N: 1, Title: "record", LastActivityAt: 100}},
				TmpRecords: map[int]map[string]any{1: tc.tmp}})
			s := read(t, sessions.Sessions{}, rc(t, m, folder, model.RowSessions))
			if !strings.Contains(string(s.Items[testkit.FakeSession(1)].Value), tc.wantFrom) {
				t.Fatalf("value %s", s.Items[testkit.FakeSession(1)].Value)
			}
		})
	}
}

func TestMalformedRecordNamesFileOnly(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep})
	name := testkit.FakeSession(4) + ".json"
	if err := os.WriteFile(filepath.Join(sessDir(folder), name), []byte(`{"title":"`+testkit.Canary+`",`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := sessions.Sessions{}.Read(context.Background(), rc(t, m, folder, model.RowSessions))
	if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), testkit.Canary) {
		t.Fatalf("error %v", err)
	}
}

// TestTombstoneRemovesSiblings applies a tombstone to a session that has
// a record, an app tmp candidate and another sibling; all three are
// quarantined, the tmp one with the app-tmp reason, and the idx drops the
// session.
func TestTombstoneRemovesSiblings(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
		Sessions:   []testkit.Session{{N: 1, Archived: true}, {N: 2}},
		TmpRecords: map[int]map[string]any{1: {"title": "tmp", "lastActivityAt": 0}}})
	if err := os.WriteFile(filepath.Join(sessDir(dst), testkit.FakeSession(1)+".bak"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-s", Replica: rep,
		Sessions: []testkit.Session{{N: 2}}, Tombstones: map[int]int64{1: 777}})
	c := rc(t, m, dst, model.RowSessions)
	srcRC := rc(t, m, src, model.RowSessions)
	desired := read(t, sessions.Sessions{}, srcRC)
	res := ops(t, sessions.Sessions{}, c, &srcRC, read(t, sessions.Sessions{}, c), desired)

	removed := map[string]string{}
	for _, e := range res.Edits {
		if e.Kind == plan.EditRemove {
			if !e.Destructive || e.Item != testkit.FakeSession(1) {
				t.Errorf("remove %s: destructive %v item %s", e.Loc.File, e.Destructive, e.Item)
			}
			removed[path.Base(e.Loc.File)] = e.Reason
		}
	}
	base := testkit.FakeSession(1)
	want := map[string]string{base + ".json": "", base + ".json.tmp": plan.ReasonAppTmpCandidate, base + ".bak": ""}
	if len(removed) != len(want) {
		t.Fatalf("removed %v", removed)
	}
	for n, r := range want {
		if got, ok := removed[n]; !ok || got != r {
			t.Errorf("%s: removed %v reason %q", n, ok, got)
		}
	}
	testkit.Apply(t, dst, res.Edits)
	idx, _ := os.ReadFile(filepath.Join(sessDir(dst), sources.ArchiveIndex))
	if string(idx) != `{"v":1,"archived":[]}` {
		t.Errorf("idx %s", idx)
	}
	tomb, _ := os.ReadFile(filepath.Join(sessDir(dst), "deleted_"+testkit.FakeUUID(1)))
	if !strings.Contains(string(tomb), "777") {
		t.Errorf("tombstone not copied from source: %s", tomb)
	}
	after := read(t, sessions.Sessions{}, rc(t, m, dst, model.RowSessions))
	if _, ok := after.Items[base]; ok || after.Tombstones[base].At != 777 {
		t.Errorf("after apply: items %v tombstones %v", after.Keys(), after.Tombstones)
	}
}

func TestIndexRegenerated(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
		Sessions: []testkit.Session{{N: 3}, {N: 1}, {N: 2, Archived: true}}})
	c := rc(t, m, folder, model.RowSessions)
	cur := read(t, sessions.Sessions{}, c)
	des := cur.Clone()
	for _, n := range []int{1, 3} {
		k := testkit.FakeSession(n)
		var obj map[string]any
		_ = json.Unmarshal(des.Items[k].Value, &obj)
		obj["isArchived"] = true
		b, _ := json.Marshal(obj)
		it, err := model.NewItem(k, b, b, model.Stamp{}, false)
		if err != nil {
			t.Fatal(err)
		}
		des.Items[k] = it
	}
	res := ops(t, sessions.Sessions{}, c, nil, cur, des)
	var idx *plan.Edit
	for i, e := range res.Edits {
		if strings.HasSuffix(e.Loc.File, sources.ArchiveIndex) {
			idx = &res.Edits[i]
		}
		if e.Kind != plan.EditRemove && strings.HasSuffix(e.Loc.File, ".tmp") {
			t.Errorf("edit writes a tmp name: %s", e.Loc.File)
		}
	}
	want := `{"v":1,"archived":["` + testkit.FakeSession(1) + `","` + testkit.FakeSession(2) + `","` + testkit.FakeSession(3) + `"]}`
	if idx == nil || idx.Kind != plan.EditReplace || string(idx.Value) != want {
		t.Fatalf("idx edit %+v", idx)
	}
}

func TestIndexWithUnknownVersionRefused(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep, Sessions: []testkit.Session{{N: 1}}})
	if err := os.WriteFile(filepath.Join(sessDir(folder), sources.ArchiveIndex), []byte(`{"v":2,"archived":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := rc(t, m, folder, model.RowSessions)
	cur := read(t, sessions.Sessions{}, c)
	des := cur.Clone()
	delete(des.Items, testkit.FakeSession(1))
	if _, err := (sessions.Sessions{}).Ops(context.Background(), sources.OpsContext{ReadContext: c}, cur, des); err == nil {
		t.Fatal("idx v=2 rewritten")
	}
}

func TestRevivedSessionLosesTombstone(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
		Tombstones: map[int]int64{4: 10}})
	c := rc(t, m, folder, model.RowSessions)
	cur := read(t, sessions.Sessions{}, c)
	des := cur.Clone()
	des.Tombstones = nil
	b := []byte(`{"title":"back","lastActivityAt":20}`)
	it, _ := model.NewItem(testkit.FakeSession(4), b, b, model.Stamp{}, false)
	des.Items[it.Key] = it
	res := ops(t, sessions.Sessions{}, c, nil, cur, des)
	removes := 0
	for _, e := range res.Edits {
		if e.Kind == plan.EditRemove {
			removes++
			if e.Reason != plan.ReasonRevivedTombstone {
				t.Fatalf("tombstone removal reason %q", e.Reason)
			}
		}
	}
	if removes != 1 {
		t.Fatalf("tombstone removals: %d", removes)
	}
	testkit.Apply(t, folder, res.Edits)
	after := read(t, sessions.Sessions{}, rc(t, m, folder, model.RowSessions))
	if len(after.Tombstones) != 0 || after.Items[it.Key].Hash != it.Hash {
		t.Fatalf("after revival: %v %v", after.Tombstones, after.Keys())
	}
}

func TestWritingRecordQuarantinesTmp(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
		Sessions:   []testkit.Session{{N: 1, LastActivityAt: 5}},
		TmpRecords: map[int]map[string]any{1: {"lastActivityAt": 1}}})
	c := rc(t, m, folder, model.RowSessions)
	cur := read(t, sessions.Sessions{}, c)
	des := cur.Clone()
	b := []byte(`{"title":"new","lastActivityAt":50}`)
	it, _ := model.NewItem(testkit.FakeSession(1), b, b, model.Stamp{}, false)
	des.Items[it.Key] = it
	res := ops(t, sessions.Sessions{}, c, nil, cur, des)
	var kinds []string
	for _, e := range res.Edits {
		kinds = append(kinds, string(e.Kind)+":"+path.Base(e.Loc.File)+":"+e.Reason)
	}
	sort.Strings(kinds)
	want := []string{"remove:" + testkit.FakeSession(1) + ".json.tmp:" + plan.ReasonAppTmpCandidate, "replace:" + testkit.FakeSession(1) + ".json:"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("edits %v", kinds)
	}
}

func TestListSessionsBusyFromBusyFields(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep, Sessions: []testkit.Session{
		{N: 1, Title: "idle", CLISessionID: "cli-1", Worktree: "/tmp/wt/a", Archived: true,
			Extra: map[string]any{"interruptedByQuitAt": 123, "runStatus": "idle"}},
		{N: 2, Extra: map[string]any{"runStatus": "running"}},
		{N: 3, Extra: map[string]any{"queuedMessages": []string{"next"}}},
		{N: 4, Extra: map[string]any{"queuedMessages": []string{}}},
	}})
	spec := testkit.Spec(t, m, model.RowSessions, rep)
	spec.Fields = map[string][]string{sources.RoleBusy: {"interruptedByQuitAt", "runStatus", "queuedMessages"}}
	infos, err := sources.ListSessions(context.Background(), folder, rep, spec)
	if err != nil {
		t.Fatal(err)
	}
	busy := map[string]bool{}
	for _, in := range infos {
		busy[string(in.ID)] = in.Busy
	}
	want := map[string]bool{testkit.FakeSession(1): false, testkit.FakeSession(2): true, testkit.FakeSession(3): true, testkit.FakeSession(4): false}
	for k, v := range want {
		if busy[k] != v {
			t.Errorf("%s busy %v, want %v", k, busy[k], v)
		}
	}
	first := infos[0]
	if first.Title != "idle" || first.CLISessionID != "cli-1" || first.Worktree != "/tmp/wt/a" || !first.Archived {
		t.Errorf("info %+v", first)
	}
	id, err := sources.ResolveByCLISession(context.Background(), folder, rep, "cli-1")
	if err != nil || string(id) != testkit.FakeSession(1) {
		t.Errorf("resolve: %s %v", id, err)
	}
	if _, err := sources.ResolveByCLISession(context.Background(), folder, rep, "nope"); err == nil {
		t.Error("unknown cli session resolved")
	}
}

func TestTasks(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
		Backlog:        []map[string]any{{"id": "b", "x": 1}, {"id": "a", "lastRunAt": 7}},
		ScheduledTasks: []map[string]any{{"id": 5, "cron": "x"}}})
	c := rc(t, m, folder, model.RowTasks)
	c.Spec.Fields = map[string][]string{sources.RoleMaxTime: {"lastRunAt"}}
	s := read(t, sessions.Tasks{}, c)
	if strings.Join(s.Order, ",") != "backlog/b,backlog/a,scheduled/5" || s.Items["backlog/a"].Stamp.AppTime != 7 {
		t.Fatalf("order %v stamp %d", s.Order, s.Items["backlog/a"].Stamp.AppTime)
	}
	des := s.Clone()
	delete(des.Items, "backlog/b")
	des.Order = []string{"backlog/a", "scheduled/5"}
	res := ops(t, sessions.Tasks{}, c, nil, s, des)
	if len(res.Edits) != 1 || strings.Join(res.Edits[0].Path, "/") != "items" {
		t.Fatalf("edits %+v", res.Edits)
	}
	before, _ := os.ReadFile(filepath.Join(sessDir(folder), "backlog", "tasks.json"))
	testkit.Apply(t, folder, res.Edits)
	after, _ := os.ReadFile(filepath.Join(sessDir(folder), "backlog", "tasks.json"))
	if !strings.Contains(string(after), `"version": 1`) || string(before) == string(after) {
		t.Errorf("other members not preserved: %s", after)
	}
	if got := read(t, sessions.Tasks{}, c); !sources.SameItems(got, des, true) {
		t.Errorf("read after apply: %v", got.Order)
	}
}

func TestWaitingInputSecretlessCopyAndSkip(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	src := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-s", Replica: rep, WaitingInput: map[string][]byte{"a.json": []byte("1"), "img/b.png": []byte("2")}})
	dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep})
	srcRC := rc(t, m, src, model.RowWaitingInput)
	des := read(t, sessions.WaitingInput{}, srcRC)
	if v := string(des.Items["img/b.png"].Value); !strings.Contains(v, `"kind":"img"`) {
		t.Fatalf("value %s", v)
	}
	if err := os.WriteFile(filepath.Join(sessDir(src), "waiting-input", "a.json"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := rc(t, m, dst, model.RowWaitingInput)
	res := ops(t, sessions.WaitingInput{}, c, &srcRC, read(t, sessions.WaitingInput{}, c), des)
	if len(res.Edits) != 1 || len(res.Skipped) != 1 || res.Skipped[0].Key != "a.json" {
		t.Fatalf("edits %d skipped %+v", len(res.Edits), res.Skipped)
	}
}

// TestTombstoneContentForms reads each tombstone form: a bare number (the
// form app 6.28899.2 writes) is the time even when the mtime differs; the
// older object form is still read; anything else falls back to the mtime.
func TestTombstoneContentForms(t *testing.T) {
	m := testkit.Manifest(t)
	mtime := time.UnixMilli(1600000000000)
	tests := []struct {
		name, content string
		want          int64
	}{
		{"bare number", "1700000000123", 1700000000123},
		{"bare number with newline", "1700000000123\n", 1700000000123},
		{"legacy object", `{"deletedAt":1700000000456}`, 1700000000456},
		{"zero", "0", mtime.UnixMilli()},
		{"string", `"1700000000123"`, mtime.UnixMilli()},
		{"malformed", "17000x", mtime.UnixMilli()},
		{"empty", "", mtime.UnixMilli()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep})
			p := filepath.Join(sessDir(folder), "deleted_"+testkit.FakeUUID(5))
			if err := os.WriteFile(p, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(p, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			s := read(t, sessions.Sessions{}, rc(t, m, folder, model.RowSessions))
			if got := s.Tombstones[testkit.FakeSession(5)].At; got != tc.want {
				t.Fatalf("tombstone time %d, want %d", got, tc.want)
			}
		})
	}
}

// TestTombstoneWrittenAsBareNumber: a new tombstone is written as a bare
// JSON number of epoch ms; the source's bytes are copied only when they
// already have that form.
func TestTombstoneWrittenAsBareNumber(t *testing.T) {
	m := testkit.Manifest(t)
	for _, tc := range []struct {
		name, source string // "" means the source has no such file
		want         string
	}{
		{"no source file", "", "777"},
		{"source bare number copied", "777", "777"},
		{"source object form rewritten", `{"deletedAt":777}`, "777"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testkit.NewDataRoot(t)
			dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep, Sessions: []testkit.Session{{N: 1}}})
			src := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-s", Replica: rep})
			if tc.source != "" {
				if err := os.WriteFile(filepath.Join(sessDir(src), "deleted_"+testkit.FakeUUID(1)), []byte(tc.source), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			c := rc(t, m, dst, model.RowSessions)
			srcRC := rc(t, m, src, model.RowSessions)
			cur := read(t, sessions.Sessions{}, c)
			des := cur.Clone()
			delete(des.Items, testkit.FakeSession(1))
			des.Tombstones = map[string]model.Tombstone{testkit.FakeSession(1): {Key: testkit.FakeSession(1), At: 777}}
			res := ops(t, sessions.Sessions{}, c, &srcRC, cur, des)
			testkit.Apply(t, dst, res.Edits)
			b, err := os.ReadFile(filepath.Join(sessDir(dst), "deleted_"+testkit.FakeUUID(1)))
			if err != nil || string(b) != tc.want {
				t.Fatalf("tombstone content %q, want %q (%v)", b, tc.want, err)
			}
			if got := read(t, sessions.Sessions{}, c).Tombstones[testkit.FakeSession(1)].At; got != 777 {
				t.Fatalf("read back %d", got)
			}
		})
	}
}

func TestWaitingInputMustBeADirectory(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep})
	if s := read(t, sessions.WaitingInput{}, rc(t, m, folder, model.RowWaitingInput)); len(s.Items) != 0 {
		t.Fatalf("empty directory gave %v", s.Keys())
	}
	dir := filepath.Join(sessDir(folder), "waiting-input")
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if s := read(t, sessions.WaitingInput{}, rc(t, m, folder, model.RowWaitingInput)); len(s.Items) != 0 {
		t.Fatalf("missing directory gave %v", s.Keys())
	}
	if err := os.WriteFile(dir, []byte(testkit.Canary), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := sessions.WaitingInput{}.Read(context.Background(), rc(t, m, folder, model.RowWaitingInput))
	if err == nil || !strings.Contains(err.Error(), "not a directory") || strings.Contains(err.Error(), testkit.Canary) ||
		strings.Contains(err.Error(), folder) {
		t.Fatalf("err %v", err)
	}
}

// TestTasksRealShapes reads the observed backlog and scheduled-task files
// with the manifest's own field lists and creates missing files in the
// same shape.
func TestTasksRealShapes(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	spec := testkit.Rich("Claude-s", 1)
	spec.Replica = rep
	src := root.AddFolder(t, spec)
	dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep})
	srcRC := rc(t, m, src, model.RowTasks)
	s := read(t, sessions.Tasks{}, srcRC)
	if it := s.Items["backlog/t1"]; it.Stamp.AppTime != 11 {
		t.Fatalf("backlog stamp %d, want filedAt 11", it.Stamp.AppTime)
	}
	c := rc(t, m, dst, model.RowTasks)
	res := ops(t, sessions.Tasks{}, c, &srcRC, read(t, sessions.Tasks{}, c), s)
	testkit.Apply(t, dst, res.Edits)
	b, err := os.ReadFile(filepath.Join(sessDir(dst), "backlog", "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var backlog struct {
		Version *int              `json:"version"`
		Items   []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(b, &backlog) != nil || backlog.Version == nil || *backlog.Version != 1 || len(backlog.Items) != 2 {
		t.Fatalf("created backlog %s", b)
	}
	sb, _ := os.ReadFile(filepath.Join(sessDir(dst), "scheduled-tasks.json"))
	if !strings.Contains(string(sb), `"recordedSkips":{}`) {
		t.Fatalf("created scheduled tasks %s", sb)
	}
	for _, p := range m.Probes {
		if p.Row != model.RowTasks {
			continue
		}
		res, err := compat.RunProbes(context.Background(), &compat.Manifest{Probes: []compat.Probe{p}}, compat.ProbeEnv{Folder: dst, Replica: rep})
		if err != nil || !res[0].OK {
			t.Errorf("probe %s on a created file: %+v %v", p.ID, res, err)
		}
	}
	if got := read(t, sessions.Tasks{}, c); !sources.SameItems(got, s, true) {
		t.Fatalf("read back %v", got.Order)
	}
}

// TestOtherAccountsNeverRead: a folder that also holds other accounts'
// session directories (with an id that collides with one of the
// replica's) gives the replica's records only, and edits never reach the
// other directories.
func TestOtherAccountsNeverRead(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	spec := testkit.Rich("Claude", 1)
	spec.Replica = rep
	folder := root.AddFolder(t, spec)
	decoy := testkit.DecoyReplica(1)
	decoyDir := filepath.Join(folder, sources.SessionRel(decoy, ""))
	before := testkit.TreeOf(t, decoyDir)
	if len(before) < 3 {
		t.Fatalf("decoy fixture %v", before)
	}
	c := rc(t, m, folder, model.RowSessions)
	s := read(t, sessions.Sessions{}, c)
	if _, ok := s.Items[testkit.FakeSession(71)]; ok {
		t.Fatal("a decoy record was read")
	}
	if _, ok := s.Tombstones[testkit.FakeSession(2)]; ok {
		t.Fatal("a decoy tombstone was read")
	}
	if strings.Contains(string(s.Items[testkit.FakeSession(1)].Value), "decoy") {
		t.Fatal("the decoy copy of a shared id was read")
	}
	infos, err := sources.ListSessions(context.Background(), folder, rep, c.Spec)
	if err != nil || len(infos) != len(s.Items) {
		t.Fatalf("list sessions %d, %v", len(infos), err)
	}
	des := s.Clone()
	for k := range des.Items {
		delete(des.Items, k)
		break
	}
	des.Tombstones = nil
	res := ops(t, sessions.Sessions{}, c, nil, s, des)
	for _, e := range res.Edits {
		if !strings.HasPrefix(e.Loc.File, sources.SessionRel(rep, "")) {
			t.Fatalf("edit outside the replica's directory: %s", e.Loc.File)
		}
	}
	testkit.Apply(t, folder, res.Edits)
	if after := testkit.TreeOf(t, decoyDir); len(after) != len(before) {
		t.Fatal("decoy directory changed")
	} else {
		for k, v := range before {
			if after[k] != v {
				t.Fatalf("decoy file %s changed", k)
			}
		}
	}
}

// A record read again in the same process is reused only while its file
// is unchanged; an edit (new bytes, new mtime) is read afresh.
func TestReadReusesOnlyUnchangedRecords(t *testing.T) {
	m := testkit.Manifest(t)
	spec := testkit.Rich("Claude", 1)
	spec.Replica = rep
	folder := testkit.NewDataRoot(t).AddFolder(t, spec)
	c := rc(t, m, folder, model.RowSessions)
	first := read(t, sessions.Sessions{}, c)
	again := read(t, sessions.Sessions{}, c)
	k := testkit.FakeSession(1)
	if first.Items[k].Hash != again.Items[k].Hash {
		t.Fatal("unchanged record read differently")
	}
	p := filepath.Join(sessDir(folder), k+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(b), "futureField", "futureFieldEdited", 1)
	if err := os.WriteFile(p, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	after := read(t, sessions.Sessions{}, c)
	if !strings.Contains(string(after.Items[k].Value), "futureFieldEdited") {
		t.Fatal("edited record served from the cache")
	}
}
