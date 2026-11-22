package merge

import (
	"github.com/kazupc/paritydesk/internal/model"
)

// Side identifies a replica in a merge.
type Side int

// Sides.
const (
	Leaving  Side = iota // the account being left (S); live most recently
	Entering             // the account being switched to (T)
)

// Tombstone modes (sync.tombstones).
const (
	// TombstoneNewerWins deletes only when the tombstone is later than the
	// other copy's app time (D9); otherwise the item is revived.
	TombstoneNewerWins = "newer-wins"
	// TombstoneDeleteWins always lets a tombstone delete.
	TombstoneDeleteWins = "delete-wins"
)

// Reasons are the constant strings carried by Outcome.Reason.
const (
	ReasonEpoch          = "epoch"
	ReasonAppTime        = "app-time"
	ReasonLeavingSide    = "leaving-side"
	ReasonTombstoneNewer = "tombstone-newer"
	ReasonDeleteWins     = "delete-wins"
	ReasonRevived        = "revived"
	ReasonRestored       = "restored"
	ReasonFieldMerge     = "field-merge"
)

// Version is one side's view of one key.
type Version struct {
	Item      *model.Item // nil: absent on this side
	Base      *model.Item // nil: absent in this side's base
	BaseValid bool
	// Tomb is this side's tombstone for the key, if any.
	Tomb *model.Tombstone
	// Epoch is the epoch during which this side was last live. It stands
	// in for the item's stamp when the side removed the key (Item nil),
	// so a removal takes part in the epoch rule of Prefer.
	Epoch uint64
}

// Changed reports whether the side changed the key since its base. With a
// valid base the side changed when presence differs from the base or the
// item's hash differs from the base's. With an invalid base, presence
// counts as a change and absence does not.
func (v Version) Changed() bool {
	if !v.BaseValid {
		return v.Item != nil
	}
	if (v.Item == nil) != (v.Base == nil) {
		return true
	}
	return v.Item != nil && v.Item.Hash != v.Base.Hash
}

// Outcome is the merge result for one key.
type Outcome struct {
	Key    string
	Result *model.Item // nil: absent (deleted) after the merge
	Winner Side
	Kind   model.ChangeKind // relative to the entering side
	// Loser holds the losing version of a both-sides change.
	Loser *model.Item
	// Reason is a constant string for summaries (the Reason* constants).
	Reason string
}

// Prefer applies the D7 tie-break between two changed versions: (1) the
// higher epoch when both bases are valid; (2) otherwise the later app time
// from this Mac, ignoring versions whose time paritydesk wrote itself or
// whose time is unknown (zero); (3) the leaving side. Equal epochs and
// equal times fall through to the next rule.
func Prefer(leaving, entering Version) (Side, string) {
	ls, es := stampOf(leaving), stampOf(entering)
	if leaving.BaseValid && entering.BaseValid && ls.Epoch != es.Epoch {
		if ls.Epoch > es.Epoch {
			return Leaving, ReasonEpoch
		}
		return Entering, ReasonEpoch
	}
	lt, et := usableTime(ls), usableTime(es)
	if lt != et {
		if lt > et {
			return Leaving, ReasonAppTime
		}
		return Entering, ReasonAppTime
	}
	return Leaving, ReasonLeavingSide
}

// stampOf returns the stamp Prefer compares: the item's, or for a
// removal only the side's epoch (a removal carries no app time).
func stampOf(v Version) model.Stamp {
	if v.Item == nil {
		return model.Stamp{Epoch: v.Epoch}
	}
	return v.Item.Stamp
}

// usableTime returns the app time that may take part in a tie-break, or 0.
func usableTime(s model.Stamp) int64 {
	if s.ToolWritten || s.AppTime <= 0 {
		return 0
	}
	return s.AppTime
}

