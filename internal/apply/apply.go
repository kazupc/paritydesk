package apply

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

const (
	opApply = "apply"
	opCheck = "apply/check"
	// defaultMode is the mode of a new file whose op carries none.
	defaultMode fs.FileMode = 0o600
)

// LSDir returns the Local Storage LevelDB directory of a data folder.
func LSDir(folder string) string { return filepath.Join(folder, "Local Storage", "leveldb") }

// HolderCheck reports whether any process holds files or the LevelDB LOCK
// under folder. Implemented by the app package's quiescence check.
type HolderCheck func(ctx context.Context, folder string) (held bool, detail string, err error)

// Applier applies plans.
type Applier struct {
	FS       fsx.Ops
	KV       kv.Opener
	Holders  HolderCheck
	ReadOnly bool
}

// Input is one apply.
type Input struct {
	Plan *plan.Plan
	// Folder is the entering folder (parked for a switch, Claude/ for sync).
	Folder string
	// TmpDir is <data_root>/.paritydesk-tmp/<run>/.
	TmpDir string
	// QuarantineDir is the generation's quarantine directory.
	QuarantineDir string
}

// Result reports what was applied.
type Result struct {
	Applied int `json:"applied"`
	// Touched reports whether the folder may have changed: a file write
	// or quarantine was attempted, or Local Storage was opened writable.
	// When it is false after an error, nothing in the folder changed and
	// there is nothing to restore.
	Touched bool `json:"touched"`
	// LSOpened reports whether Local Storage was opened writable (opening
	// alone may rewrite LevelDB's log and manifest).
	LSOpened bool `json:"ls_opened"`
	// PostHashes maps "path:<p>" and "ls:<key>" to the hashes read back.
	PostHashes map[string]string `json:"post_hashes"`
}

// pathKey and lsKey are the Result.PostHashes and Check keys.
func pathKey(p string) string  { return "path:" + p }
func lsKey(name string) string { return "ls:" + name }

// isFileOp reports whether op targets a file (as opposed to Local Storage).
func isFileOp(op plan.Op) bool { return op.Kind == plan.OpWriteFile || op.Kind == plan.OpQuarantine }

// run is the state of one Apply call.
type run struct {
	a       *Applier
	in      Input
	w       kv.Writer
	res     Result
	written []string // file paths written or quarantined, in order
}

// Apply executes in.Plan. It stops at the first error.
//
// Errors before the first write are code Internal (nothing changed);
// every error from the first write on is code ApplyRolledBack with op
// "apply", and Result reports the ops already applied, so the orchestrator
// restores the folder from the generation. Errors never contain content.
func (a *Applier) Apply(ctx context.Context, in Input) (Result, error) {
	r := &run{a: a, in: in, res: Result{PostHashes: map[string]string{}}}
	if err := r.preflight(ctx); err != nil {
		return r.res, err
	}
	defer r.closeWriter()
	if err := r.verify(ctx); err != nil {
		return r.res, err
	}
	if err := ctx.Err(); err != nil {
		return r.res, pderr.Wrap(pderr.Internal, opApply, err)
	}
	if err := r.writeFiles(); err != nil {
		return r.res, err
	}
	if err := r.writeLS(ctx); err != nil {
		return r.res, err
	}
	if err := a.FS.FullSync(in.Folder); err != nil {
		return r.res, r.rolledBack(err, "full sync of the folder failed")
	}
	return r.res, nil
}

// preflight refuses read-only mode, a missing plan, and a held folder.
func (r *run) preflight(ctx context.Context) error {
	if r.a.ReadOnly {
		return pderr.Wrap(pderr.Internal, opApply, fsx.ErrReadOnly)
	}
	if r.in.Plan == nil || r.in.Folder == "" || r.a.FS == nil {
		return pderr.New(pderr.Internal, opApply, "incomplete apply input")
	}
	if r.a.Holders == nil {
		return pderr.New(pderr.Internal, opApply, "no holder check configured")
	}
	held, detail, err := r.a.Holders(ctx, r.in.Folder)
	if err != nil {
		return &pderr.Error{Code: pderr.Internal, Op: opApply, Msg: "holder check failed", Err: err}
	}
	if held {
		return pderr.New(pderr.Internal, opApply, "the folder is in use: "+detail)
	}
	return nil
}

