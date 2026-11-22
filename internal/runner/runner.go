package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
)

// LabelPrefix prefixes every job label; the run id completes it. It is
// app.JobLabelPrefix so that app.Argv accepts exactly these labels.
const LabelPrefix = app.JobLabelPrefix

// EnvRunID is the environment variable the job's plist sets to the run id,
// so the job can rebuild its Job (Detacher.Job) and call Finish.
const EnvRunID = "PARITYDESK_RUN_ID"

// File name suffixes in the run directory.
const (
	plistSuffix  = ".plist"
	resultSuffix = ".result.json"
)

// resultRetention bounds how long Reap keeps result files of finished
// jobs after booting them out.
const resultRetention = 7 * 24 * time.Hour

// planRetention is how long a saved plan file is kept.
const planRetention = 24 * time.Hour

var (
	runIDRE    = regexp.MustCompile(`^[0-9a-z-]{8,64}$`)
	planHashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Job is one detached run.
type Job struct {
	Label      string
	Binary     string   // absolute path, resolved and verified at submit
	Args       []string // built by orchestrator.SwitchOptions.JobArgs, never by hand
	PlistPath  string   // <run dir>/<label>.plist
	ResultPath string   // <run dir>/<run id>.result.json
	StdoutPath string   // <log dir>/<run id>.out
	// RunID is the run id the label was built from.
	RunID string
}

// ValidRunID reports whether id is an acceptable run id.
func ValidRunID(id string) bool { return runIDRE.MatchString(id) }

// NewJob validates binary (absolute, regular, executable, not
// world-writable) and builds the job for runID.
func NewJob(runDir, logDir, binary, runID string, args []string) (*Job, error) {
	if !ValidRunID(runID) {
		return nil, pderr.New(pderr.Internal, "runner", "invalid run id")
	}
	for _, d := range []string{runDir, logDir} {
		if err := checkAbsClean(d); err != nil {
			return nil, pderr.Wrap(pderr.Internal, "runner", err)
		}
	}
	if err := checkBinary(binary); err != nil {
		return nil, pderr.Wrap(pderr.Internal, "runner", err)
	}
	label := LabelPrefix + runID
	return &Job{
		Label:      label,
		Binary:     binary,
		Args:       append([]string(nil), args...),
		PlistPath:  filepath.Join(runDir, label+plistSuffix),
		ResultPath: filepath.Join(runDir, runID+resultSuffix),
		StdoutPath: filepath.Join(logDir, runID+".out"),
		RunID:      runID,
	}, nil
}

func checkAbsClean(p string) error {
	if p == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return errors.New("path must be absolute and clean")
	}
	return nil
}

