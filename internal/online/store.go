package online

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// Files and directories under <SupportDir>/online.
const (
	FileQueue   = "queue.json"
	FileCapture = "capture.json"
	FileLease   = "lease.json"
	FileArmed   = "armed"
	FileStop    = "stop"

	dirSteps    = "steps"
	dirResults  = "results"
	dirCaptures = "captures"
	dirInflight = "inflight"
	dirDenials  = "denials"
	dirShown    = "shown"
	dirTmp      = ".tmp"

	// DirName is the online folder under the support directory.
	DirName = "online"
	// LockName is the online lock file under <SupportDir>/locks.
	LockName = "online.lock"
)

// Size limits (4.1).
const (
	MaxQueueBytes  = 4 << 20
	MaxLeaseBytes  = 4 << 10
	MaxResultBytes = 256 << 10
	MaxExtBytes    = 32 << 20
	HeadPrefix     = 64 << 10
	MaxDenials     = 200
	// LockWait is how long CLI writers wait for the online lock.
	LockWait = 3 * time.Second
)

// Errors of the store.
var (
	// ErrExists: an exclusive create found the file already there.
	ErrExists = errors.New("online: file exists")
	// ErrTooLarge: a file is larger than its limit (treated as absent by
	// hooks, an error for CLI commands).
	ErrTooLarge = errors.New("online: file too large")
	// ErrNotRegular: a path component is a symlink or not of the expected
	// type.
	ErrNotRegular = errors.New("online: refusing a symlink or special file")
	// ErrBadName: an id that is not valid for a file name.
	ErrBadName = errors.New("online: invalid id")
)

// Store is the online folder. Reads go directly to the filesystem (never
// following a symlink); writes go through FS, so hooks get a
// create-only FS (CreateOnly) and read-only mode an fsx.ReadOnly one.
type Store struct {
	// Dir is <SupportDir>/online.
	Dir string
	FS  fsx.Ops
	// Lock takes the online lock (<SupportDir>/locks/online.lock) for up
	// to wait. nil: no locking (hooks never lock).
	Lock func(ctx context.Context, wait time.Duration) (release func(), err error)
	// Rand is the source of temp names and tokens (crypto/rand when nil).
	Rand io.Reader
	// BeforeRead, when set, runs before every filesystem read of the
	// store with the path relative to Dir (tests inject a slow store).
	BeforeRead func(rel string)
}

// beforeRead runs the BeforeRead hook.
func (s *Store) beforeRead(rel string) {
	if s.BeforeRead != nil {
		s.BeforeRead(rel)
	}
}

// NewStore returns a store over <supportDir>/online.
func NewStore(supportDir string, ops fsx.Ops) *Store {
	return &Store{Dir: filepath.Join(supportDir, DirName), FS: ops}
}

// Locked runs f under the online lock. A lock held elsewhere for longer
// than LockWait is RunLockBusy with online.err_busy.
func (s *Store) Locked(ctx context.Context, f func() error) error {
	if s.Lock == nil {
		return f()
	}
	release, err := s.Lock(ctx, LockWait)
	if err != nil {
		if pderr.CodeOf(err) == pderr.RunLockBusy {
			return pderr.New(pderr.RunLockBusy, "online", i18n.T(i18n.OnlineErrBusy))
		}
		return pderr.Wrap(pderr.Internal, "online lock", err)
	}
	defer release()
	return f()
}

// randHex returns n random bytes in hex.
func (s *Store) randHex(n int) (string, error) {
	b := make([]byte, n)
	r := s.Rand
	if r == nil {
		r = rand.Reader
	}
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("online: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// path joins rel under Dir after the containment check.
func (s *Store) path(rel string) (string, error) {
	return fsx.Contained(s.Dir, rel)
}

// checkDirs Lstats Dir and every directory between it and rel's parent:
// each that exists must be a real directory.
func (s *Store) checkDirs(rel string) error {
	if fi, err := os.Lstat(s.Dir); err == nil && !fi.IsDir() {
		return ErrNotRegular
	}
	parts := strings.Split(filepath.Dir(rel), string(filepath.Separator))
	p := s.Dir
	for _, part := range parts {
		if part == "." || part == "" {
			continue
		}
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return ErrNotRegular
		}
	}
	return nil
}

// read reads rel (a regular file, not followed through a symlink) of at
// most max bytes. A missing file is fs.ErrNotExist.
func (s *Store) read(rel string, max int64) ([]byte, error) {
	s.beforeRead(rel)
	p, err := s.path(rel)
	if err != nil {
		return nil, err
	}
	if err := s.checkDirs(rel); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if fi.Size() > max {
		return nil, ErrTooLarge
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrTooLarge
	}
	return b, nil
}

// readPrefix reads at most n bytes of rel.
func (s *Store) readPrefix(rel string, n int64) ([]byte, error) {
	s.beforeRead(rel)
	p, err := s.path(rel)
	if err != nil {
		return nil, err
	}
	if err := s.checkDirs(rel); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if fi.Size() > MaxQueueBytes {
		return nil, ErrTooLarge
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, n))
}