// Scalar merges one key (D7 rules, D9 tombstones). tombstoneMode is
// TombstoneNewerWins or TombstoneDeleteWins (anything else is newer-wins).
//
// Only one side changed: that side's version is taken. Both changed:
// Prefer decides and the loser is returned. A tombstone meeting a present
// copy on the other side deletes in newer-wins mode only if it is later
// than that copy's app time; otherwise the copy is revived. An item one
// side lacks without a tombstone and without a removal against a valid
// base is restored from the other side. While either base is invalid, a
// deletion never wins against a present copy (C1).
func Scalar(key string, leaving, entering Version, tombstoneMode string) Outcome {
	lc, ec := leaving.Changed(), entering.Changed()
	sc := scalar{l: leaving, e: entering, mode: tombstoneMode}
	var o Outcome
	switch {
	case !lc && !ec:
		o = sc.unchanged()
	case lc && !ec:
		o = sc.oneSided(Leaving)
	case ec && !lc:
		o = sc.oneSided(Entering)
	default:
		o = sc.bothChanged()
	}
	o.Key = key
	o.Kind = KindOf(entering.Item, o.Result, o.Kind)
	return o
}

type scalar struct {
	l, e Version
	mode string
}

func (s scalar) bothValid() bool { return s.l.BaseValid && s.e.BaseValid }

func (s scalar) side(sd Side) Version {
	if sd == Leaving {
		return s.l
	}
	return s.e
}

func other(sd Side) Side {
	if sd == Leaving {
		return Entering
	}
	return Leaving
}

// tombWins reports whether tombstone t deletes the present copy it meets.
func (s scalar) tombWins(t *model.Tombstone, copy *model.Item) bool {
	if !s.bothValid() {
		return false
	}
	if s.mode == TombstoneDeleteWins {
		return true
	}
	return t.At > copy.Stamp.AppTime
}

func (s scalar) tombReason() string {
	if s.mode == TombstoneDeleteWins {
		return ReasonDeleteWins
	}
	return ReasonTombstoneNewer
}

// unchanged handles a key neither side changed.
func (s scalar) unchanged() Outcome {
	switch {
	case s.e.Item != nil:
		return Outcome{Result: s.e.Item, Winner: Entering}
	case s.l.Item == nil:
		return Outcome{Winner: Entering}
	case s.e.Tomb != nil && s.tombWins(s.e.Tomb, s.l.Item):
		return Outcome{Winner: Entering, Reason: s.tombReason()}
	case s.e.Tomb != nil:
		return Outcome{Result: s.l.Item, Winner: Leaving, Kind: model.ChangeRevived, Reason: ReasonRevived}
	default:
		return Outcome{Result: s.l.Item, Winner: Leaving, Kind: model.ChangeRestored, Reason: ReasonRestored}
	}
}

// oneSided handles a key only side sd changed.
func (s scalar) oneSided(sd Side) Outcome {
	ch, ot := s.side(sd), s.side(other(sd))
	if ch.Item != nil {
		if ot.Item == nil && ot.Tomb != nil && s.tombWins(ot.Tomb, ch.Item) {
			return Outcome{Winner: other(sd), Reason: s.tombReason(), Loser: ch.Item}
		}
		if ot.Item == nil && ot.Tomb != nil {
			return Outcome{Result: ch.Item, Winner: sd, Kind: model.ChangeRevived, Reason: ReasonRevived}
		}
		return Outcome{Result: ch.Item, Winner: sd}
	}
	// ch removed the key against a valid base.
	if ot.Item == nil {
		return Outcome{Winner: sd}
	}
	return s.removal(sd, ch, ot, false)
}

// bothChanged handles a key both sides changed.
func (s scalar) bothChanged() Outcome {
	l, e := s.l, s.e
	switch {
	case l.Item != nil && e.Item != nil:
		if l.Item.Hash == e.Item.Hash {
			return Outcome{Result: e.Item, Winner: Entering}
		}
		w, why := Prefer(l, e)
		if w == Leaving {
			return Outcome{Result: l.Item, Winner: Leaving, Loser: e.Item, Reason: why}
		}
		return Outcome{Result: e.Item, Winner: Entering, Loser: l.Item, Reason: why}
	case l.Item != nil:
		return s.removal(Entering, e, l, true)
	case e.Item != nil:
		return s.removal(Leaving, l, e, true)
	default:
		return Outcome{Winner: Entering}
	}
}

