package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources"
)

// Defaults used when the manifest's field lists are empty.
var (
	defaultActivity = "lastActivityAt"
	// legacyTombstoneTime is the member read from a tombstone holding an
	// object instead of a bare number. App 6.28899.2 writes a bare number
	// (ADR 0004); the object form is still read, never written.
	legacyTombstoneTime = []string{"deletedAt"}
)

// IndexVersion is the only archived-sessions.idx version paritydesk reads
// or writes.
const IndexVersion = 1

// Sessions is the adapter for row model.RowSessions.
type Sessions struct{}

var _ sources.Adapter = Sessions{}

// NewSessions returns the adapter.
func NewSessions() sources.Adapter { return Sessions{} }

// Row implements sources.Adapter.
func (Sessions) Row() model.RowID { return model.RowSessions }

// dirEntries lists the replica's session directory; a missing directory
// is empty.
func dirEntries(folder string, r model.Replica) ([]fs.DirEntry, error) {
	ents, err := os.ReadDir(sources.SessionsDir(folder, r))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sessions: list session directory: %w", err)
	}
	return ents, nil
}

// tombstoneKey returns the session id a deleted_<uuid> file names.
func tombstoneKey(name string) (string, bool) {
	u, ok := strings.CutPrefix(name, sources.TombstonePrefix)
	if !ok || !ids.IsUUID(u) {
		return "", false
	}
	return sources.RecordPrefix + u, true
}

// tmpKey returns the session id of an app-written local_<uuid>.json.tmp.
func tmpKey(name string) (string, bool) {
	base, ok := strings.CutSuffix(name, sources.TmpSuffix)
	if !ok {
		return "", false
	}
	id, ok := sources.RecordID(base)
	return string(id), ok
}

func activityField(spec compat.RowSpec) string {
	if f := spec.Fields[sources.RoleMaxTime]; len(f) > 0 {
		return f[0]
	}
	return defaultActivity
}

func tombstoneFields(spec compat.RowSpec) []string {
	if f := spec.Fields[sources.RoleTombstoneTime]; len(f) > 0 {
		return f
	}
	return legacyTombstoneTime
}

// Read implements sources.Adapter. Items are the local_<uuid>.json
// records (value as stored, AppTime the activity time). An app-written
// local_<uuid>.json.tmp is a candidate version: it replaces the record
// when it parses and is at least as recent (the app promotes it at
// startup); a malformed candidate is ignored. Tombstones come from
// deleted_<uuid> files, timed by their content (a bare JSON number of
// epoch ms) or else their mtime. Only the replica's own session directory
// is listed; other accounts' directories next to it are never read.
func (Sessions) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowSessions)
	ents, err := dirEntries(rc.Folder, rc.Replica)
	if err != nil {
		return nil, err
	}
	act := activityField(rc.Spec)
	tmps := map[string]string{}
	var recs []record
	for _, e := range ents {
		name := e.Name()
		if !e.Type().IsRegular() {
			continue
		}
		if id, ok := sources.RecordID(name); ok {
			recs = append(recs, record{key: string(id), name: name})
		} else if k, ok := tmpKey(name); ok {
			tmps[k] = name
		} else if k, ok := tombstoneKey(name); ok {
			at, err := tombstoneTime(rc, name, tombstoneFields(rc.Spec))
			if err != nil {
				return nil, err
			}
			if snap.Tombstones == nil {
				snap.Tombstones = map[string]model.Tombstone{}
			}
			snap.Tombstones[k] = model.Tombstone{Key: k, At: at}
		}
	}
	items, err := readRecords(ctx, rc, recs, act)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		snap.Items[it.Key] = it
	}
	for k, name := range tmps {
		if it, ok := readCandidate(rc, k, name, act); ok {
			if cur, has := snap.Items[k]; !has || it.Stamp.AppTime >= cur.Stamp.AppTime {
				snap.Items[k] = it
			}
		}
	}
	return snap, nil
}

func readRecord(rc sources.ReadContext, key, name, act string) (model.Item, error) {
	rel := sources.SessionRel(rc.Replica, name)
	data, _, err := sources.ReadFile(rc.Folder, rel)
	if err != nil {
		return model.Item{}, err
	}
	obj, err := sources.DecodeObject(data, rel)
	if err != nil {
		return model.Item{}, err
	}
	at, _ := sources.IntMember(obj, act)
	it, err := model.NewItem(key, data, data, model.Stamp{AppTime: at}, false)
	if err != nil {
		return model.Item{}, fmt.Errorf("sessions: %s: %w", rel, err)
	}
	return it, nil
}

