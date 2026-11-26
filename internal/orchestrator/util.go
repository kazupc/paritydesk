package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// ErrCrashInjected is returned when Deps.Crash asked the run to stop as if
// the process had been killed. Nothing is cleaned up after it.
var ErrCrashInjected = errors.New("orchestrator: crash injected")

// Waits and polls of the orchestrator's own loops. Every wait goes
// through Deps.Clock, so tests run them instantly with clock.Fake.
const (
	busyPoll       = 2 * time.Second
	quiescePoll    = 500 * time.Millisecond
	quiesceTimeout = 30 * time.Second
	updateTimeout  = 10 * time.Minute
	verifyPoll     = time.Second
	verifyStable   = 20 * time.Second
	verifyTimeout  = 120 * time.Second
	// verifyRecordWindow is how long after a run a standalone verify may
	// still record drift; later differences may be the user's own edits.
	verifyRecordWindow = 15 * time.Minute
	verifyLogTail      = 4 << 20
	minFreeBytes       = 500 << 20
	freeSpaceFactor    = 3
	maxTitleRunes      = 48
	defaultQuitTimeout = 60 * time.Second
)

// claudePath returns the active data folder.
func (o *Orchestrator) claudePath() string { return filepath.Join(o.d.DataRoot, ids.DefaultFolder) }

// folderPath returns a folder under the data root.
func (o *Orchestrator) folderPath(name string) string { return filepath.Join(o.d.DataRoot, name) }

func (o *Orchestrator) now() time.Time { return o.d.Clock.Now() }

// progress reports an event to the UI, if any.
func (o *Orchestrator) progress(p Phase, status, msg string) {
	if o.d.UI != nil {
		o.d.UI.Progress(Event{Phase: p, Status: status, Message: msg})
	}
}

// EventAliases is the Event.Data key listing the profile aliases a
// message names, so a support bundle can pseudonymize them even after the
// profile left the config.
const EventAliases = "aliases"

// progressNaming is progress for a message that names profile aliases.
func (o *Orchestrator) progressNaming(p Phase, status, msg string, aliases ...ids.Alias) {
	if o.d.UI == nil {
		return
	}
	names := make([]string, 0, len(aliases))
	for _, a := range aliases {
		if a != "" {
			names = append(names, string(a))
		}
	}
	o.d.UI.Progress(Event{Phase: p, Status: status, Message: msg, Data: map[string]any{EventAliases: names}})
}

// interactive reports whether questions may be asked. The detached job
// never asks.
func (o *Orchestrator) interactive(fromRunner bool) bool {
	return !fromRunner && o.d.UI != nil && o.d.UI.Interactive()
}

// ask puts a question to the user. It returns nil on yes, a Declined
// error on no, and a Declined error without asking when no one can
// answer.
func (o *Orchestrator) ask(ctx context.Context, interactive bool, c Confirmation) error {
	if !interactive {
		return pderr.New(pderr.Declined, "confirm", "a confirmation is needed; run in a terminal or pass --yes")
	}
	ok, err := o.d.UI.Confirm(ctx, c)
	if err != nil {
		return err
	}
	if !ok {
		return pderr.New(pderr.Declined, "confirm", "declined")
	}
	return nil
}

// begin records a not-done intent, then consults the crash hook.
func (o *Orchestrator) begin(r *state.Root, p Phase, name state.IntentName, data map[string]string) error {
	if err := o.d.State.Begin(r, name, data); err != nil {
		return pderr.Wrap(pderr.Internal, "state", err)
	}
	return o.crashPoint(p)
}

// done marks an intent done, then consults the crash hook.
func (o *Orchestrator) done(r *state.Root, p Phase, name state.IntentName) error {
	if err := o.d.State.Done(r, name); err != nil {
		return pderr.Wrap(pderr.Internal, "state", err)
	}
	return o.crashPoint(p)
}

func (o *Orchestrator) crashPoint(p Phase) error {
	if o.d.Crash != nil && o.d.Crash(p) {
		return ErrCrashInjected
	}
	return nil
}

// commit writes the root.
func (o *Orchestrator) commit(r *state.Root) error {
	if err := o.d.State.Commit(r); err != nil {
		return pderr.Wrap(pderr.Internal, "state", err)
	}
	return nil
}

// uncancel returns the context used from the P5 commit on and on every
// abort, rollback and relaunch path.
func uncancel(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), CommitTimeout)
}

// quitTimeout returns the configured quit timeout.
func (o *Orchestrator) quitTimeout() time.Duration {
	if d := o.d.Config.App.QuitTimeout.Duration; d > 0 {
		return d
	}
	return defaultQuitTimeout
}

// loadRoot reads root.json, or returns a fresh root when none exists.
func (o *Orchestrator) loadRoot() (*state.Root, error) {
	r, err := o.d.State.Load()
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, "state", err)
	}
	if r == nil {
		r = state.NewRoot(o.d.DataRoot)
	}
	ensureMaps(r)
	return r, nil
}

