// Package idbtest builds synthetic IndexedDB-on-LevelDB directories and
// value bytes for tests of internal/store/idb and its users. It is an
// independent encoder (it does not import idb): tables are written with
// goleveldb's table writer, logs with goleveldb's journal writer, and the
// manifest by hand with explicit sizes and key ranges.
//
// Test-only: it may be imported from _test.go files only (a policy test
// checks this). Fixtures use fake session ids only.
package idbtest

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"unicode/utf16"

	"github.com/golang/snappy"
	"github.com/syndtr/goleveldb/leveldb/journal"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/table"
)

// Well-known values of the app's pin store (spec-pins D3-D5).
const (
	Origin      = "https://claude.ai"
	Database    = "keyval-store"
	ObjectStore = "keyval"
	PinKey      = "store:pin-state:dframe-starred-code"
	Comparator  = "idb_cmp1"
	// UpdatedAt is the fixed updatedAt of every fixture.
	UpdatedAt int64 = 1700000000000
	// Dir is the data-folder-relative LevelDB directory.
	Dir = "IndexedDB/https_claude.ai_0.indexeddb.leveldb"
	// BackendPrefix is the shared prefix of the origin's entries.
	BackendPrefix = "IndexedDB/https_claude.ai_0.indexeddb."
)

// FakeID returns the fake local session id number n
// (local_00000000-0000-4000-8000-00000000xxxx).
func FakeID(n int) string { return fmt.Sprintf("local_00000000-0000-4000-8000-%012d", n) }

// Op is one put or delete of a write batch or a table.
type Op struct {
	Delete bool
	Key    []byte
	Value  []byte
}

// Put returns a put op.
func Put(k, v []byte) Op { return Op{Key: k, Value: v} }

// Del returns a delete op.
func Del(k []byte) Op { return Op{Delete: true, Key: k} }

// Batch is one log record: ops numbered from Seq.
type Batch struct {
	Seq uint64
	Ops []Op
}

// Table is one table file: its entries with their sequence numbers.
type Table struct {
	Num   int64
	Level uint64
	// Entries are written in internal-key order; Seq numbers them.
	Entries []TableEntry
	// Snappy compresses the data blocks (else none).
	Snappy bool
	// BlockSize of the data blocks (0: 4 KiB).
	BlockSize int
}

// TableEntry is one table entry.
type TableEntry struct {
	Op
	Seq uint64
}

// Log is one log file.
type Log struct {
	Num     int64
	Batches []Batch
	// Cut removes that many bytes from the end of the file (a torn tail).
	Cut int
	// TruncateTo, when > 0, truncates the file to that length.
	TruncateTo int64
}

// Layout describes a database directory.
type Layout struct {
	Comparator string // "" = idb_cmp1
	Tables     []Table
	Logs       []Log
	LogNum     int64 // manifest log number (0: the first log's number)
	PrevLogNum int64
	// UnknownTag adds a manifest field this reader does not know.
	UnknownTag bool
	// Current overrides CURRENT's content ("" = "MANIFEST-000001\n").
	Current string
	// NoCurrent leaves CURRENT out (CURRENT.bak is written instead).
	NoCurrent bool
}

