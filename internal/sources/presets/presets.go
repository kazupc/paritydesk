package presets

import (
	"context"
	"encoding/json"
	"fmt"

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

// Default limits (the app's own), used when the manifest gives none.
const (
	defaultMaxPresets = 200
	defaultMaxBytes   = 512 << 10
)

// defaultSecret are the dotted paths of env-var objects inside a preset.
var defaultSecret = []string{"settings.env", "env"}

// Paths inside the decoded values.
var (
	presetsPath = jsonx.Path{"state", "presets"}
	placePath   = jsonx.Path{"state", "placeDefaults"}
	startedPath = jsonx.Path{"state", "bySessionId"}
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

// Read implements sources.Adapter. Presets come from Local Storage (the
// truth), or from the file's byAccount entry when Local Storage lacks
// them. Env-var values are replaced by "sha256:<hex>".
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
	arr, label, err := presetArray(rc, pdoc)
	if err != nil {
		return nil, err
	}
	if err := addPresets(snap, arr, label, rc.Spec); err != nil {
		return nil, err
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

// presetArray returns the presets array from LS, else from the file.
func presetArray(rc sources.ReadContext, pdoc sources.Doc) (json.RawMessage, string, error) {
	if v, found, err := pdoc.Get(presetsPath); err != nil || found {
		return v, pdoc.Name(), err
	}
	fdoc, err := sources.LoadFile(rc.Folder, FileName)
	if err != nil {
		return nil, "", err
	}
	v, _, err := fdoc.Get(filePath(rc.Replica))
	return v, FileName, err
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
// ["byAccount",<acct>,"presets"] in the file (mode 0600), together.
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
			elems = append(elems, b)
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
		v, err := jsonx.Marshal(map[string]any{"state": map[string]any{"bySessionId": m}, "version": 0})
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
