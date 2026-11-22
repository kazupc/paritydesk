package sidebar

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources"
)

// OrderItem is the only item key of the list rows (group-order and
// pinned-order).
const OrderItem = "order"

// mirrorContainer is the config-mirror object shared by every mirrored
// row. Rows never create it: a target without it is left unchanged.
var mirrorContainer = jsonx.Path{"preferences", "epitaxyPrefs"}

// copySpec names one Local Storage copy of a mirrored value: the item name
// and the path of the value inside its decoded text.
type copySpec struct {
	key string
	sub jsonx.Path
}

// layout is where one row's value lives: the config mirror and the Local
// Storage copies (the LSS envelope first, then an optional persisted
// store).
type layout struct {
	row        model.RowID
	mirrorFile string
	mirrorPath jsonx.Path
	lss        copySpec
	store      *copySpec // dframe-store; nil when the row has none
	// persistVersion is the manifest's expected store version (0: shape
	// only).
	persistVersion int
}

func (l layout) copies() []copySpec {
	out := []copySpec{l.lss}
	if l.store != nil {
		out = append(out, *l.store)
	}
	return out
}

// newLayout validates spec and builds the row's layout. sub returns the
// value paths inside the LSS envelope and the persisted store.
func newLayout(row model.RowID, spec compat.RowSpec, sub func(mirror jsonx.Path) (jsonx.Path, jsonx.Path)) (layout, error) {
	if len(spec.MirrorPaths) != 1 || len(spec.MirrorPaths[0]) <= len(mirrorContainer) || len(spec.LSKeys) == 0 {
		return layout{}, fmt.Errorf("sidebar: %s: manifest spec lacks mirror path or keys", row)
	}
	mp := jsonx.Path(spec.MirrorPaths[0])
	for i, s := range mirrorContainer {
		if mp[i] != s {
			return layout{}, fmt.Errorf("sidebar: %s: mirror path outside the config mirror", row)
		}
	}
	file := spec.MirrorFile
	if file == "" {
		file = sources.DesktopConfigFile
	}
	lssSub, storeSub := sub(mp)
	l := layout{row: row, mirrorFile: file, mirrorPath: mp, lss: copySpec{spec.LSKeys[0], lssSub},
		persistVersion: spec.PersistVersion}
	if len(spec.LSKeys) > 1 && storeSub != nil {
		l.store = &copySpec{spec.LSKeys[1], storeSub}
	}
	if !sources.IsEnvelopeKey(l.lss.key) {
		return layout{}, fmt.Errorf("sidebar: %s: first LS key must be an LSS envelope", row)
	}
	return l, nil
}

// copyValue is one copy's value at read time.
type copyValue struct {
	doc   sources.Doc
	value json.RawMessage
	found bool
}

// readCopies loads the mirror and every LS copy.
func (l layout) readCopies(rc sources.ReadContext) (mirror copyValue, ls []copyValue, ts int64, err error) {
	mdoc, err := sources.LoadFile(rc.Folder, l.mirrorFile)
	if err != nil {
		return mirror, nil, 0, err
	}
	if mirror, err = valueAt(mdoc, l.mirrorPath); err != nil {
		return mirror, nil, 0, err
	}
	for i, c := range l.copies() {
		d, err := sources.LoadLS(rc.LS, c.key)
		if err != nil {
			return mirror, nil, 0, err
		}
		cv, err := valueAt(d, c.sub)
		if err != nil {
			return mirror, nil, 0, err
		}
		if i == 0 && d.Present {
			if raw, ok, _ := d.Get(jsonx.Path{"timestamp"}); ok {
				var f float64
				if json.Unmarshal(raw, &f) == nil {
					ts = int64(f)
				}
			}
		}
		ls = append(ls, cv)
	}
	return mirror, ls, ts, nil
}

func locFile(rel string) plan.Location { return plan.Location{File: rel} }

