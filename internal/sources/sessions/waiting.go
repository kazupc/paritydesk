package sessions

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
)

// waitingDir is the waiting-input directory inside the session directory.
const waitingDir = "waiting-input"

// WaitingInput is the adapter for row model.RowWaitingInput.
type WaitingInput struct{}

var _ sources.Adapter = WaitingInput{}

// NewWaitingInput returns the adapter.
func NewWaitingInput() sources.Adapter { return WaitingInput{} }

// Row implements sources.Adapter.
func (WaitingInput) Row() model.RowID { return model.RowWaitingInput }

// Read implements sources.Adapter. Items are the files under
// waiting-input/ by relative path; values are {"kind", "sha256"} where
// kind is the first subdirectory, or the extension for top-level files.
func (WaitingInput) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowWaitingInput)
	root := filepath.Join(sources.SessionsDir(rc.Folder, rc.Replica), waitingDir)
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.Type().IsRegular() || e.Name() == ".DS_Store" {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		frel := sources.SessionRel(rc.Replica, waitingDir+"/"+key)
		data, present, err := sources.ReadFile(rc.Folder, frel)
		if err != nil || !present {
			return err
		}
		it, err := sources.FileItem(key, data, kindOf(key), sources.FileMTimeMillis(rc.Folder, frel))
		if err != nil {
			return err
		}
		snap.Items[key] = it
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return snap, nil
	}
	if err != nil {
		return nil, err
	}
	return snap, nil
}

func kindOf(key string) string {
	if dir, _, ok := strings.Cut(key, "/"); ok {
		return dir
	}
	return strings.TrimPrefix(path.Ext(key), ".")
}

// Ops implements sources.Adapter: added or changed files are written from
// the source's bytes after a hash check (else skipped); files the desired
// snapshot lacks are quarantined.
func (a WaitingInput) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	var res sources.OpsResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	current, desired = sources.OrEmpty(current, model.RowWaitingInput), sources.OrEmpty(desired, model.RowWaitingInput)
	var cache *model.RowSnapshot
	for _, k := range unionKeys(current.Items, desired.Items) {
		c, hasC := current.Items[k]
		d, hasD := desired.Items[k]
		if hasC && hasD && c.Hash == d.Hash {
			continue
		}
		if strings.Contains(k, "..") {
			return res, errors.New("sessions: waiting-input key is not a clean path")
		}
		doc, err := sources.LoadFile(oc.Folder, sources.SessionRel(oc.Replica, waitingDir+"/"+k))
		if err != nil {
			return res, err
		}
		b := sources.NewBuilder(doc, model.RowWaitingInput)
		if !hasD {
			b.Remove(k, "")
			res.Edits = append(res.Edits, b.Edits()...)
			continue
		}
		src, err := sources.SourceItem(ctx, a, oc.Source, &cache, k, d.Hash)
		if err != nil {
			return res, err
		}
		if src == nil {
			res.Skipped = append(res.Skipped, sources.Skip(model.RowWaitingInput, k, sources.NoteSourceChanged))
			continue
		}
		b.Replace(src.Raw, k, 0, false)
		res.Edits = append(res.Edits, b.Edits()...)
	}
	return res, nil
}