// verify checks every op's pre-hash before anything is written, opening
// Local Storage writable when the plan has LS ops.
func (r *run) verify(ctx context.Context) error {
	var lsOps []plan.Op
	for i, op := range r.in.Plan.Ops {
		switch {
		case isFileOp(op):
			if err := r.checkFilePre(op); err != nil {
				return err
			}
		case op.Kind == plan.OpLSPut || op.Kind == plan.OpLSDelete:
			lsOps = append(lsOps, op)
		default:
			return pderr.New(pderr.Internal, opApply, fmt.Sprintf("op %d has unknown kind %q", i, op.Kind))
		}
	}
	if len(lsOps) == 0 {
		return nil
	}
	if r.a.KV == nil {
		return pderr.New(pderr.Internal, opApply, "no Local Storage opener configured")
	}
	w, err := r.a.KV.OpenWritable(ctx, LSDir(r.in.Folder))
	if err != nil {
		return &pderr.Error{Code: pderr.Internal, Op: opApply, Msg: openLSMsg(err), Err: err}
	}
	r.w = w
	r.res.Touched, r.res.LSOpened = true, true
	for _, op := range lsOps {
		got, err := lsHash(w, op.Key)
		if err != nil {
			return &pderr.Error{Code: pderr.Internal, Op: opApply, Msg: "reading " + lsKey(op.Key) + " failed", Err: err}
		}
		if got != op.PreHash {
			return pderr.New(pderr.Internal, opApply, lsKey(op.Key)+" changed since the plan was made")
		}
	}
	return nil
}

// openLSMsg says why OpenWritable refused Local Storage. Each refusal
// it names comes before anything is written, so the folder is untouched.
func openLSMsg(err error) string {
	switch {
	case errors.Is(err, kv.ErrLocked):
		return "the folder is in use: another process holds the Local Storage lock"
	case errors.Is(err, kv.ErrUnknownCompression):
		return "Local Storage uses an unknown block compression"
	case errors.Is(err, kv.ErrJournalSequence):
		return "Local Storage holds journal batches out of order"
	}
	return "opening Local Storage failed"
}

// checkFilePre compares a file's current hash with the op's pre-hash.
func (r *run) checkFilePre(op plan.Op) error {
	got, _, err := fileHash(r.in.Folder, op.Path)
	if err != nil {
		return &pderr.Error{Code: pderr.Internal, Op: opApply, Msg: "reading " + pathKey(op.Path) + " failed", Err: err}
	}
	if got != op.PreHash {
		return pderr.New(pderr.Internal, opApply, pathKey(op.Path)+" changed since the plan was made")
	}
	return nil
}

// writeFiles writes or quarantines every file op in order, re-checking
// each pre-hash just before its write and reading each result back.
func (r *run) writeFiles() error {
	for _, op := range r.in.Plan.Ops {
		if !isFileOp(op) {
			continue
		}
		if err := r.writeFile(op); err != nil {
			return err
		}
		r.written = append(r.written, op.Path)
		r.res.Applied++
	}
	return nil
}

