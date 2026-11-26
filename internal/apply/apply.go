package apply

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/i18n"
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
	// AfterFirstWrite, if non-nil, is called once per Apply, right after
	// the first write has landed in the folder: the first file op written
	// and read back as planned, or, for a plan with only Local Storage
	// ops, the batch written and the store closed. It is the end-to-end
	// crash test's hook (PARITYDESK_FAULT=pause:P7, internal/cli); it is
	// nil otherwise.
	AfterFirstWrite func()
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
	// LeavingFolder is the leaving folder at apply time: the source of
	// row scratch's copies from the leaving side. A plan with such a copy
	// and no LeavingFolder fails before the first write.
	LeavingFolder string
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
	// PostHashes maps "path:<p>", "dir:<p>", "link:<p>" and "ls:<key>" to
	// the hashes read back.
	PostHashes map[string]string `json:"post_hashes"`
	// SoftSkipped lists the data-folder-relative paths of soft copies
	// (plan.SoftCopy) whose source changed after the plan: the target was
	// left as it was (spec-scratch 3.3).
	SoftSkipped []string `json:"soft_skipped,omitempty"`
}

// pathKey and lsKey are the Result.PostHashes and Check keys; dirKey and
// linkKey name the targets of OpMkdir and OpSymlink.
func pathKey(p string) string  { return "path:" + p }
func lsKey(name string) string { return "ls:" + name }
func dirKey(p string) string   { return "dir:" + p }
func linkKey(p string) string  { return "link:" + p }

// PostHashKey names an op's target the way Result.PostHashes does.
func PostHashKey(op plan.Op) string {
	switch op.Kind {
	case plan.OpMkdir:
		return dirKey(op.Path)
	case plan.OpSymlink:
		return linkKey(op.Path)
	case plan.OpLSPut, plan.OpLSDelete:
		return lsKey(op.Key)
	}
	return pathKey(op.Path)
}

// isFileOp reports whether op targets a path in the folder (as opposed
// to Local Storage): files, quarantines and row scratch's copies,
// directories and symlinks.
func isFileOp(op plan.Op) bool {
	switch op.Kind {
	case plan.OpWriteFile, plan.OpQuarantine, plan.OpCopyFile, plan.OpMkdir, plan.OpSymlink:
		return true
	}
	return false
}

// run is the state of one Apply call.
type run struct {
	a       *Applier
	in      Input
	w       kv.Writer
	res     Result
	written []string // file paths written or quarantined, in order
	hooked  bool     // AfterFirstWrite was called
	// soft marks soft copies whose source already failed the preflight.
	soft map[string]bool
}

// firstWrite calls the AfterFirstWrite hook once.
func (r *run) firstWrite() {
	if r.hooked || r.a.AfterFirstWrite == nil {
		return
	}
	r.hooked = true
	r.a.AfterFirstWrite()
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
		return r.res, r.rolledBack(err, i18n.Tx(i18n.ErrApplyFullSync))
	}
	return r.res, nil
}

// preflight refuses read-only mode, a missing plan, a leaving copy
// without the leaving folder, and a held folder.
func (r *run) preflight(ctx context.Context) error {
	if r.a.ReadOnly {
		return pderr.Wrap(pderr.Internal, opApply, fsx.ErrReadOnly)
	}
	if r.in.Plan == nil || r.in.Folder == "" || r.a.FS == nil {
		return pderr.Bug(opApply, "incomplete apply input")
	}
	for _, op := range r.in.Plan.Ops {
		if op.Kind == plan.OpCopyFile && (op.Copy == nil || (op.Copy.Side == plan.SideLeaving && r.in.LeavingFolder == "")) {
			return pderr.Bug(opApply, "a copy from the leaving folder needs that folder")
		}
	}
	if r.a.Holders == nil {
		return pderr.Bug(opApply, "no holder check configured")
	}
	held, detail, err := r.a.Holders(ctx, r.in.Folder)
	if err != nil {
		return pderr.WrapT(pderr.Internal, opApply, i18n.ErrApplyHolderCheck, err)
	}
	if held {
		return pderr.NewT(pderr.Internal, opApply, i18n.ErrApplyInUse, detail)
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
			if err := r.checkCopySource(op); err != nil {
				return err
			}
		case op.Kind == plan.OpLSPut || op.Kind == plan.OpLSDelete:
			lsOps = append(lsOps, op)
		default:
			return pderr.Bugf(opApply, "op %d has unknown kind %q", i, op.Kind)
		}
	}
	if len(lsOps) == 0 {
		return nil
	}
	if r.a.KV == nil {
		return pderr.Bug(opApply, "no Local Storage opener configured")
	}
	w, err := r.a.KV.OpenWritable(ctx, LSDir(r.in.Folder))
	if err != nil {
		return pderr.WrapT(pderr.Internal, opApply, openLSMsg(err), err)
	}
	r.w = w
	r.res.Touched, r.res.LSOpened = true, true
	for _, op := range lsOps {
		got, err := lsHash(w, op.Key)
		if err != nil {
			return pderr.WrapT(pderr.Internal, opApply, i18n.ErrApplyReadFailed, err, lsKey(op.Key))
		}
		if got != op.PreHash {
			return pderr.NewT(pderr.Internal, opApply, i18n.ErrApplyChanged, lsKey(op.Key))
		}
	}
	return nil
}