func ensureMaps(r *state.Root) {
	if r.Profiles == nil {
		r.Profiles = map[ids.Alias]*state.ProfileState{}
	}
	if r.Bases == nil {
		r.Bases = map[string]state.Ref{}
	}
	if r.Canonical == nil {
		r.Canonical = map[model.RowID]state.Ref{}
	}
	if r.Overlays == nil {
		r.Overlays = map[string]state.Ref{}
	}
}

// checkBinding refuses a state bound to another data root (two-Mac rule).
func (o *Orchestrator) checkBinding(r *state.Root) error {
	if r.DataRoot != "" && filepath.Clean(r.DataRoot) != filepath.Clean(o.d.DataRoot) {
		return pderr.New(pderr.Usage, "state", "the state belongs to a different data root; pass the matching --state-dir")
	}
	return nil
}

// lsReader opens a snapshot of a folder's Local Storage, or returns nil
// when the folder has none.
func (o *Orchestrator) lsReader(ctx context.Context, folder string) (kv.Reader, error) {
	dir := sources.LevelDBPath(folder)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	r, err := o.d.KV.OpenSnapshot(ctx, dir)
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, "local storage", err)
	}
	return r, nil
}

// releaseLock releases the run lock; an error there leaves a stale lock
// file that the next flock replaces, so it is ignored.
func releaseLock(l *state.Lock) { _ = l.Release() }

func closeReader(r kv.Reader) {
	if r != nil {
		_ = r.Close()
	}
}

// folderSource returns a plan.Source over a folder's files and a Local
// Storage reader.
func folderSource(folder string, ls kv.Reader) plan.Source {
	return func(loc plan.Location) ([]byte, bool, error) {
		if loc.File != "" {
			return readContained(folder, loc.File)
		}
		if ls == nil {
			return nil, false, nil
		}
		k, err := localstorage.Key(loc.LSKey)
		if err != nil {
			return nil, false, err
		}
		v, found, err := ls.Get(k)
		if err != nil || !found {
			return nil, false, err
		}
		return append([]byte(nil), v...), true, nil
	}
}

