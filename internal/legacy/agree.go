package legacy

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"

	"github.com/kazupc/paritydesk/internal/model"
)

// ErrNoSnapshot is returned by Agree for an empty input or a nil snapshot.
var ErrNoSnapshot = errors.New("legacy: agree needs one snapshot per folder")

// SeedStats counts one row's keys: Agreed are held by every folder with
// equal projections; Differing are held by at least one folder and not
// agreed.
type SeedStats struct {
	Agreed    int `json:"agreed"`
	Differing int `json:"differing"`
}

// projectionHash is the value compared between folders: for a JSON object,
// the item hash of the value without the top-level members named by
// ignore; for any other value (or no ignore list) the item's own hash.
func projectionHash(it model.Item, ignore []string) string {
	if len(ignore) == 0 {
		return it.Hash
	}
	v := bytes.TrimSpace(it.Value)
	if len(v) == 0 || v[0] != '{' {
		return it.Hash
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(v, &m); err != nil {
		return it.Hash
	}
	for _, f := range ignore {
		delete(m, f)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return it.Hash
	}
	h, err := model.ItemHash(b)
	if err != nil {
		return it.Hash
	}
	return "p:" + h
}

// agreedKeys returns the keys every snapshot holds, none lists as stale,
// and whose projections are equal in all.
func agreedKeys(snaps []*model.RowSnapshot, ignore []string) []string {
	var out []string
	for k, it := range snaps[0].Items {
		want := projectionHash(it, ignore)
		ok := true
		for _, s := range snaps {
			o, held := s.Items[k]
			if !held || slices.Contains(s.Stale, k) || projectionHash(o, ignore) != want {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func checkSnaps(snaps []*model.RowSnapshot) error {
	if len(snaps) == 0 {
		return ErrNoSnapshot
	}
	for _, s := range snaps {
		if s == nil {
			return ErrNoSnapshot
		}
	}
	return nil
}

// Agree returns one seeded base per snapshot, from the values every
// folder agrees on: base i holds snapshot i's own item (Value, Hash,
// Stamp; never Raw) for every agreed key, so each side counts as unchanged
// against its own base. Order is snapshot 0's when every snapshot has the
// same order, else nil. Tombstones are kept for keys every snapshot has a
// tombstone for with the same time.
func Agree(snaps []*model.RowSnapshot, ignore []string) ([]*model.RowSnapshot, error) {
	if err := checkSnaps(snaps); err != nil {
		return nil, err
	}
	keys := agreedKeys(snaps, ignore)
	sameOrder := true
	for _, s := range snaps[1:] {
		if !slices.Equal(s.Order, snaps[0].Order) {
			sameOrder = false
		}
	}
	tombs := map[string]bool{}
	for k, t := range snaps[0].Tombstones {
		all := true
		for _, s := range snaps[1:] {
			if o, ok := s.Tombstones[k]; !ok || o.At != t.At {
				all = false
				break
			}
		}
		if all {
			tombs[k] = true
		}
	}
	out := make([]*model.RowSnapshot, len(snaps))
	for i, s := range snaps {
		b := &model.RowSnapshot{Row: s.Row, Schema: s.Schema, Items: make(map[string]model.Item, len(keys))}
		for _, k := range keys {
			it := s.Items[k]
			it.Value = append(json.RawMessage(nil), it.Value...)
			it.Raw = nil
			b.Items[k] = it
		}
		if sameOrder && len(snaps[0].Order) > 0 {
			b.Order = append([]string(nil), snaps[0].Order...)
		}
		if len(tombs) > 0 {
			b.Tombstones = make(map[string]model.Tombstone, len(tombs))
			for k := range tombs {
				b.Tombstones[k] = s.Tombstones[k]
			}
		}
		out[i] = b
	}
	return out, nil
}

// Stats counts the agreed and differing keys of one row.
func Stats(snaps []*model.RowSnapshot, ignore []string) (SeedStats, error) {
	if err := checkSnaps(snaps); err != nil {
		return SeedStats{}, err
	}
	all := map[string]bool{}
	for _, s := range snaps {
		for k := range s.Items {
			all[k] = true
		}
	}
	n := len(agreedKeys(snaps, ignore))
	return SeedStats{Agreed: n, Differing: len(all) - n}, nil
}