// openLSMsg says why OpenWritable refused Local Storage. Each refusal
// it names comes before anything is written, so the folder is untouched.
func openLSMsg(err error) i18n.Msg {
	switch {
	case errors.Is(err, kv.ErrLocked):
		return i18n.ErrApplyLSLocked
	case errors.Is(err, kv.ErrUnknownCompression):
		return i18n.ErrApplyLSCompression
	case errors.Is(err, kv.ErrJournalSequence):
		return i18n.ErrApplyLSJournal
	}
	return i18n.ErrApplyLSOpen
}

// checkFilePre compares a target's current hash with the op's pre-hash.
func (r *run) checkFilePre(op plan.Op) error {
	got, err := targetHash(r.in.Folder, op)
	if err != nil {
		return pderr.WrapT(pderr.Internal, opApply, i18n.ErrApplyReadFailed, err, PostHashKey(op))
	}
	if got != op.PreHash {
		return pderr.NewT(pderr.Internal, opApply, i18n.ErrApplyChanged, PostHashKey(op))
	}
	return nil
}

// sourcePath returns the absolute path of a copy's source.
func (r *run) sourcePath(op plan.Op) (string, error) {
	folder := r.in.Folder
	if op.Copy.Side == plan.SideLeaving {
		folder = r.in.LeavingFolder
	}
	return fsx.Contained(folder, op.Copy.Path)
}

// checkCopySource checks that a copy's source is still a regular file. A
// soft copy whose source fails is marked soft-skipped instead.
func (r *run) checkCopySource(op plan.Op) error {
	if op.Kind != plan.OpCopyFile {
		return nil
	}
	src, err := r.sourcePath(op)
	if err != nil {
		return pderr.Wrap(pderr.Internal, opApply, err)
	}
	fi, err := os.Lstat(src)
	if err == nil && fi.Mode().IsRegular() && noSymlinkParents(r.sourceFolder(op), op.Copy.Path) {
		return nil
	}
	if plan.SoftCopy(op) {
		if r.soft == nil {
			r.soft = map[string]bool{}
		}
		r.soft[op.Path] = true
		return nil
	}
	return pderr.NewT(pderr.Internal, opApply, i18n.ErrApplyCopySourceChanged, pathKey(op.Path))
}

// sourceFolder returns the folder a copy reads from.
func (r *run) sourceFolder(op plan.Op) string {
	if op.Copy.Side == plan.SideLeaving {
		return r.in.LeavingFolder
	}
	return r.in.Folder
}

