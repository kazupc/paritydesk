package idb

import (
	"testing"

	"github.com/kazupc/paritydesk/internal/store/idb/idbtest"
)

func FuzzIDBValue(f *testing.F) {
	f.Add(idbtest.PinValue([]string{a1}))
	f.Add(idbtest.SnappyWrapped(pinText(a1), idbtest.ValueOpts{}))
	f.Add(idbtest.BlobWrapped())
	f.Add(idbtest.Value("x", idbtest.ValueOpts{Tag: 0x63}))
	spec := testSpec()
	spec.MaxValueBytes = 4096
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := decodeValue(b, spec)
		if err == nil && len(d.text) > spec.MaxValueBytes {
			t.Fatalf("text over the cap: %d", len(d.text))
		}
	})
}

func FuzzIDBKey(f *testing.F) {
	f.Add(idbtest.DataKey(6, 1, idbtest.PinKey))
	f.Add(idbtest.DatabaseNameKey(idbtest.Origin, idbtest.Database))
	f.Add(idbtest.ObjectStoreNameKey(6, idbtest.ObjectStore))
	f.Add(idbtest.ScopeUndoKey(1, 2))
	kt := append([]byte{typeString}, stringWithLength(idbtest.PinKey)...)
	f.Fuzz(func(t *testing.T, k []byte) {
		decodePrefix(k)
		matchDatabaseName(k)
		matchObjectStoreName(k)
		if db, os, ok := matchData(k, kt); ok && string(dataKey(db, os, idbtest.PinKey)) != string(k) {
			t.Fatalf("matched a key that is not the data key")
		}
		scopeKind(k)
		readStringWithLength(k)
	})
}

func FuzzLogRec(f *testing.F) {
	f.Add([]byte{}, true)
	f.Add(idbtest.BatchBytes(idbtest.Batch{Seq: 1, Ops: []idbtest.Op{idbtest.Put([]byte("k"), []byte("v"))}}), false)
	f.Fuzz(func(t *testing.T, data []byte, newest bool) {
		dropped, err := scanLog(data, newest, func(rec []byte) error {
			return decodeBatch(rec, func(batchEntry) error { return nil })
		})
		if dropped && (!newest || err != nil) {
			t.Fatalf("torn tail outside the newest log (newest=%v err=%v)", newest, err)
		}
	})
}

func FuzzProtoScan(f *testing.F) {
	f.Add(idbtest.ProtoBytes(1, idbtest.DataKey(6, 1, idbtest.PinKey)))
	f.Add([]byte{0x0b})
	key, sp := dataKey(6, 1, idbtest.PinKey), storePrefix(6, 1)
	f.Fuzz(func(t *testing.T, b []byte) {
		Touches(b, key, sp)
	})
}