// exists reports whether rel exists as a regular file.
func (s *Store) exists(rel string) bool {
	s.beforeRead(rel)
	p, err := s.path(rel)
	if err != nil || s.checkDirs(rel) != nil {
		return false
	}
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// ensureDir creates Dir/rel (and Dir) with 0700 after the symlink checks.
func (s *Store) ensureDir(rel string) error {
	if err := s.checkDirs(filepath.Join(rel, "x")); err != nil {
		return err
	}
	p := s.Dir
	if rel != "" && rel != "." {
		var err error
		if p, err = s.path(rel); err != nil {
			return err
		}
	}
	return s.FS.MkdirAll(p, 0o700)
}

// write replaces rel atomically (0600).
func (s *Store) write(rel string, data []byte) error {
	if _, err := s.path(rel); err != nil {
		return err
	}
	if err := s.checkDirs(rel); err != nil {
		return err
	}
	if err := s.ensureDir(dirTmp); err != nil {
		return err
	}
	return s.FS.WriteFileAtomic(s.Dir, rel, data, 0o600, filepath.Join(s.Dir, dirTmp))
}

// createExcl creates rel with data unless it exists (ErrExists): a temp
// file in .tmp, then RenameExcl.
func (s *Store) createExcl(rel string, data []byte) error {
	target, err := s.path(rel)
	if err != nil {
		return err
	}
	if err := s.checkDirs(rel); err != nil {
		return err
	}
	if err := s.ensureDir(filepath.Dir(rel)); err != nil {
		return err
	}
	if err := s.ensureDir(dirTmp); err != nil {
		return err
	}
	name, err := s.randHex(8)
	if err != nil {
		return err
	}
	tmpRel := filepath.Join(dirTmp, name)
	if err := s.FS.WriteFileAtomic(s.Dir, tmpRel, data, 0o600, filepath.Join(s.Dir, dirTmp)); err != nil {
		return err
	}
	if err := s.FS.RenameExcl(filepath.Join(s.Dir, tmpRel), target); err != nil {
		_ = s.FS.RemoveAll(s.Dir, tmpRel) // refused for hooks; Cleanup removes it
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		return err
	}
	return nil
}

// remove removes rel (file or directory); a missing path is no error.
func (s *Store) remove(rel string) error {
	if _, err := s.path(rel); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(s.Dir, rel)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return s.FS.RemoveAll(s.Dir, rel)
}

// list returns the names in rel (nil when missing).
func (s *Store) list(rel string) []string {
	s.beforeRead(rel)
	p, err := s.path(rel)
	if err != nil || s.checkDirs(filepath.Join(rel, "x")) != nil {
		return nil
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.IsDir() {
		return nil
	}
	es, err := os.ReadDir(p)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// fileName returns the queue or capture file of a phase.
func fileName(p Phase) string {
	if p == PhaseCapture {
		return FileCapture
	}
	return FileQueue
}

// ReadQueue loads queue.json or capture.json; (nil, nil) when absent.
func (s *Store) ReadQueue(p Phase) (*Queue, error) {
	b, err := s.read(fileName(p), MaxQueueBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var q Queue
	if err := json.Unmarshal(b, &q); err != nil {
		return nil, fmt.Errorf("online: %s: %w", fileName(p), err)
	}
	return &q, nil
}

// ReadHead decodes the head of queue.json from a 64 KiB prefix; (nil,
// nil) when absent.
func (s *Store) ReadHead() (*QueueHead, error) {
	b, err := s.readPrefix(FileQueue, HeadPrefix)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return DecodeHead(b)
}

// DecodeHead decodes v, id, phase and state from the start of a queue
// document; the rest may be cut off.
func DecodeHead(b []byte) (*QueueHead, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("online: queue head: not an object")
	}
	var h QueueHead
	seen := 0
	for seen < 4 && dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("online: queue head: %w", err)
		}
		key, _ := kt.(string)
		switch key {
		case "v":
			err = dec.Decode(&h.V)
		case "id":
			err = dec.Decode(&h.ID)
		case "phase":
			err = dec.Decode(&h.Phase)
		case "state":
			err = dec.Decode(&h.State)
		default:
			return nil, fmt.Errorf("online: queue head: unexpected member %q", key)
		}
		if err != nil {
			return nil, fmt.Errorf("online: queue head: %w", err)
		}
		seen++
	}
	if seen < 4 || h.V != 1 {
		return nil, fmt.Errorf("online: queue head: incomplete")
	}
	return &h, nil
}

// WriteQueue replaces queue.json or capture.json.
func (s *Store) WriteQueue(q *Queue) error {
	b, err := json.Marshal(q)
	if err != nil {
		return err
	}
	if len(b) > MaxQueueBytes {
		return ErrTooLarge
	}
	return s.write(fileName(q.Phase), b)
}

// RemoveQueue removes queue.json or capture.json.
func (s *Store) RemoveQueue(p Phase) error { return s.remove(fileName(p)) }

// ReadLease loads lease.json; (nil, nil) when absent.
func (s *Store) ReadLease() (*Lease, error) {
	b, err := s.read(FileLease, MaxLeaseBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var l Lease
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("online: lease: %w", err)
	}
	return &l, nil
}

// WriteLease writes armed (the wall deadline in Unix seconds, for the
// shim) and then lease.json.
func (s *Store) WriteLease(l *Lease) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if err := s.write(FileArmed, []byte(strconv.FormatInt(l.ExpiresAt.Unix(), 10)+"\n")); err != nil {
		return err
	}
	return s.write(FileLease, b)
}