// noSymlinkParents reports whether no parent component of rel under
// folder is a symlink or a non-directory.
func noSymlinkParents(folder, rel string) bool {
	cur := folder
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// writeFiles writes or quarantines every file op in order, re-checking
// each pre-hash just before its write and reading each result back. After
// the last row-scratch op the folder gets one full sync.
func (r *run) writeFiles() error {
	last := -1
	for i, op := range r.in.Plan.Ops {
		if op.Row == model.RowScratch {
			last = i
		}
	}
	for i, op := range r.in.Plan.Ops {
		if !isFileOp(op) {
			continue
		}
		if err := r.writeFile(op); err != nil {
			return err
		}
		r.written = append(r.written, op.Path)
		r.res.Applied++
		r.firstWrite()
		if i == last {
			if err := r.a.FS.FullSync(r.in.Folder); err != nil {
				return r.rolledBack(err, i18n.Tx(i18n.ErrApplyFullSync))
			}
		}
	}
	return nil
}

// writeFile applies one file op and records its read-back hash.
func (r *run) writeFile(op plan.Op) error {
	if err := r.checkFilePre(op); err != nil {
		if len(r.written) == 0 {
			return err
		}
		return r.rolledBack(err, i18n.Tx(i18n.ErrApplyChangedDuring, pathKey(op.Path)))
	}
	key := PostHashKey(op)
	want := op.PostHash
	if r.soft[op.Path] {
		r.res.SoftSkipped = append(r.res.SoftSkipped, op.Path)
		want = op.PreHash
	} else {
		r.res.Touched = true
		var err error
		switch op.Kind {
		case plan.OpWriteFile:
			mode, merr := r.modeFor(op)
			if merr != nil {
				return r.rolledBack(merr, i18n.Tx(i18n.ErrApplyModeFailed, pathKey(op.Path)))
			}
			err = r.a.FS.WriteFileAtomic(r.in.Folder, op.Path, op.Content, mode, r.in.TmpDir)
		case plan.OpQuarantine:
			err = r.a.FS.MoveInto(r.in.Folder, op.Path, r.in.QuarantineDir)
		case plan.OpMkdir:
			err = r.a.FS.Mkdir(r.in.Folder, op.Path, op.Mode.Perm())
		case plan.OpSymlink:
			err = r.a.FS.Symlink(op.Link, r.in.Folder, op.Path)
		case plan.OpCopyFile:
			err = r.copyFile(op)
			if err != nil && plan.SoftCopy(op) && (errors.Is(err, fsx.ErrHashMismatch) || errors.Is(err, fsx.ErrSourceChanged)) {
				r.res.SoftSkipped = append(r.res.SoftSkipped, op.Path)
				want, err = op.PreHash, nil
			}
		}
		if err != nil {
			return r.rolledBack(err, i18n.Tx(i18n.ErrApplyWriteFailed, key))
		}
	}
	got, err := targetHash(r.in.Folder, op)
	if err != nil {
		return r.rolledBack(err, i18n.Tx(i18n.ErrApplyReadBackFailed, key))
	}
	r.res.PostHashes[key] = got
	if got != want {
		return r.rolledBack(nil, i18n.Tx(i18n.ErrApplyReadBackMismatch, key))
	}
	return nil
}

// copyFile clones a copy's source into place, verified against its hash.
func (r *run) copyFile(op plan.Op) error {
	src, err := r.sourcePath(op)
	if err != nil {
		return err
	}
	if !noSymlinkParents(r.sourceFolder(op), op.Copy.Path) {
		return fsx.ErrSourceChanged
	}
	mode := op.Mode.Perm()
	if mode == 0 {
		mode = defaultMode
	}
	return r.a.FS.CloneFileAtomic(src, r.in.Folder, op.Path, mode, time.Unix(0, op.Copy.MTimeNs), op.Copy.Hash, r.in.TmpDir)
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
			return r.rolledBack(err, i18n.Tx(i18n.ErrApplyEncodeFailed, lsKey(op.Key)))
		}
		if op.Kind == plan.OpLSDelete {
			b.Delete(key)
		} else {
			b.Put(key, op.Content)
		}
		ops = append(ops, op)
	}
	if err := r.w.Write(&b); err != nil {
		return r.rolledBack(err, i18n.Tx(i18n.ErrApplyLSWrite))
	}
	r.res.Applied += len(ops)
	w := r.w
	r.w = nil
	if err := w.Close(); err != nil {
		return r.rolledBack(err, i18n.Tx(i18n.ErrApplyLSClose))
	}
	r.firstWrite()
	return r.readBackLS(ctx, ops)
}