// readCandidate reads a .json.tmp candidate; anything unreadable is
// ignored (the app writes these files and may leave them partial).
func readCandidate(rc sources.ReadContext, key, name, act string) (model.Item, bool) {
	rel := sources.SessionRel(rc.Replica, name)
	data, present, err := sources.ReadFile(rc.Folder, rel)
	if err != nil || !present {
		return model.Item{}, false
	}
	obj, err := sources.DecodeObject(data, rel)
	if err != nil {
		return model.Item{}, false
	}
	at, _ := sources.IntMember(obj, act)
	it, err := model.NewItem(key, data, data, model.Stamp{AppTime: at}, false)
	return it, err == nil
}

// tombstoneTime reads a tombstone's time from its content: a bare JSON
// number of epoch ms (the form the app writes), or, for an older object
// form, the largest member named by fields. Anything else (empty,
// malformed, zero or negative) falls back to the file's mtime.
func tombstoneTime(rc sources.ReadContext, name string, fields []string) (int64, error) {
	rel := sources.SessionRel(rc.Replica, name)
	data, _, err := sources.ReadFile(rc.Folder, rel)
	if err != nil {
		return 0, err
	}
	if jsonx.Validate(data) == nil {
		var n float64
		if json.Unmarshal(data, &n) == nil && n > 0 {
			return int64(n), nil
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(data, &obj) == nil {
			if t := sources.MaxTime(obj, fields); t > 0 {
				return t, nil
			}
		}
	}
	return sources.FileMTimeMillis(rc.Folder, rel), nil
}

// sessionOps carries one Ops call's state.
type sessionOps struct {
	ctx     context.Context
	oc      sources.OpsContext
	ents    map[string]bool // names in the session directory
	removed map[string]bool
	res     sources.OpsResult
	changed bool
}

func (o *sessionOps) rel(name string) string { return sources.SessionRel(o.oc.Replica, name) }

// Ops implements sources.Adapter. Changed records are replaced whole
// (and an app .json.tmp candidate next to them is quarantined, so the app
// cannot promote it over the new record); records the desired snapshot
// lacks are quarantined with every local_<id>.* sibling; new tombstones
// are written (copied from the source when it has the file) and their
// sessions' siblings quarantined; tombstones the desired snapshot lacks
// are removed. archived-sessions.idx is regenerated as
// {"v":1,"archived":[sorted]} whenever records change or it disagrees
// with the records.
func (a Sessions) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	if err := ctx.Err(); err != nil {
		return sources.OpsResult{}, err
	}
	current, desired = sources.OrEmpty(current, model.RowSessions), sources.OrEmpty(desired, model.RowSessions)
	ents, err := dirEntries(oc.Folder, oc.Replica)
	if err != nil {
		return sources.OpsResult{}, err
	}
	o := &sessionOps{ctx: ctx, oc: oc, ents: map[string]bool{}, removed: map[string]bool{}}
	for _, e := range ents {
		o.ents[e.Name()] = true
	}
	if err := o.records(current, desired); err != nil {
		return o.res, err
	}
	if err := o.tombstones(current, desired); err != nil {
		return o.res, err
	}
	if err := o.index(desired); err != nil {
		return o.res, err
	}
	return o.res, nil
}