// Write creates dir (it must not exist or be empty) with the layout. It
// returns the sizes of the written tables by number.
func Write(dir string, l Layout) (map[int64]int64, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sizes := map[int64]int64{}
	var maxSeq uint64
	for _, t := range l.Tables {
		size, err := writeTable(filepath.Join(dir, fmt.Sprintf("%06d.ldb", t.Num)), t)
		if err != nil {
			return nil, err
		}
		sizes[t.Num] = size
		for _, e := range t.Entries {
			maxSeq = max(maxSeq, e.Seq)
		}
	}
	for _, lg := range l.Logs {
		if err := writeLog(filepath.Join(dir, fmt.Sprintf("%06d.log", lg.Num)), lg); err != nil {
			return nil, err
		}
	}
	logNum := l.LogNum
	if logNum == 0 && len(l.Logs) > 0 {
		logNum = l.Logs[0].Num
	}
	next := int64(1)
	for _, t := range l.Tables {
		next = max(next, t.Num+1)
	}
	for _, lg := range l.Logs {
		next = max(next, lg.Num+1)
	}
	comp := l.Comparator
	if comp == "" {
		comp = Comparator
	}
	var rec []byte
	rec = binary.AppendUvarint(rec, 1)
	rec = appendBytes(rec, []byte(comp))
	rec = binary.AppendUvarint(rec, 2)
	rec = binary.AppendUvarint(rec, uint64(logNum))
	if l.PrevLogNum != 0 {
		rec = binary.AppendUvarint(rec, 9)
		rec = binary.AppendUvarint(rec, uint64(l.PrevLogNum))
	}
	rec = binary.AppendUvarint(rec, 3)
	rec = binary.AppendUvarint(rec, uint64(next+1))
	rec = binary.AppendUvarint(rec, 4)
	rec = binary.AppendUvarint(rec, maxSeq)
	for _, t := range l.Tables {
		small, large := keyRange(t)
		rec = binary.AppendUvarint(rec, 7)
		rec = binary.AppendUvarint(rec, t.Level)
		rec = binary.AppendUvarint(rec, uint64(t.Num))
		rec = binary.AppendUvarint(rec, uint64(sizes[t.Num]))
		rec = appendBytes(rec, small)
		rec = appendBytes(rec, large)
	}
	if l.UnknownTag {
		rec = binary.AppendUvarint(rec, 99)
		rec = binary.AppendUvarint(rec, 1)
	}
	f, err := os.Create(filepath.Join(dir, "MANIFEST-000001"))
	if err != nil {
		return nil, err
	}
	jw := journal.NewWriter(f)
	w, err := jw.Next()
	if err == nil {
		_, err = w.Write(rec)
	}
	if err == nil {
		err = jw.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	cur := l.Current
	if cur == "" {
		cur = "MANIFEST-000001\n"
	}
	name := "CURRENT"
	if l.NoCurrent {
		name = "CURRENT.bak"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(cur), 0o600); err != nil {
		return nil, err
	}
	return sizes, nil
}

func appendBytes(b, v []byte) []byte {
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

// InternalKey is user key + uint64le(seq<<8 | kind).
func InternalKey(uk []byte, seq uint64, put bool) []byte {
	kind := uint64(0)
	if put {
		kind = 1
	}
	return binary.LittleEndian.AppendUint64(append([]byte(nil), uk...), seq<<8|kind)
}

func sortedEntries(t Table) []TableEntry {
	es := append([]TableEntry(nil), t.Entries...)
	sort.Slice(es, func(i, j int) bool {
		return bytes.Compare(InternalKey(es[i].Key, es[i].Seq, !es[i].Delete), InternalKey(es[j].Key, es[j].Seq, !es[j].Delete)) < 0
	})
	return es
}

func keyRange(t Table) ([]byte, []byte) {
	es := sortedEntries(t)
	if len(es) == 0 {
		return nil, nil
	}
	f, l := es[0], es[len(es)-1]
	return InternalKey(f.Key, f.Seq, !f.Delete), InternalKey(l.Key, l.Seq, !l.Delete)
}

func writeTable(path string, t Table) (int64, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	o := &opt.Options{Compression: opt.NoCompression, BlockSize: t.BlockSize}
	if t.Snappy {
		o.Compression = opt.SnappyCompression
	}
	w := table.NewWriter(f, o, nil, 0)
	for _, e := range sortedEntries(t) {
		v := e.Value
		if e.Delete {
			v = nil
		}
		if err := w.Append(InternalKey(e.Key, e.Seq, !e.Delete), v); err != nil {
			f.Close()
			return 0, err
		}
	}
	if err := w.Close(); err != nil {
		f.Close()
		return 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return 0, err
	}
	return fi.Size(), f.Close()
}

// BatchBytes encodes a write batch.
func BatchBytes(b Batch) []byte {
	out := binary.LittleEndian.AppendUint64(nil, b.Seq)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(b.Ops)))
	for _, op := range b.Ops {
		if op.Delete {
			out = append(out, 0)
			out = appendBytes(out, op.Key)
			continue
		}
		out = append(out, 1)
		out = appendBytes(out, op.Key)
		out = appendBytes(out, op.Value)
	}
	return out
}

func writeLog(path string, lg Log) error {
	var buf bytes.Buffer
	jw := journal.NewWriter(&buf)
	for _, b := range lg.Batches {
		w, err := jw.Next()
		if err != nil {
			return err
		}
		if _, err := w.Write(BatchBytes(b)); err != nil {
			return err
		}
	}
	if err := jw.Close(); err != nil {
		return err
	}
	data := buf.Bytes()
	if lg.Cut > 0 && lg.Cut <= len(data) {
		data = data[:len(data)-lg.Cut]
	}
	if lg.TruncateTo > 0 && lg.TruncateTo < int64(len(data)) {
		data = data[:lg.TruncateTo]
	}
	return os.WriteFile(path, data, 0o600)
}

