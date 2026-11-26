package pins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"sort"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/store/idb"
)

// Snapshot schema of row pins.
const schema = 1

// maxJSONDepth bounds the nesting of the decoded pin list.
const maxJSONDepth = 16

// Sources of a pins read (sources.ReadReport.PinsSource).
const (
	SourceIDB    = "idb"
	SourceMirror = "mirror"
)

// Options configure the adapter.
type Options struct {
	// Read reads the IndexedDB value; nil reads in place with
	// idb.ReadString (tests). The wiring passes idb.Cloner.Read.
	Read idb.Reader
}

// New returns the pins adapter.
func New(o Options) sources.Adapter {
	r := o.Read
	if r == nil {
		r = idb.ReadString
	}
	return Adapter{read: r}
}

// Adapter is the adapter of row pins.
type Adapter struct{ read idb.Reader }

// Row is model.RowPins.
func (Adapter) Row() model.RowID { return model.RowPins }

// Unavailable carries the reason a pin list cannot be used this time;
// errors.Is(err, sources.ErrSourceUnavailable) holds.
type Unavailable struct {
	Reason idb.Reason
	Cause  error
}

func (u *Unavailable) Error() string { return "pins: unavailable: " + string(u.Reason) }

// Unwrap returns sources.ErrSourceUnavailable and the cause.
func (u *Unavailable) Unwrap() []error {
	if u.Cause == nil {
		return []error{sources.ErrSourceUnavailable}
	}
	return []error{sources.ErrSourceUnavailable, u.Cause}
}

func unavailable(r idb.Reason, cause error) error { return &Unavailable{Reason: r, Cause: cause} }

// ReasonOf returns the reason of an Unavailable error ("" for any other).
func ReasonOf(err error) idb.Reason {
	var u *Unavailable
	if errors.As(err, &u) {
		return u.Reason
	}
	return ""
}

// Ops never edits: pins are applied only through the app's tools.
func (Adapter) Ops(context.Context, sources.OpsContext, *model.RowSnapshot, *model.RowSnapshot) (sources.OpsResult, error) {
	return sources.OpsResult{}, nil
}

// Read applies spec-pins 6.3.
func (a Adapter) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	spec := rc.Spec.IDB
	if spec == nil {
		return nil, unavailable(idb.ReasonFormat, nil)
	}
	re, err := regexp.Compile(spec.IDPattern)
	if err != nil {
		return nil, unavailable(idb.ReasonFormat, nil)
	}
	var (
		mirror     Mirror
		mOK, mRead bool
	)
	getMirror := func() (Mirror, bool) {
		if !mRead {
			mRead = true
			m, ok, err := ReadMirror(rc, spec)
			mirror, mOK = m, ok && err == nil
		}
		return mirror, mOK
	}
	report := func(src string, v idb.Value) {
		if rc.Report != nil {
			rc.Report.PinsSource, rc.Report.TailDropped, rc.Report.Copied = src, v.TailDropped, v.Copied
		}
	}
	side := rc.Side
	v, rerr := a.read(ctx, rc.Folder, spec.Reader())
	switch {
	case rerr == nil:
	case errors.Is(rerr, idb.ErrDirAbsent), errors.Is(rerr, idb.ErrKeyAbsent):
		// Rule 3: absence is "no pins" only where nothing says otherwise.
		reason := idb.ReasonOf(rerr)
		if side != nil && side.BaseValid && !side.BaseEmpty {
			return nil, unavailable(reason, rerr)
		}
		if m, ok := getMirror(); ok && len(m.IDs) > 0 {
			report(SourceMirror, idb.Value{})
			if rc.Report != nil {
				rc.Report.PinsReason = string(reason)
			}
			return snapshot(m.IDs)
		}
		if side != nil && len(side.StarredIDs) == 0 {
			report(SourceIDB, idb.Value{})
			return snapshot(nil)
		}
		return nil, unavailable(reason, rerr)
	case errors.Is(rerr, idb.ErrUnsupported), errors.Is(rerr, idb.ErrVersion),
		errors.Is(rerr, idb.ErrBackend), errors.Is(rerr, idb.ErrTooLarge):
		// Rule 4: a format the reader does not know: the mirror, if any.
		m, ok := getMirror()
		if !ok {
			return nil, unavailable(idb.ReasonOf(rerr), rerr)
		}
		if err := probeMirror(side, m, m.IDs); err != nil {
			return nil, err
		}
		if side != nil && side.ProbeSince > 0 && rc.Report != nil {
			rc.Report.ProbePassed = true
		}
		report(SourceMirror, idb.Value{})
		if rc.Report != nil {
			rc.Report.PinsReason = string(idb.ReasonOf(rerr))
		}
		return snapshot(m.IDs)
	default:
		// Rule 5: transient or damaged; the mirror may be ahead or behind.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, unavailable(idb.ReasonOf(rerr), rerr)
	}
	// Rule 6: readable.
	list, err := parseList(v.Text, spec, re)
	if err != nil {
		return nil, err
	}
	src, ids := SourceIDB, list.ids
	if side != nil && side.ProbeSince > 0 {
		// Rule 7: the freshness probe.
		if list.hasUpdated && list.updatedAt >= side.ProbeSince {
			if rc.Report != nil {
				rc.Report.ProbePassed = true
			}
		} else {
			m, ok := getMirror()
			fresh := ok && m.LSIDs != nil && m.LSTime >= side.ProbeSince && (m.From == mirrorLS || sameSet(m.IDs, m.LSIDs))
			if !fresh {
				return nil, unavailable(idb.ReasonStaleStore, nil)
			}
			src, ids = SourceMirror, m.IDs
			if rc.Report != nil {
				rc.Report.ProbePassed, rc.Report.PinsReason = true, string(idb.ReasonStaleStore)
			}
		}
	}
	if src == SourceIDB && rc.Report != nil {
		if m, ok := getMirror(); ok && !sameSet(m.IDs, ids) {
			rc.Report.MirrorDiffers = true
		}
	}
	report(src, v)
	return snapshot(ids)
}