func unionKeys(a, b map[string]model.Item) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]model.Item{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (o *sessionOps) records(current, desired *model.RowSnapshot) error {
	for _, k := range unionKeys(current.Items, desired.Items) {
		c, hasC := current.Items[k]
		d, hasD := desired.Items[k]
		switch {
		case hasC && hasD && c.Hash == d.Hash:
			continue
		case hasD:
			if err := o.writeRecord(k, d.Value); err != nil {
				return err
			}
		default:
			if err := o.removeSiblings(k); err != nil {
				return err
			}
		}
		o.changed = true
	}
	return nil
}

func (o *sessionOps) writeRecord(k string, value json.RawMessage) error {
	name := k + sources.RecordSuffix
	doc, err := sources.LoadFile(o.oc.Folder, o.rel(name))
	if err != nil {
		return err
	}
	if _, err := sources.DecodeObject(value, "session "+k); err != nil {
		return err
	}
	b := sources.NewBuilder(doc, model.RowSessions)
	b.Replace(value, k, 0, false)
	o.res.Edits = append(o.res.Edits, b.Edits()...)
	if tmp := name + sources.TmpSuffix; o.ents[tmp] {
		return o.remove(tmp, k)
	}
	return nil
}

// removeSiblings quarantines local_<id>.json, local_<id>.json.tmp and
// every other local_<id>.* file.
func (o *sessionOps) removeSiblings(k string) error {
	names := make([]string, 0, len(o.ents))
	for n := range o.ents {
		if strings.HasPrefix(n, k+".") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if err := o.remove(n, k); err != nil {
			return err
		}
	}
	return nil
}

// remove quarantines one file of session item; an app .json.tmp
// candidate carries plan.ReasonAppTmpCandidate.
func (o *sessionOps) remove(name, item string) error {
	reason := ""
	if strings.HasSuffix(name, sources.RecordSuffix+sources.TmpSuffix) {
		reason = plan.ReasonAppTmpCandidate
	}
	return o.removeAs(name, item, reason)
}

// removeAs quarantines one file of session item with the given
// non-user-data reason ("" when the removal deletes user data).
func (o *sessionOps) removeAs(name, item, reason string) error {
	if o.removed[name] {
		return nil
	}
	doc, err := sources.LoadFile(o.oc.Folder, o.rel(name))
	if err != nil || !doc.Present {
		return err
	}
	b := sources.NewBuilder(doc, model.RowSessions)
	b.Remove(item, reason)
	o.res.Edits = append(o.res.Edits, b.Edits()...)
	o.removed[name] = true
	return nil
}

func (o *sessionOps) tombstones(current, desired *model.RowSnapshot) error {
	keys := make([]string, 0, len(desired.Tombstones))
	for k := range desired.Tombstones {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := desired.Items[k]; !ok {
			if err := o.removeSiblings(k); err != nil {
				return err
			}
		}
		if _, ok := current.Tombstones[k]; ok {
			continue
		}
		if err := o.writeTombstone(k, desired.Tombstones[k]); err != nil {
			return err
		}
		o.changed = true
	}
	for k := range current.Tombstones {
		if _, ok := desired.Tombstones[k]; ok {
			continue
		}
		// A revived session: its tombstone goes, or the app would hide it.
		name := sources.TombstonePrefix + strings.TrimPrefix(k, sources.RecordPrefix)
		if err := o.removeAs(name, k, plan.ReasonRevivedTombstone); err != nil {
			return err
		}
		o.changed = true
	}
	return nil
}

func (o *sessionOps) writeTombstone(k string, t model.Tombstone) error {
	name := sources.TombstonePrefix + strings.TrimPrefix(k, sources.RecordPrefix)
	if _, err := ids.ParseSession(k); err != nil {
		return fmt.Errorf("sessions: tombstone key is not a session id")
	}
	content, err := o.tombstoneContent(name, t)
	if err != nil {
		return err
	}
	doc, err := sources.LoadFile(o.oc.Folder, o.rel(name))
	if err != nil {
		return err
	}
	b := sources.NewBuilder(doc, model.RowSessions)
	b.Replace(content, k, 0, false)
	o.res.Edits = append(o.res.Edits, b.Edits()...)
	return nil
}

// tombstoneContent copies the source's tombstone bytes when they are a
// bare number, else writes At as a bare JSON number of epoch ms, the form
// the app writes. A zero At (time unknown) uses NowMillis.
func (o *sessionOps) tombstoneContent(name string, t model.Tombstone) ([]byte, error) {
	if src := o.oc.Source; src != nil {
		data, present, err := sources.ReadFile(src.Folder, sources.SessionRel(src.Replica, name))
		if err != nil {
			return nil, err
		}
		if present && bareMillis(data) {
			return data, nil
		}
	}
	at := t.At
	if at <= 0 {
		at = o.oc.NowMillis
	}
	return []byte(strconv.FormatInt(at, 10)), nil
}

// bareMillis reports whether data is a single positive JSON integer.
func bareMillis(data []byte) bool {
	s := strings.TrimSpace(string(data))
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0
}

// archiveIndex is the idx file's shape.
type archiveIndex struct {
	V        int      `json:"v"`
	Archived []string `json:"archived"`
}

func (o *sessionOps) index(desired *model.RowSnapshot) error {
	archived := []string{}
	for _, k := range desired.Keys() {
		obj, err := sources.DecodeObject(desired.Items[k].Value, "session "+k)
		if err != nil {
			return err
		}
		if sources.BoolMember(obj, sources.FieldArchived) {
			archived = append(archived, k)
		}
	}
	doc, err := sources.LoadFile(o.oc.Folder, o.rel(sources.ArchiveIndex))
	if err != nil {
		return err
	}
	cur, err := readIndex(doc)
	if err != nil {
		return err
	}
	if !o.changed && doc.Present && sameSet(cur, archived) {
		return nil
	}
	if !o.changed && !doc.Present && len(archived) == 0 {
		return nil
	}
	b, err := jsonx.Marshal(archiveIndex{V: IndexVersion, Archived: archived})
	if err != nil {
		return fmt.Errorf("sessions: encode %s: %w", sources.ArchiveIndex, err)
	}
	if doc.Present && string(b) == string(doc.Stored) {
		return nil
	}
	bl := sources.NewBuilder(doc, model.RowSessions)
	bl.Replace(b, "", 0, false)
	o.res.Edits = append(o.res.Edits, bl.Edits()...)
	return nil
}

// readIndex returns the archived ids of a present idx; any version other
// than 1 is refused.
func readIndex(doc sources.Doc) ([]string, error) {
	if !doc.Present {
		return nil, nil
	}
	if err := jsonx.Validate(doc.Text); err != nil {
		return nil, fmt.Errorf("sessions: %s: malformed JSON: %w", doc.Name(), err)
	}
	var idx struct {
		V        *int     `json:"v"`
		Archived []string `json:"archived"`
	}
	if json.Unmarshal(doc.Text, &idx) != nil {
		return nil, fmt.Errorf("sessions: %s: unexpected shape", doc.Name())
	}
	if idx.V == nil || *idx.V != IndexVersion {
		return nil, fmt.Errorf("sessions: %s: unknown version; refusing to rewrite it", doc.Name())
	}
	return idx.Archived, nil
}

func sameSet(a, b []string) bool {
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

type record struct{ key, name string }

// readRecords reads the records on all CPUs. Each is parsed and hashed
// in full (records reach hundreds of kilobytes), so a folder of a few
// hundred sessions is CPU-bound. The first error wins; the order of the
// result does not matter (the caller keys it).
func readRecords(ctx context.Context, rc sources.ReadContext, recs []record, act string) ([]model.Item, error) {
	out := make([]model.Item, len(recs))
	errs := make([]error, len(recs))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(runtime.NumCPU(), len(recs)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				out[i], errs[i] = readRecordCached(rc, recs[i].key, recs[i].name, act)
			}
		}()
	}
	for i := range recs {
		if ctx.Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readRecordCached reads a record, reusing the item parsed earlier in this
// process when the file is unchanged: same device, inode, size and
// modification time before and after that read. A switch plans twice,
// before and after Claude quits, and most records are untouched between
// the two. After a folder swap the same path names other inodes, so a
// stale entry is never reused.
func readRecordCached(rc sources.ReadContext, key, name, act string) (model.Item, error) {
	path := filepath.Join(rc.Folder, filepath.FromSlash(sources.SessionRel(rc.Replica, name)))
	before, ok := statKey(path, key, act)
	if ok {
		recMu.Lock()
		it, hit := recCache[before]
		recMu.Unlock()
		if hit {
			return it, nil
		}
	}
	it, err := readRecord(rc, key, name, act)
	if err != nil || !ok {
		return it, err
	}
	if after, ok := statKey(path, key, act); ok && after == before {
		recMu.Lock()
		if len(recCache) >= recCacheLimit {
			recCache = map[recKey]model.Item{}
		}
		recCache[before] = it
		recMu.Unlock()
	}
	return it, nil
}

type recKey struct {
	path, key, act string
	dev, ino       uint64
	size, mtime    int64
}

func statKey(path, key, act string) (recKey, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return recKey{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return recKey{}, false
	}
	return recKey{path: path, key: key, act: act, dev: uint64(st.Dev), ino: uint64(st.Ino), size: fi.Size(), mtime: fi.ModTime().UnixNano()}, true
}

const recCacheLimit = 20000

var (
	recMu    sync.Mutex
	recCache = map[recKey]model.Item{}
)