// ---------------------------------------------------------------- keys

func idLen(id uint64) int {
	n := 1
	for id >>= 8; id > 0; id >>= 8 {
		n++
	}
	return n
}

// KeyPrefix is Chromium's KeyPrefix of (db, os, idx).
func KeyPrefix(db, os, idx uint64) []byte {
	dl, ol, il := idLen(db), idLen(os), idLen(idx)
	out := []byte{byte((dl-1)<<5 | (ol-1)<<2 | (il - 1))}
	for _, x := range []struct {
		v uint64
		n int
	}{{db, dl}, {os, ol}, {idx, il}} {
		for i := 0; i < x.n; i++ {
			out = append(out, byte(x.v>>(8*i)))
		}
	}
	return out
}

// StringWithLength is varint(UTF-16 units) + the units big-endian.
func StringWithLength(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := binary.AppendUvarint(nil, uint64(len(units)))
	for _, u := range units {
		out = binary.BigEndian.AppendUint16(out, u)
	}
	return out
}

// DataKey is the ObjectStoreData key of a string key.
func DataKey(db, os uint64, key string) []byte {
	out := append(KeyPrefix(db, os, 1), 0x01)
	return append(out, StringWithLength(key)...)
}

// ExistsKey is the ExistsEntry key (index id 2) of a string key.
func ExistsKey(db, os uint64, key string) []byte {
	out := append(KeyPrefix(db, os, 2), 0x01)
	return append(out, StringWithLength(key)...)
}

// DatabaseNameKey is the DatabaseName metadata key.
func DatabaseNameKey(origin, name string) []byte {
	out := append(KeyPrefix(0, 0, 0), 0xC9)
	out = append(out, StringWithLength(origin)...)
	return append(out, StringWithLength(name)...)
}

// ObjectStoreNameKey is the ObjectStoreNames key of database db.
func ObjectStoreNameKey(db uint64, name string) []byte {
	out := append(KeyPrefix(db, 0, 0), 0xC8)
	return append(out, StringWithLength(name)...)
}

// Varint is a varint id value.
func Varint(v uint64) []byte { return binary.AppendUvarint(nil, v) }

// ScopeUndoKey is a LevelDB-scopes undo/cleanup key.
func ScopeUndoKey(scope, n uint64) []byte {
	out := []byte{0, 0, 0, 0, 0x32, 0x02}
	out = binary.AppendUvarint(out, scope)
	out = append(out, 0)
	return binary.BigEndian.AppendUint64(out, n)
}

// ScopeMetaKey is a LevelDB-scopes per-transaction metadata key.
func ScopeMetaKey(scope uint64) []byte {
	return binary.AppendUvarint([]byte{0, 0, 0, 0, 0x32, 0x01}, scope)
}

// ProtoBytes encodes one length-delimited protobuf field.
func ProtoBytes(field int, b []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(field<<3|2))
	out = binary.AppendUvarint(out, uint64(len(b)))
	return append(out, b...)
}

// ---------------------------------------------------------------- values

// ValueOpts shape a serialized value. Zero values give the observed
// shape: record version 1, Blink 21 with a zero trailer, V8 16, a one-byte
// string.
type ValueOpts struct {
	RecordVersion uint64
	Blink         uint64
	NoTrailer     bool
	Trailer       []byte // 12 bytes; nil = zeros
	V8            uint64
	Padding       int
	Tag           byte // 0x22 (default), 0x63, 0x53 or anything else
	Trailing      []byte
	// RawBody replaces the encoded string body (with Tag).
	RawBody []byte
}

// Value returns the serialized IDB value of text.
func Value(text string, o ValueOpts) []byte {
	if o.RecordVersion == 0 {
		o.RecordVersion = 1
	}
	return append(binary.AppendUvarint(nil, o.RecordVersion), Inner(text, o)...)
}

// Inner is the Blink/V8 envelope without the record version (what a
// snappy wrapper compresses).
func Inner(text string, o ValueOpts) []byte {
	if o.Blink == 0 {
		o.Blink = 21
	}
	if o.V8 == 0 {
		o.V8 = 16
	}
	if o.Tag == 0 {
		o.Tag = 0x22
	}
	out := []byte{0xff}
	out = binary.AppendUvarint(out, o.Blink)
	if o.Blink >= 21 && !o.NoTrailer {
		out = append(out, 0xfe)
		tr := o.Trailer
		if tr == nil {
			tr = make([]byte, 12)
		}
		out = append(out, tr...)
	}
	out = append(out, 0xff)
	out = binary.AppendUvarint(out, o.V8)
	out = append(out, make([]byte, o.Padding)...)
	out = append(out, o.Tag)
	body := o.RawBody
	if body == nil {
		switch o.Tag {
		case 0x63:
			for _, u := range utf16.Encode([]rune(text)) {
				body = binary.LittleEndian.AppendUint16(body, u)
			}
		case 0x53:
			body = []byte(text)
		default:
			for _, r := range text {
				body = append(body, byte(r))
			}
		}
	}
	out = binary.AppendUvarint(out, uint64(len(body)))
	out = append(out, body...)
	return append(out, o.Trailing...)
}

