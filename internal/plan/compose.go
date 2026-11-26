package plan

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

const composeOp = "plan/compose"

// composeErr returns an Internal error naming the location only. msg and
// the wrapped cause never carry document content: jsonx and localstorage
// errors name offsets and constant reasons.
func composeErr(loc Location, err error, format string, args ...any) *pderr.Error {
	return &pderr.Error{Code: pderr.Internal, Op: composeOp,
		Msg: loc.String() + ": " + fmt.Sprintf(format, args...), Err: err}
}

// String names a location for messages and result keys: "path:<file>" or
// "ls:<name>". It never includes content.
func (l Location) String() string {
	if l.LSKey != "" {
		return "ls:" + l.LSKey
	}
	return "path:" + l.File
}

// group is the edits of one location, in input order.
type group struct {
	loc   Location
	edits []Edit
}

// groupByLocation groups edits by location in order of first appearance.
func groupByLocation(edits []Edit) ([]group, error) {
	var groups []group
	index := map[Location]int{}
	for i, e := range edits {
		if (e.Loc.File == "") == (e.Loc.LSKey == "") {
			return nil, &pderr.Error{Code: pderr.Internal, Op: composeOp,
				Msg: fmt.Sprintf("edit %d (row %s): location must name exactly one file or Local Storage key", i, e.Row)}
		}
		j, ok := index[e.Loc]
		if !ok {
			j = len(groups)
			index[e.Loc] = j
			groups = append(groups, group{loc: e.Loc})
		}
		groups[j].edits = append(groups[j].edits, e)
	}
	return groups, nil
}

// Compose folds edits into exactly one op per location. edits must be in
// model.AllRows order (the orchestrator concatenates adapter results in
// that order); edits of one location are applied in that order to the
// original bytes from src. For each location it:
//
//   - checks every edit's BaseHash against the original bytes;
//   - refuses EditRemove combined with any other edit, and set or delete
//     edits from different rows whose paths are equal or one is a prefix
//     of the other (overlapping splices); an EditReplace counts as the
//     empty path, so it may not be combined with edits of other rows;
//   - for Local Storage, decodes the value (refusing values that do not
//     re-encode byte for byte), applies the edits to the text, and
//     re-encodes it with localstorage.EncodeString;
//   - emits OpWriteFile / OpLSPut with PreHash = sha256(original) ("" when
//     absent) and PostHash = sha256(Content), or OpQuarantine /
//     OpLSDelete for a removal; and drops the op when the composed bytes
//     equal the original.
//
// Row scratch's edits (copy, mkdir, symlink, and its removals) are
// composed without reading the location: exactly one edit per location,
// PreHash = the edit's BaseHash, PostHash = Copy.Hash, DirHash(Mode) or
// LinkHash(Link) (composeScratch). Apply re-checks every pre-hash, so
// trusting BaseHash cannot write over unexpected bytes.
//
// Ops are returned in order of each location's first edit. Errors are
// *pderr.Error with code Internal and never contain content.
func Compose(edits []Edit, src Source) ([]Op, error) {
	if src == nil {
		return nil, pderr.Bug(composeOp, "no source")
	}
	groups, err := groupByLocation(edits)
	if err != nil {
		return nil, err
	}
	var ops []Op
	for _, g := range groups {
		if slices.ContainsFunc(g.edits, isScratchEdit) {
			op, err := composeScratch(g)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			continue
		}
		op, ok, err := composeLocation(g, src)
		if err != nil {
			return nil, err
		}
		if ok {
			ops = append(ops, op)
		}
	}
	return ops, nil
}

// composeLocation composes one location's edits. ok is false when the
// edits leave the location unchanged.
func composeLocation(g group, src Source) (Op, bool, error) {
	original, present, err := src(g.loc)
	if err != nil {
		return Op{}, false, composeErr(g.loc, err, "reading the original failed")
	}
	pre := ""
	if present {
		pre = model.HashBytes(original)
	}
	if err := checkEdits(g, pre); err != nil {
		return Op{}, false, err
	}
	op, err := newOp(g)
	if err != nil {
		return Op{}, false, err
	}
	if g.edits[0].Kind == EditRemove {
		if !present {
			return Op{}, false, nil
		}
		op.PreHash = pre
		if g.loc.LSKey != "" {
			op.Kind = OpLSDelete
		} else {
			op.Kind = OpQuarantine
		}
		return op, true, nil
	}
	content, exists, err := composeContent(g, original, present)
	if err != nil {
		return Op{}, false, err
	}
	if !exists || (present && bytes.Equal(content, original)) {
		return Op{}, false, nil
	}
	op.Content, op.PreHash, op.PostHash = content, pre, model.HashBytes(content)
	return op, true, nil
}

// checkEdits checks base hashes, edit kinds, the remove rule and the
// overlap rule of one location.
func checkEdits(g group, pre string) error {
	for i, e := range g.edits {
		if e.BaseHash != pre {
			return composeErr(g.loc, nil, "edit %d (row %s) was computed against other bytes (stale base)", i, e.Row)
		}
		switch e.Kind {
		case EditSet, EditDelete, EditReplace:
		case EditRemove:
			if len(g.edits) > 1 {
				return composeErr(g.loc, nil, "a removal (row %s) may not be combined with other edits", e.Row)
			}
		default:
			return composeErr(g.loc, nil, "edit %d (row %s) has unknown kind %q", i, e.Row, e.Kind)
		}
	}
	return checkOverlaps(g)
}

