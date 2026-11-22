package windowstate

import (
	"context"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
)

// FileName is the window-state file inside a data folder; it is also the
// row's only item key.
const FileName = "window-state.json"

// WindowState is the adapter for row model.RowWindowState.
type WindowState struct{}

var _ sources.Adapter = WindowState{}

// New returns the adapter.
func New() sources.Adapter { return WindowState{} }

// Row implements sources.Adapter.
func (WindowState) Row() model.RowID { return model.RowWindowState }

// Read implements sources.Adapter. The item's value is the file's hash;
// its Raw holds the bytes for copies.
func (WindowState) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowWindowState)
	data, present, err := sources.ReadFile(rc.Folder, FileName)
	if err != nil || !present {
		return snap, err
	}
	it, err := sources.FileItem(FileName, data, "", sources.FileMTimeMillis(rc.Folder, FileName))
	if err != nil {
		return nil, err
	}
	snap.Items[it.Key] = it
	return snap, nil
}

// Ops implements sources.Adapter: the file is replaced by the source's
// bytes when their hash matches the desired item, or quarantined when the
// desired snapshot lacks it.
func (a WindowState) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	var res sources.OpsResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	current, desired = sources.OrEmpty(current, model.RowWindowState), sources.OrEmpty(desired, model.RowWindowState)
	cur, hasCur := current.Items[FileName]
	des, hasDes := desired.Items[FileName]
	if hasCur == hasDes && (!hasCur || cur.Hash == des.Hash) {
		return res, nil
	}
	doc, err := sources.LoadFile(oc.Folder, FileName)
	if err != nil {
		return res, err
	}
	b := sources.NewBuilder(doc, model.RowWindowState)
	if !hasDes {
		b.Remove(FileName, "")
		res.Edits = b.Edits()
		return res, nil
	}
	var cache *model.RowSnapshot
	src, err := sources.SourceItem(ctx, a, oc.Source, &cache, FileName, des.Hash)
	if err != nil {
		return res, err
	}
	if src == nil {
		res.Skipped = append(res.Skipped, sources.Skip(model.RowWindowState, FileName, sources.NoteSourceChanged))
		return res, nil
	}
	b.Replace(src.Raw, FileName, 0, false)
	res.Edits = b.Edits()
	return res, nil
}