func valueAt(d sources.Doc, p jsonx.Path) (copyValue, error) {
	v, found, err := d.Get(p)
	return copyValue{doc: d, value: v, found: found}, err
}

// ---------------------------------------------------------------- list rows

// listRow is the shared implementation of group-order and pinned-order:
// one item "order" holding the array; Order lists its string elements.
type listRow struct {
	row model.RowID
	sub func(mirror jsonx.Path) (jsonx.Path, jsonx.Path)
}

func (r listRow) layout(spec compat.RowSpec) (layout, error) { return newLayout(r.row, spec, r.sub) }

func (r listRow) read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, err := r.layout(rc.Spec)
	if err != nil {
		return nil, err
	}
	mirror, ls, ts, err := l.readCopies(rc)
	if err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(r.row)
	value, stamp := mirror.value, int64(0)
	switch {
	case mirror.found:
		stale := false
		for _, c := range ls {
			if c.found && !sameValue(c.value, mirror.value) {
				stale = true
			}
		}
		if stale {
			snap.Stale = []string{OrderItem}
		} else if ls[0].found {
			stamp = ts
		}
	case ls[0].found:
		value, stamp = ls[0].value, ts
	default:
		return snap, nil
	}
	return snap, r.fill(snap, value, model.Stamp{AppTime: stamp}, l)
}

func (r listRow) fill(snap *model.RowSnapshot, value json.RawMessage, stamp model.Stamp, l layout) error {
	arr, err := sources.DecodeArray(value, l.mirrorFile)
	if err != nil {
		return fmt.Errorf("sidebar: %s: unexpected shape: %w", r.row, err)
	}
	it, err := model.NewItem(OrderItem, value, value, stamp, false)
	if err != nil {
		return fmt.Errorf("sidebar: %s: %w", r.row, err)
	}
	snap.Items[OrderItem] = it
	for _, e := range arr {
		var s string
		if json.Unmarshal(e, &s) == nil {
			snap.Order = append(snap.Order, s)
		}
	}
	return nil
}

func (r listRow) mirrorItems(spec compat.RowSpec, mirror []byte) (*model.RowSnapshot, error) {
	l, err := r.layout(spec)
	if err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(r.row)
	cv, err := valueAt(sources.Doc{Loc: locFile(l.mirrorFile), Present: true, Stored: mirror, Text: mirror}, l.mirrorPath)
	if err != nil || !cv.found {
		return snap, err
	}
	return snap, r.fill(snap, cv.value, model.Stamp{}, l)
}

func (r listRow) ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	var res sources.OpsResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	current, desired = sources.OrEmpty(current, r.row), sources.OrEmpty(desired, r.row)
	if sources.SameItems(current, desired, false) {
		return res, nil
	}
	l, err := r.layout(oc.Spec)
	if err != nil {
		return res, err
	}
	w, ok, err := newWriter(oc, l)
	if err != nil || !ok {
		res.Skipped = []model.Change{sources.Skip(r.row, OrderItem, sources.NoteMirrorMissing)}
		return res, err
	}
	d, has := desired.Items[OrderItem]
	w.lssFull = d.Value
	if err := w.setOrDelete(nil, d.Value, has, OrderItem); err != nil {
		return res, err
	}
	return w.result(), nil
}

// ---------------------------------------------------------------- writer

// writer emits one row's edits on the mirror and every LS copy.
type writer struct {
	l       layout
	oc      sources.OpsContext
	mirror  *sources.Builder
	lss     *sources.Builder
	store   *sources.Builder // nil: the store is absent and left alone
	stamped bool
	stamps  map[string]int64
	// lssFull is the row's whole value at the LSS sub-path, used when the
	// LSS item is absent and must be created.
	lssFull json.RawMessage
}