// probeMirror applies rule 7 to a snapshot that came from the mirror.
func probeMirror(side *sources.SideFacts, m Mirror, used []string) error {
	if side == nil || side.ProbeSince <= 0 {
		return nil
	}
	if m.LSIDs != nil && m.LSTime >= side.ProbeSince && sameSet(used, m.LSIDs) {
		return nil
	}
	return unavailable(idb.ReasonStaleStore, nil)
}

// list is a parsed pin list.
type list struct {
	ids        []string
	updatedAt  int64
	hasUpdated bool
}

// parseList parses the stored JSON (rule 6).
func parseList(text string, spec *compat.IDBSpec, re *regexp.Regexp) (list, error) {
	var l list
	doc, err := decodeOne([]byte(text))
	if err != nil {
		return l, unavailable(idb.ReasonFormat, nil)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return l, unavailable(idb.ReasonFormat, nil)
	}
	ver, ok := intAt(obj, spec.VersionPath)
	if !ok || !slices.Contains(spec.Versions, int(ver)) {
		return l, unavailable(idb.ReasonVersion, nil)
	}
	raw, ok := at(obj, spec.IDsPath)
	arr, isArr := raw.([]any)
	if !ok || !isArr {
		return l, unavailable(idb.ReasonFormat, nil)
	}
	ids, n := filterIDs(arr, re)
	if n > spec.MaxItems {
		return l, unavailable(idb.ReasonTooLarge, nil)
	}
	l.ids = ids
	if len(spec.UpdatedPath) > 0 {
		l.updatedAt, l.hasUpdated = intAt(obj, spec.UpdatedPath)
	}
	return l, nil
}

// filterIDs keeps the strings matching re, deduplicated, in stored order,
// and returns how many matched (duplicates included).
func filterIDs(arr []any, re *regexp.Regexp) ([]string, int) {
	seen := map[string]bool{}
	var out []string
	n := 0
	for _, e := range arr {
		s, ok := e.(string)
		if !ok || !re.MatchString(s) {
			continue
		}
		n++
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, n
}

// decodeOne decodes exactly one JSON value with numbers kept as
// json.Number and a bounded depth.
func decodeOne(b []byte) (any, error) {
	if err := checkDepth(b); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("pins: trailing data")
	}
	return v, nil
}

// checkDepth refuses JSON nested deeper than maxJSONDepth (strings are
// skipped, so brackets inside them do not count).
func checkDepth(b []byte) error {
	depth, inStr, esc := 0, false, false
	for _, c := range b {
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '{' || c == '[':
			depth++
			if depth > maxJSONDepth {
				return errors.New("pins: nested too deeply")
			}
		case c == '}' || c == ']':
			depth--
		}
	}
	return nil
}

// at returns the value at path.
func at(obj map[string]any, path []string) (any, bool) {
	var cur any = obj
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// intAt returns the integer at path.
func intAt(obj map[string]any, path []string) (int64, bool) {
	v, ok := at(obj, path)
	if !ok {
		return 0, false
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
}

// snapshot builds the row's snapshot from ids.
func snapshot(ids []string) (*model.RowSnapshot, error) {
	s := &model.RowSnapshot{Row: model.RowPins, Schema: schema, Items: map[string]model.Item{}}
	for _, id := range ids {
		it, err := model.NewItem(id, json.RawMessage("true"), nil, model.Stamp{}, false)
		if err != nil {
			return nil, err
		}
		s.Items[id] = it
	}
	return s, nil
}

// sameSet reports whether a and b hold the same ids.
func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	sort.Strings(x)
	sort.Strings(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}