// readBackLS verifies the written keys through a fresh snapshot.
func (r *run) readBackLS(ctx context.Context, ops []plan.Op) error {
	snap, err := r.a.KV.OpenSnapshot(ctx, LSDir(r.in.Folder))
	if err != nil {
		return r.rolledBack(err, i18n.Tx(i18n.ErrApplyLSReopen))
	}
	defer snap.Close()
	for _, op := range ops {
		got, err := lsHash(snap, op.Key)
		if err != nil {
			return r.rolledBack(err, i18n.Tx(i18n.ErrApplyReadBackFailed, lsKey(op.Key)))
		}
		r.res.PostHashes[lsKey(op.Key)] = got
		if got != op.PostHash {
			return r.rolledBack(nil, i18n.Tx(i18n.ErrApplyReadBackMismatch, lsKey(op.Key)))
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
func (r *run) rolledBack(cause error, msg i18n.Text) *pderr.Error {
	if len(r.written) > 0 {
		return pderr.WrapT(pderr.ApplyRolledBack, opApply, i18n.ErrApplyRolledBackWritten, cause, msg, strings.Join(r.written, ", "))
	}
	return pderr.WrapT(pderr.ApplyRolledBack, opApply, i18n.ErrApplyRolledBackNone, cause, msg)
}

// Check compares the folder against the plan's post-hashes without
// writing. recover uses it to decide whether T's bases can be committed.
// mismatched lists "path:<p>" and "ls:<name>" entries whose current hash
// differs from the op's PostHash ("" for removals).
func (a *Applier) Check(ctx context.Context, p *plan.Plan, folder string) (matches bool, mismatched []string, err error) {
	if p == nil || folder == "" {
		return false, nil, pderr.Bug(opCheck, "incomplete check input")
	}
	snap, err := a.openSnapshotIfNeeded(ctx, p, folder)
	if err != nil {
		return false, nil, err
	}
	if snap != nil {
		defer snap.Close()
	}
	for _, op := range p.Ops {
		got, key, err := CurrentHash(snap, folder, op)
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
			return nil, pderr.Bug(opCheck, "no Local Storage opener configured")
		}
		snap, err := a.KV.OpenSnapshot(ctx, LSDir(folder))
		if err != nil {
			return nil, pderr.WrapT(pderr.Internal, opCheck, i18n.ErrApplyLSOpen, err)
		}
		return snap, nil
	}
	return nil, nil
}

// CurrentHash returns the current hash of op's target and its result
// key: a file's sha256, a directory's DirHash, a symlink's LinkHash, an
// LS value's hash ("" when absent). snap is needed for LS ops only.
func CurrentHash(snap kv.Reader, folder string, op plan.Op) (string, string, error) {
	switch {
	case isFileOp(op):
		got, err := targetHash(folder, op)
		if err != nil {
			return "", "", pderr.WrapT(pderr.Internal, opCheck, i18n.ErrApplyReadFailed, err, PostHashKey(op))
		}
		return got, PostHashKey(op), nil
	case op.Kind == plan.OpLSPut || op.Kind == plan.OpLSDelete:
		got, err := lsHash(snap, op.Key)
		if err != nil {
			return "", "", pderr.WrapT(pderr.Internal, opCheck, i18n.ErrApplyReadFailed, err, lsKey(op.Key))
		}
		return got, lsKey(op.Key), nil
	}
	return "", "", pderr.Bugf(opCheck, "unknown op kind %q", op.Kind)
}

// errNotRegular is returned for a target that is a symlink, directory or
// other non-regular file, or that has a symlink among its parents.
var errNotRegular = errors.New("apply: target is not a regular file")

// targetHash returns the current hash of an op's target by kind: an
// OpMkdir's target must be a directory (DirHash of its mode), an
// OpSymlink's a symlink (LinkHash of its target), every other file op's a
// regular file (sha256); "" when absent. Any other type is an error, so a
// path never changes kind between the pre and post checks.
func targetHash(folder string, op plan.Op) (string, error) {
	switch op.Kind {
	case plan.OpMkdir, plan.OpSymlink:
		fi, err := lstatNoSymlinkParents(folder, op.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", nil
		case err != nil:
			return "", err
		case op.Kind == plan.OpMkdir && fi.IsDir():
			return plan.DirHash(fi.Mode().Perm()), nil
		case op.Kind == plan.OpSymlink && fi.Mode()&fs.ModeSymlink != 0:
			p, _ := fsx.Contained(folder, op.Path)
			target, err := os.Readlink(p)
			if err != nil {
				return "", err
			}
			return plan.LinkHash(target), nil
		}
		return "", errNotRegular
	}
	h, _, err := fileHash(folder, op.Path)
	return h, err
}

// lstatNoSymlinkParents lstats folder/rel, refusing a symlink or a
// non-directory among its parents.
func lstatNoSymlinkParents(folder, rel string) (fs.FileInfo, error) {
	target, err := fsx.Contained(folder, rel)
	if err != nil {
		return nil, err
	}
	cur := folder
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			return nil, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return nil, errNotRegular
		}
	}
	return os.Lstat(target)
}

// fileHash returns the sha256 of folder/rel ("" when absent) and its
// permission bits (0 when absent), streamed (fsx.HashFile). It refuses
// symlinks in any component and non-regular targets, and never follows a
// symlink.
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
			h, mode, err := fsx.HashFile(target)
			if err != nil {
				return "", 0, err
			}
			return h, mode, nil
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