// newWriter loads every location. ok is false when the config mirror or
// its shared container is missing (the row is then left unchanged).
func newWriter(oc sources.OpsContext, l layout) (*writer, bool, error) {
	mdoc, err := sources.LoadFile(oc.Folder, l.mirrorFile)
	if err != nil {
		return nil, false, err
	}
	w := &writer{l: l, oc: oc, mirror: sources.NewBuilder(mdoc, l.row), stamps: map[string]int64{}}
	if ok, err := w.mirror.Has(mirrorContainer); err != nil || !ok {
		return nil, false, err
	}
	ldoc, err := sources.LoadLS(oc.LS, l.lss.key)
	if err != nil {
		return nil, false, err
	}
	w.lss = sources.NewBuilder(ldoc, l.row)
	if l.store != nil {
		sdoc, err := sources.LoadLS(oc.LS, l.store.key)
		if err != nil {
			return nil, false, err
		}
		if sdoc.Present {
			if err := checkStore(sdoc, l.persistVersion); err != nil {
				return nil, false, err
			}
			w.store = sources.NewBuilder(sdoc, l.row)
		}
	}
	return w, true, nil
}

// checkStore refuses a persisted store whose wrapper or version differs
// from the manifest.
func checkStore(d sources.Doc, want int) error {
	_, v, err := localstorage.PersistedState(d.Text)
	if err != nil {
		return fmt.Errorf("sidebar: %s: no persisted-store wrapper", d.Name())
	}
	if want > 0 && v != want {
		return fmt.Errorf("sidebar: %s: persisted-store version differs from the manifest", d.Name())
	}
	return nil
}

func join(base, rel jsonx.Path) jsonx.Path {
	out := append(jsonx.Path(nil), base...)
	return append(out, rel...)
}

// setOrDelete sets (has) or deletes rel below the row's value on every
// location. A missing LSS item is created as a fresh envelope holding
// value; a missing store is left alone.
func (w *writer) setOrDelete(rel jsonx.Path, value json.RawMessage, has bool, item string) error {
	mp := join(w.l.mirrorPath, rel)
	if has {
		if err := w.mirror.Ensure(mp[:len(mp)-1], item); err != nil {
			return err
		}
		w.mirror.Set(mp, value, item, false)
	} else if err := w.mirror.Delete(mp, item, true); err != nil {
		return err
	}
	if err := w.lssEdit(rel, value, has, item); err != nil {
		return err
	}
	if w.store == nil {
		return nil
	}
	sp := join(w.l.store.sub, rel)
	if !has {
		return w.store.Delete(sp, item, true)
	}
	if err := w.store.Ensure(sp[:len(sp)-1], item); err != nil {
		return err
	}
	w.store.Set(sp, value, item, false)
	return nil
}

func (w *writer) lssEdit(rel jsonx.Path, value json.RawMessage, has bool, item string) error {
	lp := join(w.l.lss.sub, rel)
	switch {
	case !w.lss.Doc.Present && !has:
		return nil
	case !w.lss.Doc.Present:
		// A fresh envelope holds the row's whole value (lssFull); later
		// edits of the same row add nothing, since every edit of a location
		// is computed against its original bytes.
		if w.stamped {
			return nil
		}
		inner, err := nest(w.l.lss.sub[1:], w.lssFull)
		if err != nil {
			return err
		}
		env, err := localstorage.NewEnvelope(inner, w.oc.NowMillis)
		if err != nil {
			return fmt.Errorf("sidebar: %s: %w", w.l.lss.key, err)
		}
		w.lss.Replace(env, item, 0, false)
		w.stamped = true
	case !has && len(lp) == 1:
		w.lss.Remove(item, "")
	case !has:
		if err := w.lss.Delete(lp, item, true); err != nil {
			return err
		}
		w.stamp(item)
	default:
		if err := w.lss.Ensure(lp[:len(lp)-1], item); err != nil {
			return err
		}
		w.lss.Set(lp, value, item, false)
		w.stamp(item)
	}
	w.stamps[item] = w.oc.NowMillis
	return nil
}