// checkBinary refuses anything but an absolute, regular, executable file
// that neither its group nor others may write.
func checkBinary(p string) error {
	if err := checkAbsClean(p); err != nil {
		return fmt.Errorf("binary: %w", err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return fmt.Errorf("binary: %w", err)
	}
	m := fi.Mode()
	switch {
	case !m.IsRegular():
		return errors.New("binary: not a regular file")
	case m.Perm()&0o111 == 0:
		return errors.New("binary: not executable")
	case m.Perm()&0o022 != 0:
		return errors.New("binary: group- or world-writable")
	}
	return nil
}

// Plist renders the launchd property list (XML, no shell, RunAtLoad,
// no KeepAlive, AbandonProcessGroup).
func (j *Job) Plist() ([]byte, error) {
	if j == nil || j.Label == "" || j.Binary == "" {
		return nil, pderr.New(pderr.Internal, "runner", "incomplete job")
	}
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	writeKeyString(&b, "Label", j.Label)
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range append([]string{j.Binary}, j.Args...) {
		b.WriteString("\t\t<string>")
		if err := escape(&b, a); err != nil {
			return nil, err
		}
		b.WriteString("</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	b.WriteString("\t\t<key>" + EnvRunID + "</key>\n\t\t<string>")
	if err := escape(&b, j.RunID); err != nil {
		return nil, err
	}
	b.WriteString("</string>\n\t</dict>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>AbandonProcessGroup</key>\n\t<true/>\n")
	writeKeyString(&b, "StandardOutPath", j.StdoutPath)
	writeKeyString(&b, "StandardErrorPath", j.StdoutPath)
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes(), nil
}

func writeKeyString(b *bytes.Buffer, key, val string) {
	b.WriteString("\t<key>" + key + "</key>\n\t<string>")
	// Labels and paths were validated; escaping cannot fail for them.
	_ = escape(b, val)
	b.WriteString("</string>\n")
}

func escape(b *bytes.Buffer, s string) error {
	if strings.ContainsRune(s, 0) {
		return pderr.New(pderr.Internal, "runner", "argument contains NUL")
	}
	return xml.EscapeText(b, []byte(s))
}

// Launchd submits and removes jobs.
type Launchd interface {
	Bootstrap(ctx context.Context, uid int, plistPath string) error
	Bootout(ctx context.Context, uid int, label string) error
}

// NewLaunchd returns the launchctl implementation over app's exec runner.
func NewLaunchd(r app.Runner) Launchd { return launchctl{r: r} }

type launchctl struct{ r app.Runner }

func (l launchctl) Bootstrap(ctx context.Context, uid int, plistPath string) error {
	if _, err := l.r.Run(ctx, app.TmplBootstrap, strconv.Itoa(uid), plistPath); err != nil {
		return fmt.Errorf("runner: bootstrap: %w", err)
	}
	return nil
}

func (l launchctl) Bootout(ctx context.Context, uid int, label string) error {
	if _, err := l.r.Run(ctx, app.TmplBootout, strconv.Itoa(uid), label); err != nil {
		return fmt.Errorf("runner: bootout: %w", err)
	}
	return nil
}

// Submit writes the plist (0600) and bootstraps the job.
func Submit(ctx context.Context, j *Job, ld Launchd, uid int) error {
	data, err := j.Plist()
	if err != nil {
		return err
	}
	dir := filepath.Dir(j.PlistPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return pderr.Wrap(pderr.Internal, "runner", err)
	}
	if err := os.MkdirAll(filepath.Dir(j.StdoutPath), 0o700); err != nil {
		return pderr.Wrap(pderr.Internal, "runner", err)
	}
	if err := writeAtomic(j.PlistPath, data); err != nil {
		return pderr.Wrap(pderr.Internal, "runner: plist", err)
	}
	if err := ld.Bootstrap(ctx, uid, j.PlistPath); err != nil {
		_ = os.Remove(j.PlistPath)
		return pderr.Wrap(pderr.Internal, "runner", err)
	}
	return nil
}

// Finish is the job's last step: write the result file, then remove the
// plist. The job never boots itself out (that would send it SIGTERM while
// it finishes); Reap does it later.
func Finish(j *Job, r Result) error {
	if err := WriteResult(j.ResultPath, r); err != nil {
		return err
	}
	if err := os.Remove(j.PlistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return pderr.Wrap(pderr.Internal, "runner: remove plist", err)
	}
	return nil
}

// Reap boots out every paritydesk job label in runDir whose result file
// exists (a finished job), and removes leftover plists of those runs. It
// is called before each Submit and by `doctor`. Labels of jobs without a
// result file are left alone (they may still be running). Bootout errors
// are ignored (a finished job is usually unloaded already); result files
// older than a week are removed after their bootout.
func Reap(ctx context.Context, ld Launchd, uid int, runDir string) error {
	entries, err := os.ReadDir(runDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return pderr.Wrap(pderr.Internal, "runner: reap", err)
	}
	now := time.Now()
	for _, e := range entries {
		runID, ok := strings.CutSuffix(e.Name(), resultSuffix)
		if !ok || !ValidRunID(runID) || !e.Type().IsRegular() {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		label := LabelPrefix + runID
		_ = ld.Bootout(ctx, uid, label)
		plist := filepath.Join(runDir, label+plistSuffix)
		if err := os.Remove(plist); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return pderr.Wrap(pderr.Internal, "runner: reap", err)
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > resultRetention {
			_ = os.Remove(filepath.Join(runDir, e.Name()))
		}
	}
	return nil
}

// Result is the job's result file.
type Result struct {
	RunID    string    `json:"run"`
	Code     int       `json:"code"`
	ErrorID  string    `json:"error_id,omitempty"` // PD-E<code>
	From     string    `json:"from,omitempty"`
	To       string    `json:"to,omitempty"`
	Finished time.Time `json:"finished"`
}

// WriteResult writes the result file atomically (0600).
func WriteResult(path string, r Result) error {
	b, err := json.Marshal(r)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "runner: result", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return pderr.Wrap(pderr.Internal, "runner: result", err)
	}
	if err := writeAtomic(path, append(b, '\n')); err != nil {
		return pderr.Wrap(pderr.Internal, "runner: result", err)
	}
	return nil
}

// ReadResult reads a result file.
func ReadResult(path string) (Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("runner: read result: %w", err)
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return Result{}, fmt.Errorf("runner: parse result: %w", err)
	}
	return r, nil
}

// writeAtomic writes data to path through a 0600 temporary file in the same
// directory, synced and renamed into place.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// InsideApp reports whether pid descends from a process for which isApp
// is true (walks the parent chain via the process table; a cycle or a
// missing parent ends the walk). In production isApp is
// app.IsAppProcess bound to the located bundle path.
func InsideApp(ctx context.Context, procs app.ProcessTable, pid int, isApp func(app.Process) bool) (bool, error) {
	if procs == nil || isApp == nil {
		return false, pderr.New(pderr.Internal, "runner", "no process table")
	}
	list, err := procs.Processes(ctx)
	if err != nil {
		return false, fmt.Errorf("runner: process table: %w", err)
	}
	byPID := make(map[int]app.Process, len(list))
	for _, p := range list {
		byPID[p.PID] = p
	}
	seen := map[int]bool{}
	for cur := pid; cur > 0 && !seen[cur]; {
		seen[cur] = true
		p, ok := byPID[cur]
		if !ok {
			break
		}
		if isApp(p) {
			return true, nil
		}
		cur = p.PPID
	}
	return false, nil
}

// Detacher is what the orchestrator uses to hand a run to launchd.
type Detacher struct {
	RunDir, LogDir string
	Binary         string // os.Executable(), resolved
	UID            int
	Launchd        Launchd
	Procs          app.ProcessTable
	IsApp          func(app.Process) bool
}

// InsideApp reports whether the current process runs inside the app.
func (d Detacher) InsideApp(ctx context.Context) (bool, error) {
	return InsideApp(ctx, d.Procs, os.Getpid(), d.IsApp)
}

// Submit creates and submits a job running the binary with args, and
// returns the run id whose result file the caller may wait on.
func (d Detacher) Submit(ctx context.Context, args []string) (runID string, err error) {
	if d.Launchd == nil {
		return "", pderr.New(pderr.Internal, "runner", "no launchd")
	}
	if err := Reap(ctx, d.Launchd, d.UID, d.RunDir); err != nil {
		return "", err
	}
	runID, err = NewRunID(time.Now())
	if err != nil {
		return "", err
	}
	j, err := NewJob(d.RunDir, d.LogDir, d.Binary, runID, args)
	if err != nil {
		return "", err
	}
	if err := Submit(ctx, j, d.Launchd, d.UID); err != nil {
		return "", err
	}
	return runID, nil
}

// Job rebuilds the job of runID (for the job itself, which reads the id
// from EnvRunID, to call Finish). Args are not needed for Finish.
func (d Detacher) Job(runID string) (*Job, error) {
	return NewJob(d.RunDir, d.LogDir, d.Binary, runID, nil)
}

// NewRunID returns a fresh run id: the time in base 36 and 8 random bytes
// in hex, matching ^[0-9a-z-]{8,64}$.
func NewRunID(now time.Time) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", pderr.Wrap(pderr.Internal, "runner: run id", err)
	}
	return strconv.FormatInt(now.Unix(), 36) + "-" + hex.EncodeToString(b[:]), nil
}