// RemoveLease removes armed before lease.json.
func (s *Store) RemoveLease() error {
	if err := s.remove(FileArmed); err != nil {
		return err
	}
	return s.remove(FileLease)
}

// RemoveArmed removes the shim marker only (emergency cancel, no lock).
func (s *Store) RemoveArmed() error { return s.remove(FileArmed) }

// ArmedUntil reads the shim's armed marker: whether it holds a wall
// deadline after now. A missing file is not armed, and so is one whose
// first line is not 1-12 digits (as the shim reads it); any other read
// error is returned (callers fail closed on it).
func (s *Store) ArmedUntil(now time.Time) (bool, error) {
	b, err := s.read(FileArmed, 64)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	if len(line) < 1 || len(line) > 12 || strings.Trim(line, "0123456789") != "" {
		return false, nil
	}
	d, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return false, nil
	}
	return now.Unix() < d, nil
}

// Stopped reports whether the stop marker exists.
func (s *Store) Stopped() bool { return s.exists(FileStop) }

// CreateStop creates the stop marker (no lock; an existing one is kept).
func (s *Store) CreateStop() error {
	err := s.createExcl(FileStop, []byte("stop\n"))
	if errors.Is(err, ErrExists) {
		return nil
	}
	return err
}

// RemoveStop removes the stop marker.
func (s *Store) RemoveStop() error { return s.remove(FileStop) }

// validQueueName accepts queue and capture ids for file names.
func validQueueName(id string) bool { return ValidQueueID(id) || ValidCaptureID(id) }

