package presets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
)

// On-disk names.
const (
	// FileName mirrors the Local Storage presets per account (mode 0600).
	FileName = "ccd-session-presets.json"
	fileMode = 0o600
)

// Item keys.
const (
	PresetPrefix  = "preset/"
	StartedPrefix = "started/"
	PlaceDefaults = "placeDefaults"
)

// StartedVersion is the persisted version of
// persisted.ccd-started-environments on app 6.28899.2 (the manifest's
// started-environments-version probe), used only when paritydesk creates
// the item.
const StartedVersion = 1

// Default limits (the app's own), used when the manifest gives none.
const (
	defaultMaxPresets = 200
	defaultMaxBytes   = 512 << 10
)

// defaultSecret are the dotted paths of env-var objects inside a preset.
var defaultSecret = []string{"settings.env", "env"}

// NoteCopiesDisagree is the skip note of a preset change that was not
// written because the Local Storage copy and the file mirror disagree.
const NoteCopiesDisagree = "Local Storage and ccd-session-presets.json disagree; presets left unchanged"

// Paths inside the decoded values.
var (
	presetsPath  = jsonx.Path{"state", "presets"}
	mirroredPath = jsonx.Path{"state", "fileMirrored"}
	placePath    = jsonx.Path{"state", "placeDefaults"}
	startedPath  = jsonx.Path{"state", "bySessionId"}
)

// Environments is the adapter for row model.RowEnvironments.
type Environments struct{}

var _ sources.Adapter = Environments{}

// New returns the adapter.
func New() sources.Adapter { return Environments{} }

// Row implements sources.Adapter.
func (Environments) Row() model.RowID { return model.RowEnvironments }

// keys returns the presets and started-environments LS item names.
func keys(spec compat.RowSpec) (string, string, error) {
	if len(spec.LSKeys) != 2 {
		return "", "", fmt.Errorf("presets: manifest spec must name two Local Storage items")
	}
	return spec.LSKeys[0], spec.LSKeys[1], nil
}

// Read implements sources.Adapter. ccd-session-presets.json
// byAccount[<acct>] mirrors the Local Storage presets item when the item's
// state.fileMirrored is true (app 6.28899.2). Then presets are read from
// the file when it holds the account's presets array, and a Local Storage
// copy that differs is listed in Stale (per preset key) and never
// harvested. When fileMirrored is not true, or the file has no entry, the
// Local Storage copy is used, and a differing file copy is listed in
// Stale. Ops never writes presets while the copies disagree, so neither
// copy is overwritten by the other (the app's read order has not been
// observed on a real Mac). Env-var values are replaced by "sha256:<hex>".
// placeDefaults and the started-environments map exist in Local Storage
// only. The per-org presets item (persisted.ccd-session-presets.<acct>.
// <org>) is never read.
func (Environments) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pk, sk, err := keys(rc.Spec)
	if err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowEnvironments)
	pdoc, err := sources.LoadLS(rc.LS, pk)
	if err != nil {
		return nil, err
	}
	fdoc, err := sources.LoadFile(rc.Folder, FileName)
	if err != nil {
		return nil, err
	}
	fileArr, inFile, err := fdoc.Get(filePath(rc.Replica))
	if err != nil {
		return nil, err
	}
	lsArr, inLS, err := pdoc.Get(presetsPath)
	if err != nil {
		return nil, err
	}
	mirrored, _, err := pdoc.Get(mirroredPath)
	if err != nil {
		return nil, err
	}
	switch {
	case inFile && (!inLS || string(mirrored) == "true"):
		if err := addPresets(snap, fileArr, FileName, rc.Spec); err != nil {
			return nil, err
		}
		if inLS {
			if err := markStale(snap, lsArr, pdoc.Name(), rc.Spec); err != nil {
				return nil, err
			}
		}
	case inLS:
		if err := addPresets(snap, lsArr, pdoc.Name(), rc.Spec); err != nil {
			return nil, err
		}
		if inFile {
			if err := markStale(snap, fileArr, FileName, rc.Spec); err != nil {
				return nil, err
			}
		}
	}
	if err := addPlaceDefaults(snap, pdoc); err != nil {
		return nil, err
	}
	sdoc, err := sources.LoadLS(rc.LS, sk)
	if err != nil {
		return nil, err
	}
	return snap, addStarted(snap, sdoc)
}

