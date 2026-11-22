package apply

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

const canary = "PARITYDESK-CANARY-TOKEN-7f3c"

// env is one test folder with an in-memory Local Storage.
type env struct {
	fs     *fsxtest.FS
	kv     *kv.MemOpener
	store  *kv.MemStore
	folder string
	in     Input
	held   bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{fs: fsxtest.New(t), kv: kv.NewMemOpener(), store: kv.NewMem(), folder: filepath.Join(root, "Claude-b")}
	if err := os.MkdirAll(e.folder, 0o700); err != nil {
		t.Fatal(err)
	}
	e.kv.Put(LSDir(e.folder), e.store)
	e.in = Input{Folder: e.folder, TmpDir: filepath.Join(root, ".paritydesk-tmp", "run-1"),
		QuarantineDir: filepath.Join(root, "generations", "g1", "quarantine")}
	return e
}

func (e *env) applier() *Applier {
	return &Applier{FS: e.fs, KV: e.kv, Holders: func(context.Context, string) (bool, string, error) {
		return e.held, "pid 1", nil
	}}
}

func (e *env) writeFile(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(e.folder, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func (e *env) putLS(t *testing.T, name, text string) {
	t.Helper()
	key, err := localstorage.Key(name)
	if err != nil {
		t.Fatal(err)
	}
	var b kv.Batch
	b.Put(key, localstorage.EncodeString(text))
	e.store.Reopen()
	if err := e.store.Write(&b); err != nil {
		t.Fatal(err)
	}
}

func (e *env) getLS(t *testing.T, name string) (string, bool) {
	t.Helper()
	key, _ := localstorage.Key(name)
	e.store.Reopen()
	v, ok, err := e.store.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return "", false
	}
	s, err := localstorage.DecodeText(v)
	if err != nil {
		t.Fatal(err)
	}
	return s, true
}

// tree returns every regular file under dir with its sha256.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = model.HashBytes(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func h(s string) string { return model.HashBytes([]byte(s)) }

func lsH(text string) string { return model.HashBytes(localstorage.EncodeString(text)) }

func writeOp(path, pre, content string) plan.Op {
	return plan.Op{Kind: plan.OpWriteFile, Row: model.RowSessions, Path: path, Content: []byte(content),
		PreHash: pre, PostHash: h(content)}
}

func putOp(name, pre, text string) plan.Op {
	c := localstorage.EncodeString(text)
	return plan.Op{Kind: plan.OpLSPut, Row: model.RowPinnedOrder, Key: name, Content: c, PreHash: pre, PostHash: model.HashBytes(c)}
}

// fixture: an existing config, a new session file, an existing LS value,
// a new LS value and an LS value to delete.
func fixture(t *testing.T) (*env, *plan.Plan) {
	t.Helper()
	e := newEnv(t)
	e.writeFile(t, "claude_desktop_config.json", `{"a":1}`)
	e.putLS(t, "dframe-store", `{"state":{}}`)
	e.putLS(t, "old-key", `{}`)
	p := &plan.Plan{Version: plan.Version, Ops: []plan.Op{
		writeOp("claude_desktop_config.json", h(`{"a":1}`), `{"a":2}`),
		writeOp("claude-code-sessions/a/b/local_1.json", "", `{"id":1}`),
		putOp("dframe-store", lsH(`{"state":{}}`), `{"state":{"x":1}}`),
		putOp("new-key", "", `"v"`),
		{Kind: plan.OpLSDelete, Row: model.RowSidebarView, Key: "old-key", PreHash: lsH(`{}`)},
	}}
	e.in.Plan = p
	return e, p
}

func TestApplyHappyPath(t *testing.T) {
	e, p := fixture(t)
	res, err := e.applier().Apply(context.Background(), e.in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != len(p.Ops) {
		t.Fatalf("applied %d", res.Applied)
	}
	b, _ := os.ReadFile(filepath.Join(e.folder, "claude_desktop_config.json"))
	if string(b) != `{"a":2}` {
		t.Fatalf("config %s", b)
	}
	fi, err := os.Stat(filepath.Join(e.folder, "claude_desktop_config.json"))
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("existing mode not kept: %v %v", fi.Mode(), err)
	}
	fi, err = os.Stat(filepath.Join(e.folder, "claude-code-sessions/a/b/local_1.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("new file mode: %v %v", fi, err)
	}
	if v, _ := e.getLS(t, "dframe-store"); v != `{"state":{"x":1}}` {
		t.Fatalf("store %s", v)
	}
	if v, _ := e.getLS(t, "new-key"); v != `"v"` {
		t.Fatalf("new key %s", v)
	}
	if _, ok := e.getLS(t, "old-key"); ok {
		t.Fatal("old key not deleted")
	}
	want := map[string]string{
		"path:claude_desktop_config.json":            h(`{"a":2}`),
		"path:claude-code-sessions/a/b/local_1.json": h(`{"id":1}`),
		"ls:dframe-store":                            lsH(`{"state":{"x":1}}`),
		"ls:new-key":                                 lsH(`"v"`),
		"ls:old-key":                                 "",
	}
	if len(res.PostHashes) != len(want) {
		t.Fatalf("post hashes %v", res.PostHashes)
	}
	for k, v := range want {
		if res.PostHashes[k] != v {
			t.Fatalf("post hash %s = %q, want %q", k, res.PostHashes[k], v)
		}
	}
	if !slices.Contains(e.fs.Calls, "FullSync "+e.folder) {
		t.Fatalf("no full sync: %v", e.fs.Calls)
	}
	if n := countPrefix(e.fs.Calls, "WriteFileAtomic"); n != 2 {
		t.Fatalf("writes %d: %v", n, e.fs.Calls)
	}
}

func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func assertCode(t *testing.T, err error, want pderr.Code) *pderr.Error {
	t.Helper()
	var pe *pderr.Error
	if !errors.As(err, &pe) || pe.Code != want {
		t.Fatalf("want *pderr.Error code %d, got %T %v", want, err, err)
	}
	return pe
}

func TestApplyPreHashMismatchWritesNothing(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(e *env, p *plan.Plan)
	}{
		{"last file op", func(e *env, p *plan.Plan) { p.Ops[1].PreHash = h("something") }},
		{"file changed on disk", func(e *env, p *plan.Plan) { e.writeFileNoT("claude_desktop_config.json", `{"a":9}`) }},
		{"existing LS key", func(e *env, p *plan.Plan) { p.Ops[2].PreHash = lsH("other") }},
		{"LS key expected absent", func(e *env, p *plan.Plan) { p.Ops[3].PreHash = lsH("x") }},
		{"LS delete of absent key", func(e *env, p *plan.Plan) { p.Ops[4].Key = "missing-key" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, p := fixture(t)
			tc.mutate(e, p)
			before := tree(t, e.folder)
			lsBefore := snapshotLS(t, e)
			_, err := e.applier().Apply(context.Background(), e.in)
			assertCode(t, err, pderr.Internal)
			if n := countPrefix(e.fs.Calls, "WriteFileAtomic") + countPrefix(e.fs.Calls, "MoveInto"); n != 0 {
				t.Fatalf("wrote: %v", e.fs.Calls)
			}
			if !mapsEqual(before, tree(t, e.folder)) || !mapsEqual(lsBefore, snapshotLS(t, e)) {
				t.Fatal("folder changed")
			}
		})
	}
}

func (e *env) writeFileNoT(rel, content string) {
	_ = os.WriteFile(filepath.Join(e.folder, rel), []byte(content), 0o600)
}

func snapshotLS(t *testing.T, e *env) map[string]string {
	t.Helper()
	out := map[string]string{}
	e.store.Reopen()
	if err := e.store.Scan(nil, func(k, v []byte) error { out[string(k)] = model.HashBytes(v); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestApplyRefusals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(e *env, a *Applier)
	}{
		{"holder present", func(e *env, a *Applier) { e.held = true }},
		{"holder check fails", func(e *env, a *Applier) {
			a.Holders = func(context.Context, string) (bool, string, error) { return false, "", errors.New("probe failed") }
		}},
		{"no holder check", func(e *env, a *Applier) { a.Holders = nil }},
		{"read-only", func(e *env, a *Applier) { a.ReadOnly = true }},
		{"no plan", func(e *env, a *Applier) { e.in.Plan = nil }},
		{"no Local Storage database", func(e *env, a *Applier) { e.kv.Stores = map[string]*kv.MemStore{} }},
		{"unknown kind", func(e *env, a *Applier) { e.in.Plan.Ops[0].Kind = "chmod" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := fixture(t)
			a := e.applier()
			tc.mutate(e, a)
			before := tree(t, e.folder)
			_, err := a.Apply(context.Background(), e.in)
			assertCode(t, err, pderr.Internal)
			if len(e.fs.Calls) != 0 || !mapsEqual(before, tree(t, e.folder)) {
				t.Fatalf("changed something: %v", e.fs.Calls)
			}
		})
	}
}

func TestApplyCancelledBeforeWrites(t *testing.T) {
	e, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.applier().Apply(ctx, e.in)
	assertCode(t, err, pderr.Internal)
	if len(e.fs.Calls) != 0 {
		t.Fatalf("wrote: %v", e.fs.Calls)
	}
}

func TestApplyLSWriteFailure(t *testing.T) {
	e, _ := fixture(t)
	e.store.FailWrite = errors.New("disk full")
	res, err := e.applier().Apply(context.Background(), e.in)
	pe := assertCode(t, err, pderr.ApplyRolledBack)
	if pe.Op != "apply" {
		t.Fatalf("op %q", pe.Op)
	}
	if res.Applied != 2 || res.PostHashes["path:claude_desktop_config.json"] != h(`{"a":2}`) {
		t.Fatalf("written files not reported: %+v", res)
	}
	for _, p := range []string{"claude_desktop_config.json", "claude-code-sessions/a/b/local_1.json"} {
		if !strings.Contains(err.Error(), p) {
			t.Fatalf("error does not name %s: %v", p, err)
		}
	}
}

func TestApplyFileWriteFailureAfterFirstWrite(t *testing.T) {
	e, _ := fixture(t)
	e.fs.Fail("WriteFileAtomic", nil) // first write succeeds
	e.fs.Fail("WriteFileAtomic", errors.New("no space"))
	_, err := e.applier().Apply(context.Background(), e.in)
	assertCode(t, err, pderr.ApplyRolledBack)
}

func TestApplyFirstFileWriteFailure(t *testing.T) {
	e, _ := fixture(t)
	e.fs.Fail("WriteFileAtomic", errors.New("no space"))
	_, err := e.applier().Apply(context.Background(), e.in)
	assertCode(t, err, pderr.ApplyRolledBack)
}

func TestApplyFullSyncFailure(t *testing.T) {
	e, _ := fixture(t)
	e.fs.Fail("FullSync", errors.New("io"))
	_, err := e.applier().Apply(context.Background(), e.in)
	assertCode(t, err, pderr.ApplyRolledBack)
}

// tamperOpener returns snapshots whose named key holds other bytes.
type tamperOpener struct {
	*kv.MemOpener
	key []byte
}

func (o tamperOpener) OpenSnapshot(ctx context.Context, dir string) (kv.Reader, error) {
	r, err := o.MemOpener.OpenSnapshot(ctx, dir)
	if err != nil {
		return nil, err
	}
	s := r.(*kv.MemStore)
	var b kv.Batch
	b.Put(o.key, localstorage.EncodeString("tampered"))
	if err := s.Write(&b); err != nil {
		return nil, err
	}
	return s, nil
}

func TestApplyLSReadBackMismatch(t *testing.T) {
	e, _ := fixture(t)
	key, _ := localstorage.Key("new-key")
	a := e.applier()
	a.KV = tamperOpener{MemOpener: e.kv, key: key}
	res, err := a.Apply(context.Background(), e.in)
	pe := assertCode(t, err, pderr.ApplyRolledBack)
	if !strings.Contains(pe.Msg, "read-back mismatch for ls:new-key") {
		t.Fatalf("msg %q", pe.Msg)
	}
	if res.PostHashes["ls:new-key"] != lsH("tampered") {
		t.Fatalf("read-back hash not reported: %v", res.PostHashes)
	}
}

// shortWriteFS writes truncated content, so the file read-back differs.
type shortWriteFS struct{ *fsxtest.FS }

func (f shortWriteFS) WriteFileAtomic(root, rel string, data []byte, perm fs.FileMode, tmpDir string) error {
	return f.FS.WriteFileAtomic(root, rel, data[:len(data)/2], perm, tmpDir)
}

func TestApplyFileReadBackMismatch(t *testing.T) {
	e, _ := fixture(t)
	a := e.applier()
	a.FS = shortWriteFS{e.fs}
	_, err := a.Apply(context.Background(), e.in)
	pe := assertCode(t, err, pderr.ApplyRolledBack)
	if !strings.Contains(pe.Msg, "read-back mismatch for path:claude_desktop_config.json") {
		t.Fatalf("msg %q", pe.Msg)
	}
}

func TestApplyQuarantine(t *testing.T) {
	e := newEnv(t)
	e.writeFile(t, "claude-code-sessions/a/b/local_2.json", "rec")
	e.writeFile(t, "claude-code-sessions/a/b/local_3.json.tmp", "tmp")
	e.in.Plan = &plan.Plan{Version: plan.Version, Ops: []plan.Op{
		{Kind: plan.OpQuarantine, Row: model.RowSessions, Path: "claude-code-sessions/a/b/local_2.json", PreHash: h("rec"), Destructive: true},
		{Kind: plan.OpQuarantine, Row: model.RowSessions, Path: "claude-code-sessions/a/b/local_3.json.tmp", PreHash: h("tmp"), Reason: plan.ReasonAppTmpCandidate},
	}}
	res, err := e.applier().Apply(context.Background(), e.in)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"claude-code-sessions/a/b/local_2.json", "claude-code-sessions/a/b/local_3.json.tmp"} {
		if _, err := os.Lstat(filepath.Join(e.folder, rel)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s still in folder: %v", rel, err)
		}
		if _, err := os.Stat(filepath.Join(e.in.QuarantineDir, rel)); err != nil {
			t.Fatalf("%s not quarantined: %v", rel, err)
		}
		if v, ok := res.PostHashes["path:"+rel]; !ok || v != "" {
			t.Fatalf("post hash of %s: %q %v", rel, v, ok)
		}
	}
	if res.Applied != 2 {
		t.Fatalf("applied %d", res.Applied)
	}
}

func TestApplyRefusesSymlinkTarget(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(filepath.Dir(e.folder), "outside.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.folder, "link.json")); err != nil {
		t.Fatal(err)
	}
	e.in.Plan = &plan.Plan{Ops: []plan.Op{writeOp("link.json", h("{}"), `{"x":1}`)}}
	_, err := e.applier().Apply(context.Background(), e.in)
	assertCode(t, err, pderr.Internal)
	if b, _ := os.ReadFile(outside); string(b) != "{}" {
		t.Fatal("followed the symlink")
	}
}

func TestCheck(t *testing.T) {
	e, p := fixture(t)
	a := e.applier()
	if ok, _, err := a.Check(context.Background(), p, e.folder); err != nil || ok {
		t.Fatalf("before apply: ok=%v err=%v", ok, err)
	}
	if _, err := a.Apply(context.Background(), e.in); err != nil {
		t.Fatal(err)
	}
	ok, mism, err := a.Check(context.Background(), p, e.folder)
	if err != nil || !ok || len(mism) != 0 {
		t.Fatalf("after apply: ok=%v mism=%v err=%v", ok, mism, err)
	}
	e.writeFile(t, "claude_desktop_config.json", `{"a":3}`)
	e.putLS(t, "old-key", `{}`)
	ok, mism, err = a.Check(context.Background(), p, e.folder)
	if err != nil || ok {
		t.Fatalf("after edit: ok=%v err=%v", ok, err)
	}
	want := []string{"path:claude_desktop_config.json", "ls:old-key"}
	if !slices.Equal(mism, want) {
		t.Fatalf("mismatched %v, want %v", mism, want)
	}
	if len(e.fs.Calls) != countPrefix(e.fs.Calls, "WriteFileAtomic")+countPrefix(e.fs.Calls, "FullSync") || countPrefix(e.fs.Calls, "WriteFileAtomic") != 2 {
		t.Fatalf("check wrote: %v", e.fs.Calls)
	}
	if _, _, err := a.Check(context.Background(), nil, e.folder); err == nil {
		t.Fatal("nil plan accepted")
	}
}

func TestCheckFilesOnlyNeedsNoLocalStorage(t *testing.T) {
	e := newEnv(t)
	e.kv.Stores = map[string]*kv.MemStore{}
	e.writeFile(t, "a.json", "x")
	p := &plan.Plan{Ops: []plan.Op{writeOp("a.json", "", "x")}}
	ok, mism, err := e.applier().Check(context.Background(), p, e.folder)
	if err != nil || !ok || mism != nil {
		t.Fatalf("ok=%v mism=%v err=%v", ok, mism, err)
	}
}

func TestSecretContentNeverInErrors(t *testing.T) {
	secret := `{"env":{"TOKEN":"` + canary + `"}}`
	tests := []struct {
		name  string
		setup func(e *env, p *plan.Plan)
	}{
		{"pre-hash mismatch", func(e *env, p *plan.Plan) { p.Ops[0].PreHash = h("x") }},
		{"LS write failure", func(e *env, p *plan.Plan) { e.store.FailWrite = errors.New("disk full") }},
		{"file write failure", func(e *env, p *plan.Plan) { e.fs.Fail("WriteFileAtomic", errors.New("no space")) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, p := fixture(t)
			p.Ops[0] = writeOp("claude_desktop_config.json", h(`{"a":1}`), secret)
			p.Ops[0].Secret = true
			p.Ops[3] = putOp("new-key", "", secret)
			p.Ops[3].Secret = true
			tc.setup(e, p)
			_, err := e.applier().Apply(context.Background(), e.in)
			if err == nil {
				t.Fatal("no error")
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("canary in error: %v", err)
			}
		})
	}
}

// TestApplyReportsTouched checks Result.Touched and LSOpened, which tell
// the orchestrator whether a failed apply left anything to restore.
func TestApplyReportsTouched(t *testing.T) {
	tests := []struct {
		name         string
		prepare      func(t *testing.T, e *env, p *plan.Plan)
		wantErr      bool
		wantTouched  bool
		wantLSOpened bool
	}{
		{name: "success", wantTouched: true, wantLSOpened: true},
		{name: "folder held", prepare: func(_ *testing.T, e *env, _ *plan.Plan) { e.held = true }, wantErr: true},
		{name: "file changed before apply", prepare: func(t *testing.T, e *env, _ *plan.Plan) {
			e.writeFile(t, "claude_desktop_config.json", `{"a":9}`)
		}, wantErr: true},
		{name: "local storage changed before apply", prepare: func(t *testing.T, e *env, _ *plan.Plan) {
			e.putLS(t, "dframe-store", `{"state":{"other":1}}`)
		}, wantErr: true, wantTouched: true, wantLSOpened: true},
		{name: "files only", prepare: func(_ *testing.T, _ *env, p *plan.Plan) { p.Ops = p.Ops[:2] }, wantTouched: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, p := fixture(t)
			if tc.prepare != nil {
				tc.prepare(t, e, p)
			}
			res, err := e.applier().Apply(context.Background(), e.in)
			if (err != nil) != tc.wantErr || res.Touched != tc.wantTouched || res.LSOpened != tc.wantLSOpened {
				t.Fatalf("err %v touched %v ls %v; want err %v touched %v ls %v",
					err, res.Touched, res.LSOpened, tc.wantErr, tc.wantTouched, tc.wantLSOpened)
			}
		})
	}
}

