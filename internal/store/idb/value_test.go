package idb

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/store/idb/idbtest"
)

func TestDecodeValue(t *testing.T) {
	text := pinText(a1, a2)
	spec := testSpec()
	cases := []struct {
		name    string
		b       []byte
		want    string
		wrapped string
		err     error
	}{
		{"observed shape", idbtest.Value(text, idbtest.ValueOpts{}), text, "", nil},
		{"blink 20 without trailer", idbtest.Value(text, idbtest.ValueOpts{Blink: 20}), text, "", nil},
		{"blink 21 non-zero trailer", idbtest.Value(text, idbtest.ValueOpts{Trailer: bytes.Repeat([]byte{1}, 12)}), "", "", ErrUnsupported},
		{"blink 21 without trailer", idbtest.Value(text, idbtest.ValueOpts{NoTrailer: true}), "", "", ErrUnsupported},
		{"blink 22", idbtest.Value(text, idbtest.ValueOpts{Blink: 22}), "", "", ErrVersion},
		{"v8 17", idbtest.Value(text, idbtest.ValueOpts{V8: 17}), "", "", ErrVersion},
		{"two-byte string with padding", idbtest.Value("a\u00e9\U0001F600", idbtest.ValueOpts{Tag: 0x63, Padding: 1}), "a\u00e9\U0001F600", "", nil},
		{"utf-8 string", idbtest.Value("a\u00e9", idbtest.ValueOpts{Tag: 0x53}), "a\u00e9", "", nil},
		{"latin-1 high bytes", idbtest.Value("\u00e9", idbtest.ValueOpts{}), "\u00e9", "", nil},
		{"unpaired surrogate", idbtest.Value("", idbtest.ValueOpts{Tag: 0x63, RawBody: []byte{0x00, 0xd8}}), "", "", ErrUnsupported},
		{"odd two-byte length", idbtest.Value("", idbtest.ValueOpts{Tag: 0x63, RawBody: []byte{0x41}}), "", "", ErrUnsupported},
		{"invalid utf-8", idbtest.Value("", idbtest.ValueOpts{Tag: 0x53, RawBody: []byte{0xff}}), "", "", ErrUnsupported},
		{"trailing byte", idbtest.Value(text, idbtest.ValueOpts{Trailing: []byte{0}}), "", "", ErrUnsupported},
		{"object tag", idbtest.Value("", idbtest.ValueOpts{Tag: 0x6f, RawBody: []byte{}}), "", "", ErrUnsupported},
		{"blob wrapped", idbtest.BlobWrapped(), "", "", ErrBlob},
		{"snappy wrapped", idbtest.SnappyWrapped(text, idbtest.ValueOpts{}), text, "snappy", nil},
		{"other wrapper", []byte{1, 0xff, 0x11, 0x07}, "", "", ErrUnsupported},
		{"empty", []byte{}, "", "", ErrUnsupported},
		{"no envelope", []byte{1, 0x22, 1, 'a'}, "", "", ErrUnsupported},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := decodeValue(c.b, spec)
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("err = %v, want %v", err, c.err)
				}
				return
			}
			if err != nil || d.text != c.want || d.wrapped != c.wrapped {
				t.Fatalf("d = %+v err = %v", d, err)
			}
		})
	}
	if got := ReasonOf(func() error { _, err := decodeValue(idbtest.BlobWrapped(), spec); return err }()); got != ReasonBlob {
		t.Fatalf("blob reason = %s", got)
	}
}

func TestDecodeValueCaps(t *testing.T) {
	spec := testSpec()
	spec.MaxValueBytes = 4096
	big := strings.Repeat("x", 8192)
	if _, err := decodeValue(idbtest.SnappyWrapped(big, idbtest.ValueOpts{}), spec); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("snappy over cap: %v", err)
	}
	if _, err := decodeValue(idbtest.Value(big, idbtest.ValueOpts{}), spec); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("plain over cap: %v", err)
	}
}