// stamp sets the envelope timestamp once per row.
func (w *writer) stamp(item string) {
	if w.stamped {
		return
	}
	w.lss.Set(jsonx.Path{"timestamp"}, json.RawMessage(fmt.Sprint(w.oc.NowMillis)), item, false)
	w.stamped = true
}

// nest wraps value in objects named by path ({"a":{"b":value}}).
func nest(path jsonx.Path, value json.RawMessage) (json.RawMessage, error) {
	for i := len(path) - 1; i >= 0; i-- {
		b, err := jsonx.Marshal(map[string]json.RawMessage{path[i]: value})
		if err != nil {
			return nil, err
		}
		value = b
	}
	return value, nil
}

func (w *writer) result() sources.OpsResult {
	var res sources.OpsResult
	res.Edits = append(res.Edits, w.mirror.Edits()...)
	res.Edits = append(res.Edits, w.lss.Edits()...)
	if w.store != nil {
		res.Edits = append(res.Edits, w.store.Edits()...)
	}
	if len(w.stamps) > 0 {
		res.WrittenStamps = w.stamps
	}
	return res
}

func sameValue(a, b json.RawMessage) bool {
	ha, err1 := model.ItemHash(a)
	hb, err2 := model.ItemHash(b)
	return err1 == nil && err2 == nil && ha == hb
}

// ---------------------------------------------------------------- group order

// GroupOrder is the adapter for row model.RowGroupOrder.
type GroupOrder struct{}

var (
	_ sources.Adapter      = GroupOrder{}
	_ sources.MirrorReader = GroupOrder{}
)

// groupOrder: the LSS value is the array itself; there is no store copy.
var groupOrder = listRow{row: model.RowGroupOrder, sub: func(jsonx.Path) (jsonx.Path, jsonx.Path) {
	return jsonx.Path{"value"}, nil
}}

// NewGroupOrder returns the adapter.
func NewGroupOrder() sources.Adapter { return GroupOrder{} }

// Row implements sources.Adapter.
func (GroupOrder) Row() model.RowID { return model.RowGroupOrder }

// Read implements sources.Adapter.
func (GroupOrder) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return groupOrder.read(ctx, rc)
}

// Ops implements sources.Adapter.
func (GroupOrder) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return groupOrder.ops(ctx, oc, current, desired)
}

// MirrorItems implements sources.MirrorReader.
func (GroupOrder) MirrorItems(spec compat.RowSpec, mirror []byte) (*model.RowSnapshot, error) {
	return groupOrder.mirrorItems(spec, mirror)
}

// ---------------------------------------------------------------- pinned order

// PinnedOrder is the adapter for row model.RowPinnedOrder.
type PinnedOrder struct{}

var (
	_ sources.Adapter      = PinnedOrder{}
	_ sources.MirrorReader = PinnedOrder{}
)

// pinnedOrder: the mirror path ends in the slice's member name, which is
// also the member inside the LSS value and the store's state.
var pinnedOrder = listRow{row: model.RowPinnedOrder, sub: func(mp jsonx.Path) (jsonx.Path, jsonx.Path) {
	leaf := mp[len(mp)-1]
	return jsonx.Path{"value", leaf}, jsonx.Path{"state", leaf}
}}

// NewPinnedOrder returns the adapter.
func NewPinnedOrder() sources.Adapter { return PinnedOrder{} }

// Row implements sources.Adapter.
func (PinnedOrder) Row() model.RowID { return model.RowPinnedOrder }

// Read implements sources.Adapter.
func (PinnedOrder) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return pinnedOrder.read(ctx, rc)
}

// Ops implements sources.Adapter.
func (PinnedOrder) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return pinnedOrder.ops(ctx, oc, current, desired)
}

// MirrorItems implements sources.MirrorReader.
func (PinnedOrder) MirrorItems(spec compat.RowSpec, mirror []byte) (*model.RowSnapshot, error) {
	return pinnedOrder.mirrorItems(spec, mirror)
}