// CreateResult records a reconcile step's result (first wins: a second
// one is ErrExists).
func (s *Store) CreateResult(r Result) error {
	if !ValidQueueID(r.Queue) || !ValidStepID(string(r.Step)) {
		return ErrBadName
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.createExcl(filepath.Join(dirResults, r.Queue, string(r.Step)+".json"), b)
}

// RemoveResult removes a reconcile step's result (online retry, under the
// online lock, once the result was consumed).
func (s *Store) RemoveResult(queue string, step StepID) error {
	if !ValidQueueID(queue) || !ValidStepID(string(step)) {
		return ErrBadName
	}
	return s.remove(filepath.Join(dirResults, queue, string(step)+".json"))
}

// Results loads the recorded results of a queue; maxOf gives the size
// limit per step (nil: MaxResultBytes). Unreadable or oversized files
// count as absent.
func (s *Store) Results(queue string, maxOf func(StepID) int64) map[StepID]Result {
	out := map[StepID]Result{}
	if !ValidQueueID(queue) {
		return out
	}
	dir := filepath.Join(dirResults, queue)
	for _, name := range s.list(dir) {
		id := StepID(strings.TrimSuffix(name, ".json"))
		if !strings.HasSuffix(name, ".json") || !ValidStepID(string(id)) {
			continue
		}
		max := int64(MaxResultBytes)
		if maxOf != nil {
			max = maxOf(id)
		}
		b, err := s.read(filepath.Join(dir, name), max)
		if err != nil {
			continue
		}
		var r Result
		if json.Unmarshal(b, &r) != nil || r.Step != id || r.Queue != queue {
			continue
		}
		out[id] = r
	}
	return out
}

// sessionPart is the file-name part of a payload session id.
func sessionPart(session string) string {
	if ids.IsUUID(session) {
		return session
	}
	return "none"
}

// CreateCaptureResult records a capture step's result, keyed by the
// payload session.
func (s *Store) CreateCaptureResult(r Result) error {
	if !ValidCaptureID(r.Queue) || !ValidStepID(string(r.Step)) {
		return ErrBadName
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.createExcl(filepath.Join(dirCaptures, r.Queue, string(r.Step)+"."+sessionPart(r.Session)+".json"), b)
}

// CaptureResults loads a capture's results recorded for session only.
func (s *Store) CaptureResults(capture, session string, maxOf func(StepID) int64) map[StepID]Result {
	out := map[StepID]Result{}
	if !ValidCaptureID(capture) || !ids.IsUUID(session) {
		return out
	}
	dir := filepath.Join(dirCaptures, capture)
	suffix := "." + session + ".json"
	for _, name := range s.list(dir) {
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		id := StepID(strings.TrimSuffix(name, suffix))
		if !ValidStepID(string(id)) {
			continue
		}
		max := int64(MaxResultBytes)
		if maxOf != nil {
			max = maxOf(id)
		}
		b, err := s.read(filepath.Join(dir, name), max)
		if err != nil {
			continue
		}
		var r Result
		if json.Unmarshal(b, &r) != nil || r.Step != id || r.Queue != capture || r.Session != session {
			continue
		}
		out[id] = r
	}
	return out
}

// CreateInflight creates the in-flight marker <queue>/<step>.<n> (the
// gate of 8.3): ErrExists when a parallel identical call won.
func (s *Store) CreateInflight(queue string, step StepID, m Marker) error {
	if !validQueueName(queue) || !ValidStepID(string(step)) || m.N < 1 || m.N > 2 {
		return ErrBadName
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.createExcl(filepath.Join(dirInflight, queue, string(step)+"."+strconv.Itoa(m.N)), b)
}

// Inflight loads the in-flight markers of a queue per step, by n.
func (s *Store) Inflight(queue string) map[StepID][]Marker {
	out := map[StepID][]Marker{}
	if !validQueueName(queue) {
		return out
	}
	dir := filepath.Join(dirInflight, queue)
	for _, name := range s.list(dir) {
		i := strings.LastIndexByte(name, '.')
		if i < 0 {
			continue
		}
		id, n := StepID(name[:i]), name[i+1:]
		if !ValidStepID(string(id)) || (n != "1" && n != "2") {
			continue
		}
		b, err := s.read(filepath.Join(dir, name), MaxLeaseBytes)
		if err != nil {
			continue
		}
		var m Marker
		if json.Unmarshal(b, &m) != nil || strconv.Itoa(m.N) != n {
			continue
		}
		out[id] = append(out[id], m)
	}
	return out
}

// InflightCounts returns how many markers each step has.
func InflightCounts(m map[StepID][]Marker) map[StepID]int {
	out := make(map[StepID]int, len(m))
	for k, v := range m {
		out[k] = len(v)
	}
	return out
}

// ResetStepFiles removes a step's result and in-flight markers (a read
// offered again under a new lease).
func (s *Store) ResetStepFiles(queue string, step StepID) error {
	if !validQueueName(queue) || !ValidStepID(string(step)) {
		return ErrBadName
	}
	if err := s.remove(filepath.Join(dirResults, queue, string(step)+".json")); err != nil {
		return err
	}
	for _, n := range []string{"1", "2"} {
		if err := s.remove(filepath.Join(dirInflight, queue, string(step)+"."+n)); err != nil {
			return err
		}
	}
	return nil
}

// WriteStepInput stores a large step input under steps/<queue>/<step>.json
// (exclusive create; an identical existing file is accepted).
func (s *Store) WriteStepInput(queue string, step StepID, data []byte) error {
	if !ValidQueueID(queue) || !ValidStepID(string(step)) {
		return ErrBadName
	}
	rel := filepath.Join(dirSteps, queue, string(step)+".json")
	err := s.createExcl(rel, data)
	if errors.Is(err, ErrExists) {
		old, rerr := s.read(rel, MaxExtBytes)
		if rerr == nil && bytes.Equal(old, data) {
			return nil
		}
	}
	return err
}

// ReadStepInput loads a large step input and checks its hash.
func (s *Store) ReadStepInput(queue string, step Step, max int64) (json.RawMessage, error) {
	if !ValidQueueID(queue) || !ValidStepID(string(step.ID)) {
		return nil, ErrBadName
	}
	if max <= 0 || max > MaxExtBytes {
		max = MaxExtBytes
	}
	b, err := s.read(filepath.Join(dirSteps, queue, string(step.ID)+".json"), max)
	if err != nil {
		return nil, err
	}
	if InputSHA(b) != step.InputSHA256 {
		return nil, fmt.Errorf("online: step %s: input does not match its hash", step.ID)
	}
	return json.RawMessage(b), nil
}

// Denial is one guard denial record.
type Denial struct {
	At     time.Time `json:"at"`
	Tool   string    `json:"tool"`
	Reason string    `json:"reason"`
	Queue  string    `json:"queue,omitempty"`
}

// CreateDenial records a denial unless MaxDenials records exist; a
// failure never changes the decision, so errors are only returned.
func (s *Store) CreateDenial(d Denial, mono MonoNow) error {
	if len(s.list(dirDenials)) >= MaxDenials {
		return nil
	}
	if !ValidTool(d.Tool) && !guardedToolName(d.Tool) {
		d.Tool = ""
	}
	if !ValidQueueID(d.Queue) {
		d.Queue = ""
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	suffix, err := s.randHex(4)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%020d-%s.json", max(mono.NS, 0), suffix)
	return s.createExcl(filepath.Join(dirDenials, name), b)
}

// Denials returns the denial records, oldest first.
func (s *Store) Denials() []Denial {
	var out []Denial
	for _, name := range s.list(dirDenials) {
		b, err := s.read(filepath.Join(dirDenials, name), MaxLeaseBytes)
		if err != nil {
			continue
		}
		var d Denial
		if json.Unmarshal(b, &d) != nil {
			continue
		}
		if d.Tool != "" && !ValidTool(d.Tool) && !guardedToolName(d.Tool) {
			continue
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// CreateShown takes the first free SessionStart counter key.<n>, n =
// 1..max; false when every counter is taken or on any error.
func (s *Store) CreateShown(key string, maxShows int) bool {
	if !validShownKey(key) || maxShows < 1 || maxShows > 9 {
		return false
	}
	for n := 1; n <= maxShows; n++ {
		err := s.createExcl(filepath.Join(dirShown, key+"."+strconv.Itoa(n)), []byte("1\n"))
		switch {
		case err == nil:
			return true
		case errors.Is(err, ErrExists):
			continue
		default:
			return false
		}
	}
	return false
}

// validShownKey accepts queue ids and the artifacts unit's due-<seq>.
func validShownKey(key string) bool {
	if ValidQueueID(key) {
		return true
	}
	seq, ok := strings.CutPrefix(key, "due-")
	if !ok || seq == "" || len(seq) > 20 {
		return false
	}
	for _, c := range seq {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// CleanupInput says what Cleanup keeps.
type CleanupInput struct {
	// Queue is the id of the queue that stays valid ("" removes every
	// queue's files).
	Queue string
	// Capture is the id of capture.json that stays ("" removes all).
	Capture string
	Now     time.Time
	// CaptureMaxAge and QueueMaxAge bound capture directories (x6) and
	// denial records.
	CaptureMaxAge, QueueMaxAge time.Duration
}

// Cleanup removes the files of other queues and captures, old denial
// records, temp files older than a minute, and the stop marker when no
// queue is live. The caller holds the online lock.
func (s *Store) Cleanup(in CleanupInput) error {
	var errs []error
	keepDir := func(dir, keep string) {
		for _, name := range s.list(dir) {
			if name != keep {
				errs = append(errs, s.remove(filepath.Join(dir, name)))
			}
		}
	}
	keepDir(dirResults, in.Queue)
	keepDir(dirSteps, in.Queue)
	for _, name := range s.list(dirInflight) {
		if name != in.Queue && name != in.Capture {
			errs = append(errs, s.remove(filepath.Join(dirInflight, name)))
		}
	}
	for _, name := range s.list(dirShown) {
		key := name
		if i := strings.LastIndexByte(name, '.'); i > 0 {
			key = name[:i]
		}
		if key != in.Queue && !strings.HasPrefix(key, "due-") {
			errs = append(errs, s.remove(filepath.Join(dirShown, name)))
		}
	}
	for _, name := range s.list(dirCaptures) {
		if name == in.Capture {
			continue
		}
		p := filepath.Join(s.Dir, dirCaptures, name)
		fi, err := os.Lstat(p)
		if err != nil || in.Now.Sub(fi.ModTime()) > 6*in.CaptureMaxAge || in.Capture == "" {
			errs = append(errs, s.remove(filepath.Join(dirCaptures, name)))
		}
	}
	for _, name := range s.list(dirDenials) {
		fi, err := os.Lstat(filepath.Join(s.Dir, dirDenials, name))
		if err != nil || in.Now.Sub(fi.ModTime()) > in.QueueMaxAge {
			errs = append(errs, s.remove(filepath.Join(dirDenials, name)))
		}
	}
	for _, name := range s.list(dirTmp) {
		fi, err := os.Lstat(filepath.Join(s.Dir, dirTmp, name))
		if err != nil || in.Now.Sub(fi.ModTime()) > time.Minute {
			errs = append(errs, s.remove(filepath.Join(dirTmp, name)))
		}
	}
	if in.Queue == "" {
		errs = append(errs, s.RemoveStop())
	}
	return errors.Join(errs...)
}

// ErrLeaseNotUsable: the lease to rebind is missing, stale or unbound.
var ErrLeaseNotUsable = errors.New("online: no fresh bound lease to rebind")

// RebindLease moves a fresh, bound lease from one queue to the next
// (artifacts rounds, spec-artifacts A7): same session and token, a fresh
// deadline of ttl, armed rewritten. The caller holds the online lock.
func (s *Store) RebindLease(_ context.Context, fromQueue, toQueue string, now time.Time, mono MonoNow, ttl time.Duration) error {
	if !ValidQueueID(toQueue) {
		return ErrBadName
	}
	l, err := s.ReadLease()
	if err != nil || l == nil || l.Queue != fromQueue || l.Session == "" || !l.Fresh(now, mono, s.Stopped()) {
		return ErrLeaseNotUsable
	}
	l.Queue = toQueue
	l.BootID, l.MonoDeadlineNS, l.ExpiresAt = mono.BootID, mono.NS+int64(ttl), now.Add(ttl)
	return s.WriteLease(l)
}

// MarkRefused turns each offered step of queue without a result file into
// failed(user_refused) (spec-artifacts A15); other steps are unchanged.
// The caller holds the online lock.
func (s *Store) MarkRefused(_ context.Context, queue string, ids []StepID) error {
	q, err := s.ReadQueue(PhaseReconcile)
	if err != nil {
		return err
	}
	if q == nil || q.ID != queue {
		return nil
	}
	recorded := s.Results(queue, nil)
	changed := false
	for _, id := range ids {
		st := q.Step(id)
		if st == nil || st.Status != StatusOffered {
			continue
		}
		if _, ok := recorded[id]; ok {
			continue
		}
		st.Status, st.Reason = StatusFailed, "user_refused"
		changed = true
	}
	if !changed {
		return nil
	}
	return s.WriteQueue(q)
}

// ReleaseLease removes armed, then lease.json, when the lease belongs to
// queue (spec-artifacts A19); the queue keeps its state and results. The
// caller holds the online lock.
func (s *Store) ReleaseLease(_ context.Context, queue string) error {
	l, err := s.ReadLease()
	if err != nil || l == nil || l.Queue != queue {
		return err
	}
	return s.RemoveLease()
}