func TestKeyCodecs(t *testing.T) {
	for _, id := range []uint64{0, 1, 255, 256, 1 << 32, 1 << 56} {
		for _, idx := range []uint64{1, 2, 255, 256} {
			p := encodePrefix(id, id+1, idx)
			if !bytes.Equal(p, idbtest.KeyPrefix(id, id+1, idx)) {
				t.Fatalf("prefix(%d,%d) differs from the test encoder", id, idx)
			}
			db, os, ix, rest, ok := decodePrefix(append(p, 0xAA))
			if !ok || db != id || os != id+1 || ix != idx || !bytes.Equal(rest, []byte{0xAA}) {
				t.Fatalf("round trip %d: %d %d %d %v", id, db, os, ix, ok)
			}
		}
	}
	if got := hex.EncodeToString(encodePrefix(6, 1, 1)); got != "00060101" {
		t.Fatalf("observed prefix = %s", got)
	}
	if got := hex.EncodeToString(storePrefix(6, 1)); got != "000601" {
		t.Fatalf("store prefix = %s", got)
	}
	sw := stringWithLength(idbtest.PinKey)
	if sw[0] != 0x23 || len(sw) != 1+2*35 {
		t.Fatalf("observed key = %x", sw)
	}
	if !bytes.Equal(dataKey(6, 1, idbtest.PinKey)[:6], []byte{0x00, 0x06, 0x01, 0x01, 0x01, 0x23}) {
		t.Fatalf("data key = %x", dataKey(6, 1, idbtest.PinKey))
	}
	for _, s := range []string{"", "keyval", "\u00e9\uac00", "\U0001F600x"} {
		got, rest, ok := readStringWithLength(append(stringWithLength(s), 7))
		if !ok || got != s || !bytes.Equal(rest, []byte{7}) {
			t.Fatalf("string round trip %q: %q %v", s, got, ok)
		}
	}
	if name, ok := matchDatabaseName(idbtest.DatabaseNameKey(idbtest.Origin, idbtest.Database)); !ok || name != idbtest.Database {
		t.Fatal("database name key")
	}
	if db, name, ok := matchObjectStoreName(idbtest.ObjectStoreNameKey(6, idbtest.ObjectStore)); !ok || db != 6 || name != idbtest.ObjectStore {
		t.Fatal("object store name key")
	}
	if _, _, ok := matchObjectStoreName(idbtest.ObjectStoreNameKey(0, "x")); ok {
		t.Fatal("db 0 object store accepted")
	}
}

func TestScanLogUnits(t *testing.T) {
	var seen int
	if _, err := scanLog(nil, true, func([]byte) error { seen++; return nil }); err != nil || seen != 0 {
		t.Fatalf("empty log: %v %d", err, seen)
	}
	// A zero-filled block is padding.
	if _, err := scanLog(make([]byte, logBlockSize+10), false, func([]byte) error { seen++; return nil }); err != nil || seen != 0 {
		t.Fatalf("zero log: %v %d", err, seen)
	}
	if err := decodeBatch([]byte{1, 2, 3}, func(batchEntry) error { return nil }); !errors.Is(err, ErrUnclean) {
		t.Fatalf("short batch: %v", err)
	}
	b := idbtest.BatchBytes(idbtest.Batch{Seq: 5, Ops: []idbtest.Op{idbtest.Put([]byte("k"), []byte("v")), idbtest.Del([]byte("d"))}})
	var got []batchEntry
	if err := decodeBatch(b, func(e batchEntry) error { got = append(got, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].seq != 5 || !got[0].put || got[1].seq != 6 || got[1].put {
		t.Fatalf("entries = %+v", got)
	}
	if err := decodeBatch(append(b, 0), func(batchEntry) error { return nil }); !errors.Is(err, ErrUnclean) {
		t.Fatalf("trailing: %v", err)
	}
}

func TestTouches(t *testing.T) {
	key := dataKey(6, 1, idbtest.PinKey)
	sp := storePrefix(6, 1)
	if Touches(nil, key, sp) {
		t.Fatal("empty message")
	}
	if !Touches(idbtest.ProtoBytes(2, key), key, sp) {
		t.Fatal("direct key")
	}
	if Touches(idbtest.ProtoBytes(2, dataKey(7, 1, "x")), key, sp) {
		t.Fatal("other store")
	}
	nested := idbtest.ProtoBytes(1, idbtest.ProtoBytes(1, idbtest.ProtoBytes(1, key)))
	if !Touches(nested, key, sp) {
		t.Fatal("nested")
	}
	if !Touches([]byte{0x0b}, key, sp) {
		t.Fatal("group is not scannable")
	}
	// A plain JSON string field is not a message and is not a hit.
	if Touches(idbtest.ProtoBytes(4, []byte(`{"state":1}`)), key, sp) {
		t.Fatal("json bytes")
	}
}
