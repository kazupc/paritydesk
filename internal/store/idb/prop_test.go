package idb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/store/idb/idbtest"
)

// event is one write batch of the property test and what it means for
// the pin key.
type event struct {
	ops     []idbtest.Op
	pin     int // 0: no pin change, 1: put, 2: delete
	pinText string
}

func genEvent(t *rapid.T, i int) event {
	switch rapid.IntRange(0, 6).Draw(t, "kind"+strconv.Itoa(i)) {
	case 0, 1:
		n := rapid.IntRange(0, 4).Draw(t, "ids"+strconv.Itoa(i))
		var ids []string
		for j := 0; j < n; j++ {
			ids = append(ids, idbtest.FakeID(rapid.IntRange(1, 50).Draw(t, "id")))
		}
		text := idbtest.PinJSON(ids)
		return event{ops: []idbtest.Op{idbtest.Put(dk(), idbtest.PinValue(ids)), idbtest.Put(idbtest.ExistsKey(6, 1, idbtest.PinKey), idbtest.Varint(uint64(i)))}, pin: 1, pinText: text}
	case 2:
		return event{ops: []idbtest.Op{idbtest.Del(dk()), idbtest.Del(idbtest.ExistsKey(6, 1, idbtest.PinKey))}, pin: 2}
	case 3: // another database's copy of the key
		return event{ops: []idbtest.Op{idbtest.Put(idbtest.DataKey(9, 1, idbtest.PinKey), idbtest.PinValue([]string{idbtest.FakeID(99)}))}}
	case 4: // a finished scope transaction naming the key
		k := idbtest.ScopeUndoKey(uint64(i), 0)
		return event{ops: []idbtest.Op{idbtest.Put(k, idbtest.ProtoBytes(1, dk())), idbtest.Del(k)}}
	case 5: // a live scope entry of another store
		return event{ops: []idbtest.Op{idbtest.Put(idbtest.ScopeUndoKey(1000+uint64(i), 0), idbtest.ProtoBytes(1, idbtest.DataKey(9, 9, "x")))}}
	default: // other keys of the store
		return event{ops: []idbtest.Op{idbtest.Put(idbtest.DataKey(6, 1, "other"), []byte("v"))}}
	}
}

func TestPropRead(tt *testing.T) {
	base := tt.TempDir()
	iter := 0
	rapid.Check(tt, func(t *rapid.T) {
		iter++
		n := rapid.IntRange(1, 24).Draw(t, "n")
		events := make([]event, n)
		for i := range events {
			events[i] = genEvent(t, i)
		}
		head := idbtest.Batch{Seq: 1, Ops: append(meta(), idbtest.Put(idbtest.DatabaseNameKey(idbtest.Origin, "other-db"), idbtest.Varint(9)))}
		tableCut := rapid.IntRange(0, n).Draw(t, "tableCut")
		snap := rapid.Bool().Draw(t, "snappy")
		seq := uint64(10)
		var entries []idbtest.TableEntry
		var batches []idbtest.Batch
		for i, e := range events {
			b := idbtest.Batch{Seq: seq, Ops: e.ops}
			if i < tableCut {
				for j, op := range e.ops {
					entries = append(entries, idbtest.TableEntry{Op: op, Seq: seq + uint64(j)})
				}
			} else {
				batches = append(batches, b)
			}
			seq += uint64(len(e.ops))
		}
		split := rapid.IntRange(0, len(batches)).Draw(t, "logSplit")
		logs := []idbtest.Log{{Num: 7, Batches: append([]idbtest.Batch{head}, batches[:split]...)}, {Num: 8, Batches: batches[split:]}}
		torn := rapid.Bool().Draw(t, "torn") && len(batches[split:]) > 0
		lost := -1
		if torn {
			logs[1].Cut = rapid.IntRange(1, 5).Draw(t, "cut")
			lost = n - 1
		}
		layout := idbtest.Layout{Logs: logs, LogNum: 7}
		if len(entries) > 0 {
			layout.Tables = []idbtest.Table{{Num: 5, Entries: entries, Snappy: snap, BlockSize: 256}}
		}
		dir := filepath.Join(base, strconv.Itoa(iter))
		defer os.RemoveAll(dir)
		if _, err := idbtest.Write(filepath.Join(dir, idbtest.Dir), layout); err != nil {
			t.Fatal(err)
		}
		want, absent := "", true
		for i, e := range events {
			if i == lost {
				break
			}
			switch e.pin {
			case 1:
				want, absent = e.pinText, false
			case 2:
				want, absent = "", true
			}
		}
		v, err := ReadString(context.Background(), dir, testSpec())
		if absent {
			if !errors.Is(err, ErrKeyAbsent) {
				t.Fatalf("want absent, got %+v, %v", v, err)
			}
			return
		}
		if err != nil || v.Text != want || v.TailDropped != torn {
			t.Fatalf("got %q %v (torn %v), want %q", v.Text, err, v.TailDropped, want)
		}
	})
}

func TestPropCodec(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		db := rapid.Uint64().Draw(t, "db")
		os := rapid.Uint64().Draw(t, "os")
		idx := rapid.Uint64Range(0, 1<<32-1).Draw(t, "idx")
		gdb, gos, gidx, rest, ok := decodePrefix(encodePrefix(db, os, idx))
		if !ok || gdb != db || gos != os || gidx != idx || len(rest) != 0 {
			t.Fatalf("prefix round trip (%d,%d,%d)", db, os, idx)
		}
		s := rapid.StringN(0, 64, -1).Draw(t, "s")
		got, rest2, ok := readStringWithLength(stringWithLength(s))
		if !ok || got != s || len(rest2) != 0 {
			t.Fatalf("string round trip %q -> %q", s, got)
		}
	})
}