// writeFile applies one file op and records its read-back hash.
func (r *run) writeFile(op plan.Op) error {
	if err := r.checkFilePre(op); err != nil {
		if len(r.written) == 0 {
			return err
		}
		return r.rolledBack(err, pathKey(op.Path)+" changed during apply")
	}
	r.res.Touched = true
	var err error
	switch op.Kind {
	case plan.OpWriteFile:
		mode, merr := r.modeFor(op)
		if merr != nil {
			return r.rolledBack(merr, "reading the mode of "+pathKey(op.Path)+" failed")
		}
		err = r.a.FS.WriteFileAtomic(r.in.Folder, op.Path, op.Content, mode, r.in.TmpDir)
	case plan.OpQuarantine:
		err = r.a.FS.MoveInto(r.in.Folder, op.Path, r.in.QuarantineDir)
	}
	if err != nil {
		return r.rolledBack(err, "writing "+pathKey(op.Path)+" failed")
	}
	got, _, err := fileHash(r.in.Folder, op.Path)
	if err != nil {
		return r.rolledBack(err, "reading back "+pathKey(op.Path)+" failed")
	}
	r.res.PostHashes[pathKey(op.Path)] = got
	if got != op.PostHash {
		return r.rolledBack(nil, "read-back mismatch for "+pathKey(op.Path))
	}
	return nil
}

// modeFor returns op.Mode, else the existing file's mode, else 0600.
func (r *run) modeFor(op plan.Op) (fs.FileMode, error) {
	if op.Mode != 0 {
		return op.Mode.Perm(), nil
	}
	_, mode, err := fileHash(r.in.Folder, op.Path)
	if err != nil {
		return 0, err
	}
	if mode != 0 {
		return mode, nil
	}
	return defaultMode, nil
}

// writeLS writes every LS op in one batch, closes the store, and reads
// every key back through a snapshot.
func (r *run) writeLS(ctx context.Context) error {
	if r.w == nil {
		return nil
	}
	var b kv.Batch
	var ops []plan.Op
	for _, op := range r.in.Plan.Ops {
		if isFileOp(op) {
			continue
		}
		key, err := localstorage.Key(op.Key)
		if err != nil {
			return r.rolledBack(err, "encoding "+lsKey(op.Key)+" failed")
		}
		if op.Kind == plan.OpLSDelete {
			b.Delete(key)
		} else {
			b.Put(key, op.Content)
		}
		ops = append(ops, op)
	}
	if err := r.w.Write(&b); err != nil {
		return r.rolledBack(err, "Local Storage write failed")
	}
	r.res.Applied += len(ops)
	w := r.w
	r.w = nil
	if err := w.Close(); err != nil {
		return r.rolledBack(err, "closing Local Storage failed")
	}
	return r.readBackLS(ctx, ops)
}

// readBackLS verifies the written keys through a fresh snapshot.
func (r *run) readBackLS(ctx context.Context, ops []plan.Op) error {
	snap, err := r.a.KV.OpenSnapshot(ctx, LSDir(r.in.Folder))
	if err != nil {
		return r.rolledBack(err, "reopening Local Storage failed")
	}
	defer snap.Close()
	for _, op := range ops {
		got, err := lsHash(snap, op.Key)
		if err != nil {
			return r.rolledBack(err, "reading back "+lsKey(op.Key)+" failed")
		}
		r.res.PostHashes[lsKey(op.Key)] = got
		if got != op.PostHash {
			return r.rolledBack(nil, "read-back mismatch for "+lsKey(op.Key))
		}
	}
	return nil
}

// closeWriter closes the writable store if it is still open.
func (r *run) closeWriter() {
	if r.w != nil {
		_ = r.w.Close()
		r.w = nil
	}
}

// rolledBack returns the ApplyRolledBack error for a failure after the
// first write, naming the files already written.
func (r *run) rolledBack(cause error, msg string) *pderr.Error {
	if len(r.written) > 0 {
		msg += fmt.Sprintf(" (files already written: %s)", strings.Join(r.written, ", "))
	} else {
		msg += " (no files written yet)"
	}
	return &pderr.Error{Code: pderr.ApplyRolledBack, Op: opApply, Msg: msg, Err: cause}
}

