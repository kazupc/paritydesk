package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
)

const goldenPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>io.github.kazupc.paritydesk.run.abcd-1234</string>
	<key>ProgramArguments</key>
	<array>
		<string>/opt/bin/paritydesk</string>
		<string>switch</string>
		<string>--from-runner</string>
		<string>--confirmed-plan=a&amp;b&lt;c&gt;</string>
		<string>--</string>
		<string>work</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PARITYDESK_RUN_ID</key>
		<string>abcd-1234</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>AbandonProcessGroup</key>
	<true/>
	<key>StandardOutPath</key>
	<string>/logs/abcd-1234.out</string>
	<key>StandardErrorPath</key>
	<string>/logs/abcd-1234.out</string>
</dict>
</plist>
`

func TestPlistGolden(t *testing.T) {
	j := &Job{
		Label:      LabelPrefix + "abcd-1234",
		RunID:      "abcd-1234",
		Binary:     "/opt/bin/paritydesk",
		Args:       []string{"switch", "--from-runner", "--confirmed-plan=a&b<c>", "--", "work"},
		PlistPath:  "/run/" + LabelPrefix + "abcd-1234.plist",
		ResultPath: "/run/abcd-1234.result.json",
		StdoutPath: "/logs/abcd-1234.out",
	}
	got, err := j.Plist()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != goldenPlist {
		t.Errorf("plist mismatch:\n%s", got)
	}
	for _, bad := range []string{"KeepAlive", "/bin/sh", "<string>-c</string>"} {
		if strings.Contains(string(got), bad) {
			t.Errorf("plist contains %q", bad)
		}
	}
}

func TestPlistRefusesNUL(t *testing.T) {
	j := &Job{Label: LabelPrefix + "abcd-1234", RunID: "abcd-1234", Binary: "/b", Args: []string{"a\x00b"}}
	if _, err := j.Plist(); err == nil {
		t.Fatal("NUL argument accepted")
	}
}

// writeBinary creates an executable file with mode perm.
func writeBinary(t *testing.T, dir string, perm os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, "paritydesk")
	if err := os.WriteFile(p, []byte("#!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, perm); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewJobValidation(t *testing.T) {
	dir := t.TempDir()
	good := writeBinary(t, dir, 0o755)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	worldW := writeBinary(t, sub, 0o757)
	sub2 := filepath.Join(dir, "sub2")
	if err := os.Mkdir(sub2, 0o700); err != nil {
		t.Fatal(err)
	}
	groupW := writeBinary(t, sub2, 0o775)
	sub3 := filepath.Join(dir, "sub3")
	if err := os.Mkdir(sub3, 0o700); err != nil {
		t.Fatal(err)
	}
	noExec := writeBinary(t, sub3, 0o644)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, runDir, binary, runID string
		ok                          bool
	}{
		{"ok", dir, good, "abcd1234", true},
		{"short run id", dir, good, "abc", false},
		{"upper-case run id", dir, good, "ABCD1234", false},
		{"slash in run id", dir, good, "abcd/1234", false},
		{"relative binary", dir, "paritydesk", "abcd1234", false},
		{"unclean binary", dir, dir + "/./paritydesk", "abcd1234", false},
		{"missing binary", dir, filepath.Join(dir, "nope"), "abcd1234", false},
		{"directory binary", dir, dir, "abcd1234", false},
		{"symlink binary", dir, link, "abcd1234", false},
		{"world-writable", dir, worldW, "abcd1234", false},
		{"group-writable", dir, groupW, "abcd1234", false},
		{"not executable", dir, noExec, "abcd1234", false},
		{"relative run dir", "run", good, "abcd1234", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j, err := NewJob(tc.runDir, dir, tc.binary, tc.runID, []string{"switch"})
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if !tc.ok {
				return
			}
			if j.Label != LabelPrefix+tc.runID || j.PlistPath != filepath.Join(dir, j.Label+".plist") ||
				j.ResultPath != filepath.Join(dir, tc.runID+".result.json") {
				t.Errorf("job = %+v", j)
			}
		})
	}
}

// recLaunchd records calls and checks file state at each call.
type recLaunchd struct {
	calls   []string
	bootErr error
}

func (r *recLaunchd) Bootstrap(_ context.Context, uid int, plistPath string) error {
	r.calls = append(r.calls, "bootstrap "+plistPath)
	if fi, err := os.Stat(plistPath); err != nil || fi.Mode().Perm() != 0o600 {
		r.calls = append(r.calls, "bad plist")
	}
	return r.bootErr
}

func (r *recLaunchd) Bootout(_ context.Context, uid int, label string) error {
	r.calls = append(r.calls, "bootout "+label)
	return errors.New("not loaded")
}

func TestSubmitFinishReap(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	logDir := filepath.Join(dir, "logs")
	bin := writeBinary(t, dir, 0o755)
	ld := &recLaunchd{}
	ctx := context.Background()

	j1, err := NewJob(runDir, logDir, bin, "run-0001", []string{"switch"})
	if err != nil {
		t.Fatal(err)
	}
	j2, err := NewJob(runDir, logDir, bin, "run-0002", []string{"switch"})
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range []*Job{j1, j2} {
		if err := Submit(ctx, j, ld, 501); err != nil {
			t.Fatal(err)
		}
	}
	if len(ld.calls) != 2 || strings.Contains(strings.Join(ld.calls, ","), "bad plist") {
		t.Fatalf("calls = %v", ld.calls)
	}

	// Finish j1: result first, then the plist goes; never a bootout.
	ld.calls = nil
	if err := Finish(j1, Result{RunID: "run-0001", Code: 0, Finished: time.Unix(1, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	if len(ld.calls) != 0 {
		t.Fatalf("Finish called launchd: %v", ld.calls)
	}
	if _, err := os.Stat(j1.PlistPath); !os.IsNotExist(err) {
		t.Fatal("plist not removed")
	}
	if fi, err := os.Stat(j1.ResultPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("result file: %v", err)
	}

	// A finished job whose plist is still present (Finish interrupted).
	j3, _ := NewJob(runDir, logDir, bin, "run-0003", nil)
	if err := Submit(ctx, j3, ld, 501); err != nil {
		t.Fatal(err)
	}
	if err := WriteResult(j3.ResultPath, Result{RunID: "run-0003"}); err != nil {
		t.Fatal(err)
	}

	ld.calls = nil
	if err := Reap(ctx, ld, 501, runDir); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(ld.calls, ",")
	want := "bootout " + j1.Label + ",bootout " + j3.Label
	if got != want {
		t.Fatalf("reap calls = %q, want %q", got, want)
	}
	if _, err := os.Stat(j3.PlistPath); !os.IsNotExist(err) {
		t.Error("leftover plist of a finished run not removed")
	}
	if _, err := os.Stat(j2.PlistPath); err != nil {
		t.Error("plist of a running job removed")
	}
}

func TestSubmitBootstrapFailureRemovesPlist(t *testing.T) {
	dir := t.TempDir()
	bin := writeBinary(t, dir, 0o755)
	j, _ := NewJob(filepath.Join(dir, "run"), filepath.Join(dir, "logs"), bin, "run-0001", nil)
	ld := &recLaunchd{bootErr: errors.New("boom")}
	if err := Submit(context.Background(), j, ld, 501); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(j.PlistPath); !os.IsNotExist(err) {
		t.Error("plist kept after a failed bootstrap")
	}
}

func TestDetacherSubmitReapsFirst(t *testing.T) {
	dir := t.TempDir()
	bin := writeBinary(t, dir, 0o755)
	runDir := filepath.Join(dir, "run")
	if err := WriteResult(filepath.Join(runDir, "old-run-1.result.json"), Result{RunID: "old-run-1"}); err != nil {
		t.Fatal(err)
	}
	ld := &recLaunchd{}
	d := Detacher{RunDir: runDir, LogDir: filepath.Join(dir, "logs"), Binary: bin, UID: 501, Launchd: ld}
	id, err := d.Submit(context.Background(), []string{"switch", "--from-runner", "--", "work"})
	if err != nil {
		t.Fatal(err)
	}
	if !ValidRunID(id) {
		t.Fatalf("run id %q", id)
	}
	if len(ld.calls) != 2 || ld.calls[0] != "bootout "+LabelPrefix+"old-run-1" ||
		!strings.HasPrefix(ld.calls[1], "bootstrap ") {
		t.Fatalf("calls = %v", ld.calls)
	}
	j, err := d.Job(id)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(j.PlistPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "<string>"+id+"</string>") {
		t.Error("plist lacks the run id")
	}
}

func TestResultRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.result.json")
	in := Result{RunID: "run-0001", Code: 41, ErrorID: pderr.ID(41), From: "a", To: "b", Finished: time.Unix(1700000000, 0).UTC()}
	if err := WriteResult(p, in); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v", err)
	}
	out, err := ReadResult(p)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip: %+v != %+v", out, in)
	}
	if _, err := ReadResult(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing file read")
	}
}

type procTable []app.Process

func (p procTable) Processes(context.Context) ([]app.Process, error) { return p, nil }

func TestInsideApp(t *testing.T) {
	isApp := func(p app.Process) bool { return strings.HasPrefix(p.Exe, "/Apps/Claude.app/") }
	table := procTable{
		{PID: 1, PPID: 0, Exe: "/sbin/launchd"},
		{PID: 10, PPID: 1, Exe: "/Apps/Claude.app/Contents/MacOS/Claude"},
		{PID: 11, PPID: 10, Exe: "/bin/zsh"},
		{PID: 12, PPID: 11, Exe: "/usr/local/bin/paritydesk"},
		{PID: 20, PPID: 1, Exe: "/bin/zsh"},
		{PID: 21, PPID: 20, Exe: "/usr/local/bin/paritydesk"},
		{PID: 30, PPID: 31, Exe: "/bin/a"},
		{PID: 31, PPID: 30, Exe: "/bin/b"},
		{PID: 40, PPID: 99, Exe: "/bin/orphan"},
	}
	tests := []struct {
		pid  int
		want bool
	}{
		{12, true}, {21, false}, {30, false}, {40, false}, {77, false},
	}
	for _, tc := range tests {
		got, err := InsideApp(context.Background(), table, tc.pid, isApp)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("pid %d: got %v, want %v", tc.pid, got, tc.want)
		}
	}
	if _, err := InsideApp(context.Background(), nil, 1, isApp); err == nil {
		t.Error("nil table accepted")
	}
}

func testPlan(now time.Time) *plan.Plan {
	return &plan.Plan{
		Version: plan.Version, From: "a", Target: plan.Target{Alias: "b"},
		CreatedAt: now,
		Ops:       []plan.Op{{Kind: plan.OpWriteFile, Row: model.RowSessions, Path: "x.json", Content: []byte("secret"), PostHash: "h"}},
		Changes:   []model.Change{{Row: model.RowSessions, Kind: model.ChangeDeleted, Key: "k"}},
	}
}

func TestPlanStore(t *testing.T) {
	dir := t.TempDir()
	s := PlanStore{Dir: dir}
	now := time.Now()
	p := testPlan(now)
	h, err := s.Save(p)
	if err != nil {
		t.Fatal(err)
	}
	if h != p.Hash() {
		t.Fatal("hash differs")
	}
	file := filepath.Join(dir, "plans", h+".json")
	fi, err := os.Stat(file)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("plan file: %v", err)
	}
	if b, _ := os.ReadFile(file); strings.Contains(string(b), "secret") {
		t.Fatal("op content serialized")
	}
	got, err := s.Load(h, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hash() != h || len(got.Changes) != 1 {
		t.Fatalf("loaded %+v", got)
	}

	declined := func(err error) bool { return pderr.CodeOf(err) == pderr.Declined }
	if _, err := s.Load(h, now.Add(3*time.Hour)); err != nil {
		t.Errorf("an old plan: %v", err)
	}
	if _, err := s.Load(strings.Repeat("0", 64), now); !declined(err) {
		t.Errorf("missing: %v", err)
	}
	if _, err := s.Load("../x", now); !declined(err) {
		t.Errorf("bad hash: %v", err)
	}
	// A tampered file no longer hashes to its name.
	tampered := testPlan(now)
	tampered.Changes = nil
	h2, err := s.Save(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "plans", h2+".json"), file); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(h, now); !declined(err) {
		t.Errorf("hash mismatch: %v", err)
	}

	// Pruning: a plan older than a day goes on the next save.
	old := filepath.Join(dir, "plans", strings.Repeat("1", 64)+".json")
	if err := os.WriteFile(old, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := now.Add(-25 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(testPlan(now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old plan not pruned")
	}
}

type recRunner struct{ calls [][]string }

func (r *recRunner) Run(_ context.Context, t app.Template, args ...string) ([]byte, error) {
	if _, err := app.Argv(t, args...); err != nil {
		return nil, err
	}
	r.calls = append(r.calls, append([]string{string(t)}, args...))
	return nil, nil
}

func TestNewLaunchd(t *testing.T) {
	r := &recRunner{}
	ld := NewLaunchd(r)
	ctx := context.Background()
	if err := ld.Bootstrap(ctx, 501, "/run/"+LabelPrefix+"abcd1234.plist"); err != nil {
		t.Fatal(err)
	}
	if err := ld.Bootout(ctx, 501, LabelPrefix+"abcd1234"); err != nil {
		t.Fatal(err)
	}
	if err := ld.Bootout(ctx, 501, "other.label"); err == nil {
		t.Error("foreign label accepted")
	}
	if len(r.calls) != 2 || r.calls[0][0] != string(app.TmplBootstrap) || r.calls[1][0] != string(app.TmplBootout) {
		t.Fatalf("calls = %v", r.calls)
	}
}