// PlanStore keeps plans the user confirmed outside a run (`plan` saves
// the preview; `switch --confirmed-plan <hash>` names it) under
// <RunDir>/plans/<hash>.json, mode 0600. Plans hold no contents (Op.Content
// is never serialized). Load verifies that the stored plan hashes to the
// requested hash and returns *pderr.Error Declined when it is missing or
// past ExpiresAt. Save prunes plans older than a day.
type PlanStore struct{ Dir string }

func (s PlanStore) dir() string { return filepath.Join(s.Dir, "plans") }

// Save stores p and returns its hash.
func (s PlanStore) Save(p *plan.Plan) (string, error) {
	if p == nil {
		return "", pderr.New(pderr.Internal, "plans", "nil plan")
	}
	if err := checkAbsClean(s.Dir); err != nil {
		return "", pderr.Wrap(pderr.Internal, "plans", err)
	}
	h := p.Hash()
	b, err := json.Marshal(p)
	if err != nil {
		return "", pderr.Wrap(pderr.Internal, "plans", err)
	}
	if err := os.MkdirAll(s.dir(), 0o700); err != nil {
		return "", pderr.Wrap(pderr.Internal, "plans", err)
	}
	s.prune(time.Now())
	if err := writeAtomic(filepath.Join(s.dir(), h+".json"), b); err != nil {
		return "", pderr.Wrap(pderr.Internal, "plans", err)
	}
	return h, nil
}

// prune removes plan files older than planRetention (best effort).
func (s PlanStore) prune(now time.Time) {
	entries, err := os.ReadDir(s.dir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || !e.Type().IsRegular() {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > planRetention {
			_ = os.Remove(filepath.Join(s.dir(), e.Name()))
		}
	}
}

// Load returns the stored plan with the given hash.
func (s PlanStore) Load(hash string, now time.Time) (*plan.Plan, error) {
	if !planHashRE.MatchString(hash) {
		return nil, pderr.New(pderr.Declined, "plans", "the confirmed plan hash is not valid")
	}
	b, err := os.ReadFile(filepath.Join(s.dir(), hash+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, pderr.New(pderr.Declined, "plans", "the confirmed plan was not found; run paritydesk plan again")
	}
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, "plans", err)
	}
	var p plan.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, pderr.New(pderr.Declined, "plans", "the confirmed plan is unreadable")
	}
	if p.Hash() != hash {
		return nil, pderr.New(pderr.Declined, "plans", "the confirmed plan does not match its hash")
	}
	if p.ExpiresAt.IsZero() || now.After(p.ExpiresAt) {
		return nil, pderr.New(pderr.Declined, "plans", "the confirmed plan expired; run paritydesk plan again")
	}
	return &p, nil
}