// markStale lists in snap.Stale every preset key on which the other
// copy's array disagrees with the harvested items (a differing value, or a
// key only one copy has), sorted.
func markStale(snap *model.RowSnapshot, otherArr json.RawMessage, label string, spec compat.RowSpec) error {
	ls := sources.NewSnapshot(model.RowEnvironments)
	if err := addPresets(ls, otherArr, label, spec); err != nil {
		return err
	}
	stale := map[string]bool{}
	for k, it := range snap.Items {
		if o, ok := ls.Items[k]; !ok || o.Hash != it.Hash {
			stale[k] = true
		}
	}
	for k := range ls.Items {
		if _, ok := snap.Items[k]; !ok {
			stale[k] = true
		}
	}
	for k := range stale {
		snap.Stale = append(snap.Stale, k)
	}
	sort.Strings(snap.Stale)
	return nil
}

func filePath(r model.Replica) jsonx.Path {
	return jsonx.Path{"byAccount", string(r.Account), "presets"}
}

func addPresets(snap *model.RowSnapshot, arr json.RawMessage, label string, spec compat.RowSpec) error {
	if arr == nil {
		return nil
	}
	elems, err := sources.DecodeArray(arr, label)
	if err != nil {
		return err
	}
	secret := spec.Fields[sources.RoleSecret]
	if len(secret) == 0 {
		secret = defaultSecret
	}
	for _, e := range elems {
		obj, err := sources.DecodeObject(e, label)
		if err != nil {
			return err
		}
		id, ok := sources.IDString(obj)
		key := PresetPrefix + id
		if _, dup := snap.Items[key]; !ok || dup {
			return fmt.Errorf("presets: %s: preset without a unique id", label)
		}
		val, isSecret, err := sources.HashSecrets(e, secret, label)
		if err != nil {
			return err
		}
		stamp := model.Stamp{AppTime: sources.MaxTime(obj, spec.Fields[sources.RoleMaxTime])}
		it, err := model.NewItem(key, val, e, stamp, isSecret)
		if err != nil {
			return fmt.Errorf("presets: %s: %w", label, err)
		}
		snap.Items[key] = it
		snap.Order = append(snap.Order, key)
	}
	return nil
}

func addPlaceDefaults(snap *model.RowSnapshot, pdoc sources.Doc) error {
	v, found, err := pdoc.Get(placePath)
	if err != nil || !found {
		return err
	}
	it, err := model.NewItem(PlaceDefaults, v, v, model.Stamp{}, false)
	if err != nil {
		return fmt.Errorf("presets: %s: %w", pdoc.Name(), err)
	}
	snap.Items[PlaceDefaults] = it
	return nil
}

func addStarted(snap *model.RowSnapshot, sdoc sources.Doc) error {
	v, found, err := sdoc.Get(startedPath)
	if err != nil || !found {
		return err
	}
	obj, err := sources.DecodeObject(v, sdoc.Name())
	if err != nil {
		return err
	}
	for sid, env := range obj {
		it, err := model.NewItem(StartedPrefix+sid, env, env, model.Stamp{}, false)
		if err != nil {
			return fmt.Errorf("presets: %s: %w", sdoc.Name(), err)
		}
		snap.Items[it.Key] = it
	}
	return nil
}

// ops carries one Ops call's state.
type ops struct {
	a       Environments
	ctx     context.Context
	oc      sources.OpsContext
	cur     *model.RowSnapshot
	des     *model.RowSnapshot
	source  *model.RowSnapshot // cache of the source's snapshot
	res     sources.OpsResult
	secrets bool // the presets array holds source secret bytes
}

// Ops implements sources.Adapter. The presets array is rewritten at
// ["state","presets"] in Local Storage and at
// ["byAccount",<acct>,"presets"] in the file (mode 0600), together, so
// the two copies never disagree after a write; other members of either
// copy (builtIns, fileMirrored, seeding flags) are left alone.
// Unchanged presets keep the target's bytes; changed presets with secrets
// are copied from the source's raw bytes after a hash check, and when the
// source moved on the target's preset is kept and reported skipped.
// placeDefaults is set at ["state","placeDefaults"]; started environments
// are set or deleted at ["state","bySessionId",<session id>].
func (a Environments) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	if err := ctx.Err(); err != nil {
		return sources.OpsResult{}, err
	}
	o := &ops{a: a, ctx: ctx, oc: oc,
		cur: sources.OrEmpty(current, model.RowEnvironments), des: sources.OrEmpty(desired, model.RowEnvironments)}
	pk, sk, err := keys(oc.Spec)
	if err != nil {
		return o.res, err
	}
	if err := o.presets(pk); err != nil {
		return o.res, err
	}
	if err := o.started(sk); err != nil {
		return o.res, err
	}
	return o.res, nil
}

