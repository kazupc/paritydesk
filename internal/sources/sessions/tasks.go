package sessions

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources"
)

// taskFile is one task list of the replica's session directory.
type taskFile struct {
	name   string // relative to the session directory
	member string // the array member keyed by id
	prefix string // item key prefix
	// fresh are the other members a file paritydesk creates starts with.
	fresh map[string]json.RawMessage
}

// taskFiles are the backlog ({"version":1,"items":[...]}) and the
// scheduled tasks ({"scheduledTasks":[...],"recordedSkips":{},...}).
// Other top-level members of each file (the version, recorded skips and
// the app's migration stamps) stay the target's and are never edited.
var taskFiles = []taskFile{
	{name: "backlog/tasks.json", member: "items", prefix: "backlog/",
		fresh: map[string]json.RawMessage{"version": json.RawMessage("1")}},
	{name: "scheduled-tasks.json", member: "scheduledTasks", prefix: "scheduled/",
		fresh: map[string]json.RawMessage{"recordedSkips": json.RawMessage("{}")}},
}

// Tasks is the adapter for row model.RowTasks.
type Tasks struct{}

var _ sources.Adapter = Tasks{}

// NewTasks returns the adapter.
func NewTasks() sources.Adapter { return Tasks{} }

// Row implements sources.Adapter.
func (Tasks) Row() model.RowID { return model.RowTasks }

// Read implements sources.Adapter. Items are "backlog/<id>" and
// "scheduled/<id>"; values are the task objects; AppTime is the largest
// of the manifest's run-time fields. Order lists the keys in file order.
func (Tasks) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowTasks)
	for _, f := range taskFiles {
		doc, err := sources.LoadFile(rc.Folder, sources.SessionRel(rc.Replica, f.name))
		if err != nil {
			return nil, err
		}
		v, found, err := doc.Get(jsonx.Path{f.member})
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		if err := addTasks(snap, v, f, doc.Name(), rc.Spec.Fields[sources.RoleMaxTime]); err != nil {
			return nil, err
		}
	}
	return snap, nil
}

func addTasks(snap *model.RowSnapshot, v json.RawMessage, f taskFile, label string, timeFields []string) error {
	elems, err := sources.DecodeArray(v, label)
	if err != nil {
		return err
	}
	for _, e := range elems {
		obj, err := sources.DecodeObject(e, label)
		if err != nil {
			return err
		}
		id, ok := sources.IDString(obj)
		key := f.prefix + id
		if _, dup := snap.Items[key]; !ok || dup {
			return fmt.Errorf("sessions: %s: task without a unique id", label)
		}
		it, err := model.NewItem(key, e, e, model.Stamp{AppTime: sources.MaxTime(obj, timeFields)}, false)
		if err != nil {
			return fmt.Errorf("sessions: %s: %w", label, err)
		}
		snap.Items[key] = it
		snap.Order = append(snap.Order, key)
	}
	return nil
}

// Ops implements sources.Adapter: each file's array member is rewritten
// at its literal path when its items or their order differ; unchanged
// tasks keep the target's bytes. A missing file is created holding the
// array and the file's fresh members (the backlog's version 1, an empty
// recordedSkips); the app's migration stamps are left for the app to add.
func (Tasks) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	var res sources.OpsResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	current, desired = sources.OrEmpty(current, model.RowTasks), sources.OrEmpty(desired, model.RowTasks)
	for _, f := range taskFiles {
		edits, err := taskEdits(oc, f, current, desired)
		if err != nil {
			return res, err
		}
		res.Edits = append(res.Edits, edits...)
	}
	return res, nil
}

func taskEdits(oc sources.OpsContext, f taskFile, current, desired *model.RowSnapshot) ([]plan.Edit, error) {
	want := sources.ItemsWithPrefix(desired, f.prefix, current.Order)
	have := sources.ItemsWithPrefix(current, f.prefix, nil)
	if sameSeq(current, desired, have, want) {
		return nil, nil
	}
	elems := make([]json.RawMessage, len(want))
	for i, k := range want {
		elems[i] = desired.Items[k].Value
		if c, ok := current.Items[k]; ok && c.Hash == desired.Items[k].Hash && c.Raw != nil {
			elems[i] = c.Raw
		}
	}
	arr := sources.JoinArray(elems)
	doc, err := sources.LoadFile(oc.Folder, sources.SessionRel(oc.Replica, f.name))
	if err != nil {
		return nil, err
	}
	b := sources.NewBuilder(doc, model.RowTasks)
	if !doc.Present {
		obj := map[string]json.RawMessage{f.member: arr}
		for k, v := range f.fresh {
			obj[k] = v
		}
		out, err := jsonx.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("sessions: encode %s: %w", f.name, err)
		}
		b.Replace(out, "", 0, false)
	} else {
		b.Set(jsonx.Path{f.member}, arr, "", false)
	}
	return b.Edits(), nil
}

func sameSeq(current, desired *model.RowSnapshot, have, want []string) bool {
	if len(have) != len(want) {
		return false
	}
	for i := range have {
		if have[i] != want[i] || current.Items[have[i]].Hash != desired.Items[want[i]].Hash {
			return false
		}
	}
	return true
}