// TestApplyLocalStorageLocked: when another process holds the Local
// Storage lock, apply stops before writing anything, says the folder is
// in use and reports it untouched, so nothing needs restoring.
func TestApplyLocalStorageLocked(t *testing.T) {
	e, _ := fixture(t)
	e.kv.WritableErr = fmt.Errorf("leveldb: lock database: %w", kv.ErrLocked)
	before := tree(t, e.folder)
	res, err := e.applier().Apply(context.Background(), e.in)
	pe := assertCode(t, err, pderr.Internal)
	if !errors.Is(err, kv.ErrLocked) || !strings.Contains(pe.Msg, "in use") {
		t.Fatalf("error does not say the folder is in use: %v", err)
	}
	if res.Touched || res.LSOpened || len(e.fs.Calls) != 0 || !mapsEqual(before, tree(t, e.folder)) {
		t.Fatalf("changed something: touched %v ls %v calls %v", res.Touched, res.LSOpened, e.fs.Calls)
	}
}

// TestAfterFirstWrite: the e2e crash hook runs once, after the first file
// op has landed and before the second; for a plan with only Local Storage
// ops it runs after the batch is written and the store closed.
func TestAfterFirstWrite(t *testing.T) {
	e, _ := fixture(t)
	a := e.applier()
	var seen []string
	a.AfterFirstWrite = func() {
		b, _ := os.ReadFile(filepath.Join(e.folder, "claude_desktop_config.json"))
		_, err := os.Stat(filepath.Join(e.folder, "claude-code-sessions/a/b/local_1.json"))
		v, _ := e.getLS(t, "dframe-store")
		seen = append(seen, fmt.Sprintf("%s|%v|%s", b, errors.Is(err, fs.ErrNotExist), v))
	}
	if _, err := a.Apply(context.Background(), e.in); err != nil {
		t.Fatal(err)
	}
	if want := []string{`{"a":2}|true|{"state":{}}`}; !slices.Equal(seen, want) {
		t.Fatalf("hook saw %q, want %q", seen, want)
	}

	e = newEnv(t)
	e.putLS(t, "dframe-store", `{"state":{}}`)
	e.in.Plan = &plan.Plan{Version: plan.Version, Ops: []plan.Op{putOp("dframe-store", lsH(`{"state":{}}`), `{"state":{"x":1}}`)}}
	a = e.applier()
	calls := 0
	a.AfterFirstWrite = func() {
		calls++
		if v, _ := e.getLS(t, "dframe-store"); v != `{"state":{"x":1}}` {
			t.Errorf("hook ran before the Local Storage batch landed: %s", v)
		}
	}
	if _, err := a.Apply(context.Background(), e.in); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("hook ran %d times, want 1", calls)
	}
}