// SnappyWrapped is a value wrapped as ff 11 02 + raw snappy(inner).
func SnappyWrapped(text string, o ValueOpts) []byte {
	rv := o.RecordVersion
	if rv == 0 {
		rv = 1
	}
	out := binary.AppendUvarint(nil, rv)
	out = append(out, 0xff, 0x11, 0x02)
	return append(out, snappy.Encode(nil, Inner(text, o))...)
}

// BlobWrapped is a value replaced by a blob reference (ff 11 01 ...).
func BlobWrapped() []byte { return []byte{0x01, 0xff, 0x11, 0x01, 0x05, 0x00, 0x00} }

// PinJSON is the zustand envelope the app stores, with the fixed
// updatedAt.
func PinJSON(ids []string) string {
	if ids == nil {
		ids = []string{}
	}
	b, _ := json.Marshal(map[string]any{"state": map[string]any{"starredIds": ids}, "version": 0, "updatedAt": UpdatedAt})
	return string(b)
}

// PinValue is the serialized value of PinJSON(ids).
func PinValue(ids []string) []byte { return Value(PinJSON(ids), ValueOpts{}) }

// ---------------------------------------------------------------- helpers

// PinDB returns a layout holding the pin store's metadata and value in
// one log: database id 6, object store id 1, as observed.
func PinDB(value []byte) Layout {
	return Layout{Logs: []Log{{Num: 3, Batches: []Batch{
		{Seq: 1, Ops: []Op{
			Put(DatabaseNameKey(Origin, Database), Varint(6)),
			Put(ObjectStoreNameKey(6, ObjectStore), Varint(1)),
		}},
		{Seq: 10, Ops: []Op{
			Put(DataKey(6, 1, PinKey), value),
			Put(ExistsKey(6, 1, PinKey), Varint(1)),
		}},
	}}}}
}

// SetBlockType rewrites the compression type byte of the first data block
// of the table at path and fixes its checksum.
func SetBlockType(path string, typ byte) error {
	return patchFirstBlock(path, func(block []byte, trailer []byte) {
		trailer[0] = typ
		binary.LittleEndian.PutUint32(trailer[1:], maskedCRC(append(append([]byte(nil), block...), typ)))
	})
}

// FlipBlockByte flips one byte of the first data block of the table at
// path without fixing its checksum.
func FlipBlockByte(path string) error {
	return patchFirstBlock(path, func(block []byte, _ []byte) { block[0] ^= 0xff })
}

func patchFirstBlock(path string, fn func(block, trailer []byte)) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) < 48 {
		return fmt.Errorf("idbtest: short table")
	}
	footer := data[len(data)-48:]
	_, n1 := binary.Uvarint(footer) // metaindex offset
	_, n2 := binary.Uvarint(footer[n1:])
	ioff, n3 := binary.Uvarint(footer[n1+n2:])
	ilen, _ := binary.Uvarint(footer[n1+n2+n3:])
	index := data[ioff : ioff+ilen]
	if data[ioff+ilen] == 1 { // goleveldb compresses the index block too
		dec, err := snappy.Decode(nil, index)
		if err != nil {
			return err
		}
		index = dec
	}
	// First index entry: shared(0), unshared, vlen, key, value (handle).
	p := index
	_, a := binary.Uvarint(p)
	p = p[a:]
	unshared, b := binary.Uvarint(p)
	p = p[b:]
	_, c := binary.Uvarint(p)
	p = p[c:]
	p = p[unshared:]
	boff, d := binary.Uvarint(p)
	blen, _ := binary.Uvarint(p[d:])
	block := data[boff : boff+blen]
	trailer := data[boff+blen : boff+blen+5]
	fn(block, trailer)
	return os.WriteFile(path, data, 0o600)
}

func maskedCRC(b []byte) uint32 {
	c := crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))
	return (c>>15 | c<<17) + 0xa282ead8
}