// presets emits the LS and file edits for presets and placeDefaults.
func (o *ops) presets(pk string) error {
	pdoc, err := sources.LoadLS(o.oc.LS, pk)
	if err != nil {
		return err
	}
	arr, changed, err := o.presetBytes(pdoc)
	if err != nil {
		return err
	}
	if changed {
		disagree, err := o.copiesDisagree(pdoc)
		if err != nil {
			return err
		}
		if disagree {
			// Writing would replace one copy with the other's values.
			for _, k := range changedPresets(o.cur, o.des) {
				o.res.Skipped = append(o.res.Skipped, sources.Skip(model.RowEnvironments, k, NoteCopiesDisagree))
			}
			changed, arr = false, nil
		}
	}
	place, placeHas := o.des.Items[PlaceDefaults]
	placeChanged := itemChanged(o.cur, o.des, PlaceDefaults)
	if !changed && !placeChanged {
		return nil
	}
	lb := sources.NewBuilder(pdoc, model.RowEnvironments)
	if !pdoc.Present {
		if err := o.newPresetsItem(lb, arr, place.Value, placeHas); err != nil {
			return err
		}
	} else {
		if err := lb.Ensure(jsonx.Path{"state"}, ""); err != nil {
			return err
		}
		if changed {
			lb.Set(presetsPath, arr, "", o.secrets)
		}
		if placeChanged && placeHas {
			lb.Set(placePath, place.Value, PlaceDefaults, false)
		} else if placeChanged {
			if err := lb.Delete(placePath, PlaceDefaults, true); err != nil {
				return err
			}
		}
	}
	o.res.Edits = append(o.res.Edits, lb.Edits()...)
	if changed {
		return o.file(arr)
	}
	return nil
}

// copiesDisagree reports whether the target's Local Storage presets array
// and its file mirror entry both exist and hold different presets.
func (o *ops) copiesDisagree(pdoc sources.Doc) (bool, error) {
	lsArr, inLS, err := pdoc.Get(presetsPath)
	if err != nil || !inLS {
		return false, err
	}
	fdoc, err := sources.LoadFile(o.oc.Folder, FileName)
	if err != nil {
		return false, err
	}
	fileArr, inFile, err := fdoc.Get(filePath(o.oc.Replica))
	if err != nil || !inFile {
		return false, err
	}
	a := sources.NewSnapshot(model.RowEnvironments)
	if err := addPresets(a, fileArr, FileName, o.oc.Spec); err != nil {
		return false, err
	}
	if err := markStale(a, lsArr, pdoc.Name(), o.oc.Spec); err != nil {
		return false, err
	}
	return len(a.Stale) > 0, nil
}

