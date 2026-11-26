package online

import (
	"encoding/json"

	"github.com/kazupc/paritydesk/internal/model"
)

// PinsConfirm is the pins part of a confirmation (spec-pins 9).
type PinsConfirm struct {
	// Base is T's new pins base (nil when there was none).
	Base *model.RowSnapshot
	// Confirmed are pin step ids to append to Root.Online.Confirmed.
	Confirmed []StepID
	// Changed counts the confirmed done steps that changed the pin list
	// (spec-pins 9.3 sets the freshness probe when it is above 0).
	Changed int
	// Moved reports whether Base differs from the base given.
	Moved bool
}

// PinsBaseAfter returns T's new pins base from the P10 base and the
// resolved pin steps of the queue (spec-pins 9.1). Steps already in
// confirmed are not applied again. ever lists removal step ids that were
// ever recorded as failed(no_result) or failed(unknown_outcome), so a late
// "done" stays "unknown" here. It is pure and idempotent.
//
// Changed counts confirmed done steps whose effect changed the base's
// membership (an add or keep of an id the base lacked, a removal of an id
// it held). That is the step's "changed" answer as far as the base can
// tell: the base is T's state at P10, before the launch, and the queue
// was created after it, so a step the app answered "already" for an id
// the base did not hold means some write after the launch put it there,
// which also moved the store's updatedAt past the queue's creation (the
// probe still passes).
func PinsBaseAfter(base *model.RowSnapshot, steps []Step, confirmed map[StepID]bool, ever map[StepID]bool) (PinsConfirm, error) {
	var pc PinsConfirm
	if base == nil {
		return pc, nil
	}
	b := base.Clone()
	if b.Items == nil {
		b.Items = map[string]model.Item{}
	}
	doneSet := map[string]bool{} // ids with a done add or keep
	for _, s := range steps {
		d, in, ok := decodePin(s)
		if !ok {
			continue
		}
		if s.Status == StatusDone && d.Op != PinRemove {
			doneSet[in.SessionID] = true
		}
		if confirmed[s.ID] {
			continue
		}
		if s.Status == StatusDone {
			switch d.Op {
			case PinAdd, PinKeep:
				if _, had := b.Items[in.SessionID]; !had {
					it, err := model.NewItem(in.SessionID, json.RawMessage("true"), nil, model.Stamp{}, false)
					if err != nil {
						return PinsConfirm{}, err
					}
					b.Items[in.SessionID] = it
					pc.Changed++
				}
			case PinRemove:
				if _, had := b.Items[in.SessionID]; had {
					delete(b.Items, in.SessionID)
					pc.Changed++
				}
			}
		}
		if s.Status.Terminal() {
			pc.Confirmed = append(pc.Confirmed, s.ID)
		}
	}
	// Cascade risk, re-applied on every call (idempotent).
	for _, s := range steps {
		d, _, ok := decodePin(s)
		if !ok || d.Op != PinRemove {
			continue
		}
		unknown := ever[s.ID] || s.EverUnknown ||
			(s.Status == StatusFailed && (s.Reason == ReasonNoResult || s.Reason == ReasonUnknownOutcome))
		definite := s.Status == StatusDone && !unknown
		for _, y := range d.Desc {
			switch {
			case unknown:
				delete(b.Items, y)
			case definite && !doneSet[y]:
				delete(b.Items, y)
			}
		}
	}
	pc.Base = b
	pc.Moved = !sameKeys(b, base)
	return pc, nil
}

func sameKeys(a, b *model.RowSnapshot) bool {
	if len(a.Items) != len(b.Items) {
		return false
	}
	for k := range a.Items {
		if _, ok := b.Items[k]; !ok {
			return false
		}
	}
	return true
}

// EverUnknown returns the steps of a queue that were ever recorded as
// failed(no_result) or failed(unknown_outcome).
func EverUnknown(steps []Step) map[StepID]bool {
	out := map[StepID]bool{}
	for _, s := range steps {
		if s.EverUnknown {
			out[s.ID] = true
		}
	}
	return out
}