// removal resolves a removal by side rs (version rem) against a present
// copy (version keep) on the other side. edited is true when the other
// side changed the copy itself.
func (s scalar) removal(rs Side, rem, keep Version, edited bool) Outcome {
	ks := other(rs)
	if !s.bothValid() {
		return Outcome{Result: keep.Item, Winner: ks, Kind: model.ChangeRestored, Reason: ReasonRestored}
	}
	if rem.Tomb != nil {
		if s.tombWins(rem.Tomb, keep.Item) {
			o := Outcome{Winner: rs, Reason: s.tombReason()}
			if edited {
				o.Loser = keep.Item
			}
			return o
		}
		return Outcome{Result: keep.Item, Winner: ks, Kind: model.ChangeRevived, Reason: ReasonRevived}
	}
	if !edited {
		return Outcome{Winner: rs}
	}
	var w Side
	var why string
	if rs == Leaving {
		w, why = Prefer(rem, keep)
	} else {
		w, why = Prefer(keep, rem)
	}
	if w == rs {
		return Outcome{Winner: rs, Loser: keep.Item, Reason: why}
	}
	return Outcome{Result: keep.Item, Winner: ks, Reason: why}
}

// KindOf classifies the change from before to after for the entering
// side. A hint of ChangeRevived or ChangeRestored is kept when after is
// present; otherwise the kind is derived from presence and hashes ("" for
// no change).
func KindOf(before, after *model.Item, hint model.ChangeKind) model.ChangeKind {
	if after != nil && (hint == model.ChangeRevived || (hint == model.ChangeRestored && before == nil)) {
		return hint
	}
	switch {
	case before == nil && after == nil:
		return ""
	case before == nil:
		return model.ChangeAdded
	case after == nil:
		return model.ChangeDeleted
	case before.Hash != after.Hash:
		return model.ChangeUpdated
	}
	return ""
}

// FieldMerger merges a record field by field. It returns the merged record
// value, or ok=false to fall back to whole-record last-writer-wins.
type FieldMerger func(base, leaving, entering []byte) (merged []byte, ok bool)

// Keyed merges keyed records: Scalar per key, with optional field-level
// merge for keys changed on both sides. The base handed to field is the
// leaving side's base when valid, else the entering side's, else nil. A
// field-merged result keeps the Prefer winner's stamp, secret flag and Raw.
func Keyed(keys []string, leaving, entering func(key string) Version, field FieldMerger, tombstoneMode string) []Outcome {
	out := make([]Outcome, 0, len(keys))
	for _, k := range keys {
		l, e := leaving(k), entering(k)
		o := Scalar(k, l, e, tombstoneMode)
		if field != nil && o.Loser != nil && o.Result != nil && l.Item != nil && e.Item != nil {
			if it, ok := fieldMerge(k, l, e, o.Result, field); ok {
				o.Result, o.Loser, o.Reason = it, nil, ReasonFieldMerge
				o.Kind = KindOf(e.Item, o.Result, "")
			}
		}
		out = append(out, o)
	}
	return out
}

func fieldMerge(key string, l, e Version, winner *model.Item, field FieldMerger) (*model.Item, bool) {
	var base []byte
	switch {
	case l.BaseValid && l.Base != nil:
		base = l.Base.Value
	case e.BaseValid && e.Base != nil:
		base = e.Base.Value
	}
	merged, ok := field(base, l.Item.Value, e.Item.Value)
	if !ok {
		return nil, false
	}
	it, err := model.NewItem(key, merged, winner.Raw, winner.Stamp, winner.Secret)
	if err != nil {
		return nil, false
	}
	return &it, true
}