// readContained reads root/rel, refusing escapes and symlinks.
func readContained(root, rel string) ([]byte, bool, error) {
	p, err := fsx.Contained(root, rel)
	if err != nil {
		return nil, false, err
	}
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, fmt.Errorf("orchestrator: %s is not a regular file", rel)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// dirSize sums the sizes of regular files under dir (0 when absent).
func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.Type().IsRegular() {
			if fi, err := e.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// sanitizeTitle makes an untrusted session title safe to show: control
// and format characters dropped, whitespace collapsed, length bounded.
func sanitizeTitle(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			continue
		}
		if n >= maxTitleRunes {
			b.WriteString("…")
			break
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
		n++
	}
	if b.Len() == 0 {
		return "(untitled)"
	}
	return b.String()
}

// parseSize parses sizes such as "2GiB", "500MB" or "1024".
func parseSize(s string) int64 {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		mult   int64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1},
	}
	for _, u := range units {
		if v, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil || n < 0 {
				return 0
			}
			return n * u.mult
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// sameItems reports whether two snapshots hold the same item hashes and
// order.
func sameItems(a, b *model.RowSnapshot) bool {
	return sameRead(a, b, true)
}

// sameRead reports whether two snapshots hold the same item hashes, and,
// when withOrder is set, the same order. Rows whose merge does not carry
// RowSnapshot.Order (rows.CarriesOrder) are compared without it.
func sameRead(a, b *model.RowSnapshot, withOrder bool) bool {
	ai, bi := itemsOf(a), itemsOf(b)
	if len(ai) != len(bi) {
		return false
	}
	for k, it := range ai {
		o, ok := bi[k]
		if !ok || o.Hash != it.Hash {
			return false
		}
	}
	if !withOrder {
		return true
	}
	var ao, bo []string
	if a != nil {
		ao = a.Order
	}
	if b != nil {
		bo = b.Order
	}
	return strings.Join(ao, "\x00") == strings.Join(bo, "\x00")
}

func itemsOf(s *model.RowSnapshot) map[string]model.Item {
	if s == nil {
		return nil
	}
	return s.Items
}

// postHashKey names a plan target the way apply.Result.PostHashes does:
// "dir:" for a directory row scratch creates, "link:" for a symlink,
// "path:" for every other file op and "ls:" for Local Storage.
func postHashKey(op plan.Op) string {
	switch {
	case op.Kind == plan.OpMkdir:
		return "dir:" + op.Path
	case op.Kind == plan.OpSymlink:
		return "link:" + op.Path
	case op.Path != "":
		return "path:" + op.Path
	}
	return "ls:" + op.Key
}

// postHashes maps every op's target to its post-hash ("" for removals).
func postHashes(p *plan.Plan) map[string]string {
	if p == nil || len(p.Ops) == 0 {
		return nil
	}
	m := make(map[string]string, len(p.Ops))
	for _, op := range p.Ops {
		m[postHashKey(op)] = op.PostHash
	}
	return m
}

// planFromPostHashes rebuilds a check-only plan from recorded post-hashes
// (recover uses it with Applier.Check).
func planFromPostHashes(ph map[string]string, target plan.Target) *plan.Plan {
	p := &plan.Plan{Version: plan.Version, Target: target}
	keys := make([]string, 0, len(ph))
	for k := range ph {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h := ph[k]
		switch {
		case strings.HasPrefix(k, "path:"):
			kind := plan.OpWriteFile
			if h == "" {
				kind = plan.OpQuarantine
			}
			p.Ops = append(p.Ops, plan.Op{Kind: kind, Path: strings.TrimPrefix(k, "path:"), PostHash: h})
		case strings.HasPrefix(k, "dir:"):
			p.Ops = append(p.Ops, plan.Op{Kind: plan.OpMkdir, Path: strings.TrimPrefix(k, "dir:"), PostHash: h})
		case strings.HasPrefix(k, "link:"):
			p.Ops = append(p.Ops, plan.Op{Kind: plan.OpSymlink, Path: strings.TrimPrefix(k, "link:"), PostHash: h})
		case strings.HasPrefix(k, "ls:"):
			kind := plan.OpLSPut
			if h == "" {
				kind = plan.OpLSDelete
			}
			p.Ops = append(p.Ops, plan.Op{Kind: kind, Key: strings.TrimPrefix(k, "ls:"), PostHash: h})
		}
	}
	return p
}

// hasLSOps reports whether a plan writes Local Storage.
func hasLSOps(p *plan.Plan) bool {
	if p == nil {
		return false
	}
	for _, op := range p.Ops {
		if op.Kind == plan.OpLSPut || op.Kind == plan.OpLSDelete {
			return true
		}
	}
	return false
}

// joinRows and splitRows store a row list in intent data.
func joinRows(rs []model.RowID) string {
	s := make([]string, len(rs))
	for i, r := range rs {
		s[i] = string(r)
	}
	return strings.Join(s, ",")
}

func splitRows(s string) []model.RowID {
	if s == "" {
		return nil
	}
	var out []model.RowID
	for _, p := range strings.Split(s, ",") {
		out = append(out, model.RowID(p))
	}
	return out
}

// contestedFor returns the contested entries of one base key by item.
func contestedFor(r *state.Root, k state.BaseKey) map[string]model.Contested {
	prefix := k.String() + "/"
	var out map[string]model.Contested
	for key, c := range r.Contested {
		if item, ok := strings.CutPrefix(key, prefix); ok {
			if out == nil {
				out = map[string]model.Contested{}
			}
			out[item] = c
		}
	}
	return out
}

// dropBase invalidates one base (a BaseKey string) together with its
// contested entries: a contested value only means "not an edit" relative
// to the base it was recorded against.
func dropBase(r *state.Root, key string) {
	delete(r.Bases, key)
	prefix := key + "/"
	for k := range r.Contested {
		if strings.HasPrefix(k, prefix) {
			delete(r.Contested, k)
		}
	}
}

// dropAliasKeys removes every map entry keyed by one profile's BaseKeys.
func dropAliasKeys(r *state.Root, a ids.Alias) {
	prefix := string(a) + "/"
	for k := range r.Bases {
		if strings.HasPrefix(k, prefix) {
			delete(r.Bases, k)
		}
	}
	for k := range r.Overlays {
		if strings.HasPrefix(k, prefix) {
			delete(r.Overlays, k)
		}
	}
	for k := range r.ToolStamps {
		if strings.HasPrefix(k, prefix) {
			delete(r.ToolStamps, k)
		}
	}
	for k := range r.Contested {
		if strings.HasPrefix(k, prefix) {
			delete(r.Contested, k)
		}
	}
}

// expandedLSKeys lists the expanded ls_keys of rows for a replica.
func expandedLSKeys(m *compat.Manifest, rows []model.RowID, rep model.Replica) []string {
	var out []string
	for _, r := range rows {
		if spec, ok := m.Row(r); ok {
			out = append(out, spec.Expand(rep).LSKeys...)
		}
	}
	if len(out) == 0 {
		// plan.Validate enforces the list only when it is non-empty: a
		// run whose rows name no Local Storage item allows none.
		return []string{nothingAllowed}
	}
	return out
}

// nothingAllowed is an allow-list entry that matches no real name, so an
// otherwise empty allow list still refuses everything.
const nothingAllowed = ".paritydesk-nothing-is-allowed"

// latestGeneration returns the highest generation number of a switch or
// sync in state. Generations kept for a purpose (leftovers moved aside by
// profile adopt --from-legacy) are skipped, so a bare rollback never picks
// one.
func latestGeneration(r *state.Root) uint64 {
	var n uint64
	for _, g := range r.Generations {
		if g.Purpose == "" {
			n = max(n, g.N)
		}
	}
	return n
}

// appendReplica adds rep to list unless present.
func appendReplica(list []model.Replica, rep model.Replica) []model.Replica {
	for _, r := range list {
		if r == rep {
			return list
		}
	}
	return append(list, rep)
}

// appendAccount adds a to list unless present or empty.
func appendAccount(list []ids.AccountID, a ids.AccountID) []ids.AccountID {
	if a == "" {
		return list
	}
	for _, x := range list {
		if x == a {
			return list
		}
	}
	return append(list, a)
}