// Check compares the folder against the plan's post-hashes without
// writing. recover uses it to decide whether T's bases can be committed.
// mismatched lists "path:<p>" and "ls:<name>" entries whose current hash
// differs from the op's PostHash ("" for removals).
func (a *Applier) Check(ctx context.Context, p *plan.Plan, folder string) (matches bool, mismatched []string, err error) {
	if p == nil || folder == "" {
		return false, nil, pderr.New(pderr.Internal, opCheck, "incomplete check input")
	}
	snap, err := a.openSnapshotIfNeeded(ctx, p, folder)
	if err != nil {
		return false, nil, err
	}
	if snap != nil {
		defer snap.Close()
	}
	for _, op := range p.Ops {
		got, key, err := currentHash(snap, folder, op)
		if err != nil {
			return false, nil, err
		}
		if got != op.PostHash {
			mismatched = append(mismatched, key)
		}
	}
	return len(mismatched) == 0, mismatched, nil
}

// openSnapshotIfNeeded opens a Local Storage snapshot when p has LS ops.
func (a *Applier) openSnapshotIfNeeded(ctx context.Context, p *plan.Plan, folder string) (kv.Reader, error) {
	for _, op := range p.Ops {
		if isFileOp(op) {
			continue
		}
		if a.KV == nil {
			return nil, pderr.New(pderr.Internal, opCheck, "no Local Storage opener configured")
		}
		snap, err := a.KV.OpenSnapshot(ctx, LSDir(folder))
		if err != nil {
			return nil, &pderr.Error{Code: pderr.Internal, Op: opCheck, Msg: "opening Local Storage failed", Err: err}
		}
		return snap, nil
	}
	return nil, nil
}

// currentHash returns the current hash of op's target and its result key.
func currentHash(snap kv.Reader, folder string, op plan.Op) (string, string, error) {
	switch {
	case isFileOp(op):
		got, _, err := fileHash(folder, op.Path)
		if err != nil {
			return "", "", &pderr.Error{Code: pderr.Internal, Op: opCheck, Msg: "reading " + pathKey(op.Path) + " failed", Err: err}
		}
		return got, pathKey(op.Path), nil
	case op.Kind == plan.OpLSPut || op.Kind == plan.OpLSDelete:
		got, err := lsHash(snap, op.Key)
		if err != nil {
			return "", "", &pderr.Error{Code: pderr.Internal, Op: opCheck, Msg: "reading " + lsKey(op.Key) + " failed", Err: err}
		}
		return got, lsKey(op.Key), nil
	}
	return "", "", pderr.New(pderr.Internal, opCheck, fmt.Sprintf("unknown op kind %q", op.Kind))
}

// errNotRegular is returned for a target that is a symlink, directory or
// other non-regular file, or that has a symlink among its parents.
var errNotRegular = errors.New("apply: target is not a regular file")

// fileHash returns the sha256 of folder/rel ("" when absent) and its
// permission bits (0 when absent). It refuses symlinks in any component
// and non-regular targets, and never follows a symlink.
func fileHash(folder, rel string) (string, fs.FileMode, error) {
	target, err := fsx.Contained(folder, rel)
	if err != nil {
		return "", 0, err
	}
	cur := folder
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return "", 0, nil
		}
		if err != nil {
			return "", 0, err
		}
		last := i == len(parts)-1
		if fi.Mode()&fs.ModeSymlink != 0 || (!last && !fi.IsDir()) || (last && !fi.Mode().IsRegular()) {
			return "", 0, errNotRegular
		}
		if last {
			data, err := os.ReadFile(target)
			if err != nil {
				return "", 0, err
			}
			return model.HashBytes(data), fi.Mode().Perm(), nil
		}
	}
	return "", 0, errNotRegular
}

// lsHash returns the sha256 of an LS item's encoded value ("" when absent).
func lsHash(rd kv.Reader, name string) (string, error) {
	key, err := localstorage.Key(name)
	if err != nil {
		return "", err
	}
	v, found, err := rd.Get(key)
	if err != nil || !found {
		return "", err
	}
	return model.HashBytes(v), nil
}