// checkOverlaps refuses edits of different rows whose paths are equal or
// nested. A replace edit counts as the empty path.
func checkOverlaps(g group) error {
	for i, a := range g.edits {
		for _, b := range g.edits[i+1:] {
			if a.Row == b.Row {
				continue
			}
			if isPrefix(editPath(a), editPath(b)) || isPrefix(editPath(b), editPath(a)) {
				return composeErr(g.loc, nil, "rows %s and %s edit overlapping paths", a.Row, b.Row)
			}
		}
	}
	return nil
}

func editPath(e Edit) jsonx.Path {
	if e.Kind == EditReplace {
		return nil
	}
	return e.Path
}

// isPrefix reports whether p is a prefix of q (or equal to it).
func isPrefix(p, q jsonx.Path) bool {
	if len(p) > len(q) {
		return false
	}
	for i := range p {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}

// newOp fills the op fields derived from the edits: target, rows, items,
// mode, flags and reason.
func newOp(g group) (Op, error) {
	op := Op{Path: g.loc.File, Key: g.loc.LSKey, Kind: OpWriteFile}
	if g.loc.LSKey != "" {
		op.Kind = OpLSPut
	}
	seenRow, seenItem := map[model.RowID]bool{}, map[string]bool{}
	for _, e := range g.edits {
		if !seenRow[e.Row] {
			seenRow[e.Row] = true
			op.Rows = append(op.Rows, e.Row)
		}
		if e.Item != "" && !seenItem[e.Item] {
			seenItem[e.Item] = true
			op.Items = append(op.Items, e.Item)
		}
		if op.Mode == 0 {
			op.Mode = e.Mode
		}
		op.Destructive = op.Destructive || e.Destructive
		op.Secret = op.Secret || e.Secret
		if e.Reason != "" {
			if op.Reason != "" && op.Reason != e.Reason {
				return Op{}, composeErr(g.loc, nil, "edits carry different reasons")
			}
			op.Reason = e.Reason
		}
	}
	op.Row = op.Rows[0]
	if len(op.Rows) == 1 {
		op.Rows = nil
	}
	return op, nil
}

// composeContent applies the set, delete and replace edits and returns
// the new stored bytes. exists is false when the location stays absent.
func composeContent(g group, original []byte, present bool) ([]byte, bool, error) {
	doc := original
	if g.loc.LSKey != "" && present {
		text, err := decodeLS(original)
		if err != nil {
			return nil, false, composeErr(g.loc, err, "Local Storage value cannot be edited")
		}
		doc = text
	}
	doc, exists, err := applyEdits(g, doc, present)
	if err != nil || !exists {
		return nil, exists, err
	}
	if g.loc.LSKey == "" {
		return doc, true, nil
	}
	if !utf8.Valid(doc) {
		return nil, false, composeErr(g.loc, nil, "composed Local Storage text is not valid UTF-8")
	}
	return localstorage.EncodeString(string(doc)), true, nil
}

// decodeLS returns the text of an encoded Local Storage value, refusing
// values that do not re-encode byte for byte.
func decodeLS(raw []byte) ([]byte, error) {
	v, err := localstorage.Decode(raw)
	if err != nil {
		return nil, err
	}
	text, err := v.String()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(localstorage.EncodeString(text), raw) {
		return nil, localstorage.ErrNotRoundTrip
	}
	return []byte(text), nil
}

// applyEdits applies the edits in order to doc.
func applyEdits(g group, doc []byte, exists bool) ([]byte, bool, error) {
	for i, e := range g.edits {
		var err error
		switch e.Kind {
		case EditReplace:
			doc, exists = append([]byte(nil), e.Value...), true
		case EditSet:
			if !exists {
				return nil, false, composeErr(g.loc, nil, "edit %d (row %s) sets a path in an absent document", i, e.Row)
			}
			doc, err = jsonx.Set(doc, e.Path, e.Value)
		case EditDelete:
			if !exists {
				continue
			}
			var out []byte
			out, err = jsonx.Delete(doc, e.Path)
			if errors.Is(err, jsonx.ErrNotFound) {
				err = nil
				continue
			}
			doc = out
		}
		if err != nil {
			return nil, false, composeErr(g.loc, err, "edit %d (row %s) failed", i, e.Row)
		}
	}
	return doc, exists, nil
}

// composeScratch composes the single row-scratch edit of a location into
// its op without reading the location.
func composeScratch(g group) (Op, error) {
	if len(g.edits) != 1 {
		return Op{}, composeErr(g.loc, nil, "a scratch edit may not be combined with other edits")
	}
	e := g.edits[0]
	if g.loc.LSKey != "" {
		return Op{}, composeErr(g.loc, nil, "a scratch edit (row %s) may not name a Local Storage key", e.Row)
	}
	op := Op{Row: e.Row, Path: g.loc.File, Mode: e.Mode, PreHash: e.BaseHash, Destructive: e.Destructive,
		Secret: e.Secret, Reason: e.Reason}
	if e.Item != "" {
		op.Items = []string{e.Item}
	}
	switch e.Kind {
	case EditCopy:
		if e.Copy == nil {
			return Op{}, composeErr(g.loc, nil, "a copy (row %s) has no source", e.Row)
		}
		c := *e.Copy
		op.Kind, op.Copy, op.PostHash = OpCopyFile, &c, c.Hash
	case EditMkdir:
		op.Kind, op.PostHash = OpMkdir, DirHash(e.Mode)
	case EditSymlink:
		op.Kind, op.Link, op.PostHash = OpSymlink, e.Link, LinkHash(e.Link)
	case EditRemove:
		op.Kind, op.Destructive = OpQuarantine, true
	default:
		return Op{}, composeErr(g.loc, nil, "edit (row %s) has unknown kind %q", e.Row, e.Kind)
	}
	return op, nil
}