// changedPresets lists the preset keys whose desired item differs from
// the current one, sorted.
func changedPresets(cur, des *model.RowSnapshot) []string {
	var out []string
	for _, s := range []*model.RowSnapshot{cur, des} {
		for _, k := range sources.ItemsWithPrefix(s, PresetPrefix, nil) {
			if itemChanged(cur, des, k) && !contains(out, k) {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (o *ops) newPresetsItem(lb *sources.Builder, arr json.RawMessage, place json.RawMessage, placeHas bool) error {
	state := map[string]json.RawMessage{"presets": arr}
	if arr == nil {
		state["presets"] = json.RawMessage("[]")
	}
	if placeHas {
		state["placeDefaults"] = place
	}
	b, err := jsonx.Marshal(map[string]any{"state": state, "version": o.oc.Spec.PersistVersion})
	if err != nil {
		return fmt.Errorf("presets: encode: %w", err)
	}
	lb.Replace(b, "", 0, o.secrets)
	return nil
}

// file writes the presets array into the file's byAccount entry.
func (o *ops) file(arr json.RawMessage) error {
	fdoc, err := sources.LoadFile(o.oc.Folder, FileName)
	if err != nil {
		return err
	}
	fb := sources.NewBuilder(fdoc, model.RowEnvironments)
	acct := string(o.oc.Replica.Account)
	if !fdoc.Present {
		b, err := jsonx.Marshal(map[string]any{"byAccount": map[string]any{acct: map[string]json.RawMessage{"presets": arr}}})
		if err != nil {
			return fmt.Errorf("presets: encode: %w", err)
		}
		fb.Replace(b, "", fileMode, o.secrets)
	} else {
		p := filePath(o.oc.Replica)
		if err := fb.Ensure(p[:len(p)-1], ""); err != nil {
			return err
		}
		fb.Set(p, arr, "", o.secrets)
	}
	for _, e := range fb.Edits() {
		e.Mode = fileMode
		o.res.Edits = append(o.res.Edits, e)
	}
	return nil
}

// presetBytes builds the desired presets array and reports whether it
// differs from the target's.
func (o *ops) presetBytes(pdoc sources.Doc) (json.RawMessage, bool, error) {
	order := sources.ItemsWithPrefix(o.des, PresetPrefix, o.cur.Order)
	curOrder := sources.ItemsWithPrefix(o.cur, PresetPrefix, nil)
	var elems []json.RawMessage
	for _, k := range order {
		b, err := o.presetElem(k)
		if err != nil {
			return nil, false, err
		}
		if b != nil {
			elems = append(elems, compact(b))
		}
	}
	arr := sources.JoinArray(elems)
	curElems := make([]json.RawMessage, 0, len(curOrder))
	for _, k := range curOrder {
		curElems = append(curElems, o.cur.Items[k].Raw)
	}
	same, err := equalJSON(arr, sources.JoinArray(curElems))
	if err != nil {
		return nil, false, err
	}
	if len(order) == 0 && len(curOrder) == 0 {
		return nil, false, nil
	}
	if !same {
		if err := o.checkLimits(elems); err != nil {
			return nil, false, err
		}
	}
	return arr, !same, nil
}

// presetElem returns the bytes for preset k, or nil when it is left out.
func (o *ops) presetElem(k string) (json.RawMessage, error) {
	d := o.des.Items[k]
	c, hasCur := o.cur.Items[k]
	if hasCur && c.Hash == d.Hash && c.Raw != nil {
		return c.Raw, nil
	}
	src, err := sources.SourceItem(o.ctx, o.a, o.oc.Source, &o.source, k, d.Hash)
	if err != nil {
		return nil, err
	}
	switch {
	case src != nil:
		o.secrets = o.secrets || d.Secret
		return src.Raw, nil
	case !d.Secret:
		return d.Value, nil
	}
	o.res.Skipped = append(o.res.Skipped, sources.Skip(model.RowEnvironments, k, sources.NoteSourceChanged))
	if hasCur {
		return c.Raw, nil
	}
	return nil, nil
}

func (o *ops) checkLimits(elems []json.RawMessage) error {
	maxN, maxB := o.oc.Spec.Limits["max_presets"], o.oc.Spec.Limits["max_bytes"]
	if maxN == 0 {
		maxN = defaultMaxPresets
	}
	if maxB == 0 {
		maxB = defaultMaxBytes
	}
	if len(elems) > maxN {
		return fmt.Errorf("presets: %d presets exceed the app's limit of %d", len(elems), maxN)
	}
	if n := len(sources.JoinArray(elems)); n > maxB {
		return fmt.Errorf("presets: presets exceed the app's size limit of %d bytes", maxB)
	}
	return nil
}

// started emits the edits of the session-to-environment map.
func (o *ops) started(sk string) error {
	var changed []string
	for _, s := range []*model.RowSnapshot{o.cur, o.des} {
		for _, k := range sources.ItemsWithPrefix(s, StartedPrefix, nil) {
			if itemChanged(o.cur, o.des, k) && !contains(changed, k) {
				changed = append(changed, k)
			}
		}
	}
	if len(changed) == 0 {
		return nil
	}
	sdoc, err := sources.LoadLS(o.oc.LS, sk)
	if err != nil {
		return err
	}
	b := sources.NewBuilder(sdoc, model.RowEnvironments)
	if !sdoc.Present {
		m := map[string]json.RawMessage{}
		for _, k := range sources.ItemsWithPrefix(o.des, StartedPrefix, nil) {
			m[k[len(StartedPrefix):]] = o.des.Items[k].Value
		}
		v, err := jsonx.Marshal(map[string]any{"state": map[string]any{"bySessionId": m}, "version": StartedVersion})
		if err != nil {
			return fmt.Errorf("presets: encode: %w", err)
		}
		b.Replace(v, "", 0, false)
		o.res.Edits = append(o.res.Edits, b.Edits()...)
		return nil
	}
	if err := b.Ensure(startedPath, ""); err != nil {
		return err
	}
	for _, k := range changed {
		p := append(append(jsonx.Path(nil), startedPath...), k[len(StartedPrefix):])
		if d, ok := o.des.Items[k]; ok {
			b.Set(p, d.Value, k, false)
		} else if err := b.Delete(p, k, true); err != nil {
			return err
		}
	}
	o.res.Edits = append(o.res.Edits, b.Edits()...)
	return nil
}

func itemChanged(cur, des *model.RowSnapshot, k string) bool {
	c, okc := cur.Items[k]
	d, okd := des.Items[k]
	return okc != okd || c.Hash != d.Hash
}

func contains(s []string, k string) bool {
	for _, x := range s {
		if x == k {
			return true
		}
	}
	return false
}

// compact drops insignificant whitespace (the file copy is indented);
// string contents, secret values included, stay byte-identical.
func compact(b json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return b
	}
	return buf.Bytes()
}

func equalJSON(a, b json.RawMessage) (bool, error) {
	ha, err := model.ItemHash(a)
	if err != nil {
		return false, fmt.Errorf("presets: %w", err)
	}
	hb, err := model.ItemHash(b)
	if err != nil {
		return false, fmt.Errorf("presets: %w", err)
	}
	return ha == hb, nil
}
