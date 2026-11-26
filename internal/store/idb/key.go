package idb

import (
	"encoding/binary"
	"unicode/utf16"
)

// Chromium IndexedDB key bytes (spec-pins D3-D6).
const (
	typeString         = 0x01 // IDB key type byte of a string key
	typeDatabaseName   = 0xC9 // metadata: DatabaseName key
	typeObjectStoreNam = 0xC8 // database metadata: ObjectStoreNames key
	scopesByte         = 0x32 // LevelDB-scopes bookkeeping under 00 00 00 00
	scopesUndo         = 0x02 // ... undo/cleanup entries
	scopesMeta         = 0x01 // ... per-transaction metadata
	maxStringUnits     = 4096
	objectStoreDataIdx = 1 // index id of ObjectStoreData records
)

// idLen is the minimal little-endian byte length of id (at least 1).
func idLen(id uint64) int {
	n := 1
	for id >>= 8; id > 0; id >>= 8 {
		n++
	}
	return n
}

// encodePrefix implements Chromium's KeyPrefix: first byte
// ((len(db)-1)<<5)|((len(os)-1)<<2)|(len(idx)-1), then the three ids
// little-endian in their minimal byte lengths (db, os: 1..8; idx: 1..4).
func encodePrefix(db, os, idx uint64) []byte {
	dl, ol, il := idLen(db), idLen(os), idLen(idx)
	if il > 4 {
		il = 4 // callers never pass an index id above 2^32-1
	}
	out := []byte{byte((dl-1)<<5 | (ol-1)<<2 | (il - 1))}
	out = appendLE(out, db, dl)
	out = appendLE(out, os, ol)
	return appendLE(out, idx, il)
}

func appendLE(b []byte, v uint64, n int) []byte {
	for i := 0; i < n; i++ {
		b = append(b, byte(v>>(8*i)))
	}
	return b
}

// decodePrefix splits a key into its KeyPrefix ids and the rest.
func decodePrefix(k []byte) (db, os, idx uint64, rest []byte, ok bool) {
	if len(k) == 0 {
		return 0, 0, 0, nil, false
	}
	dl, ol, il := int(k[0]>>5)+1, int(k[0]>>2&7)+1, int(k[0]&3)+1
	if len(k) < 1+dl+ol+il {
		return 0, 0, 0, nil, false
	}
	p := k[1:]
	db, p = readLE(p, dl)
	os, p = readLE(p, ol)
	idx, p = readLE(p, il)
	return db, os, idx, p, true
}

func readLE(b []byte, n int) (uint64, []byte) {
	var v uint64
	for i := 0; i < n; i++ {
		v |= uint64(b[i]) << (8 * i)
	}
	return v, b[n:]
}

// storePrefix returns the bytes shared by every KeyPrefix(db, os, *) with
// a 1-byte index id: encodePrefix(db, os, 1) without its last byte, with
// the index-length bits of the first byte (always 0 for idx < 256) kept.
func storePrefix(db, os uint64) []byte {
	p := encodePrefix(db, os, 1)
	return p[:len(p)-1]
}

// stringWithLength is Chromium's StringWithLength: varint(UTF-16 code
// units) + the units big-endian.
func stringWithLength(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := binary.AppendUvarint(nil, uint64(len(units)))
	for _, u := range units {
		out = binary.BigEndian.AppendUint16(out, u)
	}
	return out
}

// readStringWithLength reads a StringWithLength of at most
// maxStringUnits units.
func readStringWithLength(b []byte) (s string, rest []byte, ok bool) {
	n, w := binary.Uvarint(b)
	if w <= 0 || n > maxStringUnits || uint64(len(b)-w) < 2*n {
		return "", nil, false
	}
	b = b[w:]
	units := make([]uint16, n)
	for i := range units {
		units[i] = binary.BigEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(units)), b[2*n:], true
}

// dataKey is the user key of an ObjectStoreData record of a string key.
func dataKey(db, os uint64, key string) []byte {
	out := encodePrefix(db, os, objectStoreDataIdx)
	out = append(out, typeString)
	return append(out, stringWithLength(key)...)
}

// matchDatabaseName reports whether k is a DatabaseName metadata key
// (D3) and returns its database name.
func matchDatabaseName(k []byte) (string, bool) {
	db, os, idx, rest, ok := decodePrefix(k)
	if !ok || db != 0 || os != 0 || idx != 0 || len(rest) == 0 || rest[0] != typeDatabaseName {
		return "", false
	}
	_, rest, ok = readStringWithLength(rest[1:]) // origin: any well-formed value
	if !ok {
		return "", false
	}
	name, rest, ok := readStringWithLength(rest)
	if !ok || len(rest) != 0 {
		return "", false
	}
	return name, true
}

// matchObjectStoreName reports whether k is an ObjectStoreNames key (D4)
// and returns its database id and object store name.
func matchObjectStoreName(k []byte) (uint64, string, bool) {
	db, os, idx, rest, ok := decodePrefix(k)
	if !ok || db == 0 || os != 0 || idx != 0 || len(rest) == 0 || rest[0] != typeObjectStoreNam {
		return 0, "", false
	}
	name, rest, ok := readStringWithLength(rest[1:])
	if !ok || len(rest) != 0 {
		return 0, "", false
	}
	return db, name, true
}

// matchData reports whether k is the ObjectStoreData key of keyText under
// any database and object store (index id 1), and returns the pair.
func matchData(k []byte, keyText []byte) (db, os uint64, ok bool) {
	db, os, idx, rest, ok := decodePrefix(k)
	if !ok || db == 0 || os == 0 || idx != objectStoreDataIdx || len(rest) != len(keyText) {
		return 0, 0, false
	}
	if string(rest) != string(keyText) {
		return 0, 0, false
	}
	// The prefix must be minimal, as Chromium writes it; anything else is
	// another key.
	if string(encodePrefix(db, os, idx)) != string(k[:len(k)-len(rest)]) {
		return 0, 0, false
	}
	return db, os, true
}

// scopeKind returns scopesUndo or scopesMeta for a LevelDB-scopes key
// under 00 00 00 00 32, else 0.
func scopeKind(k []byte) byte {
	if len(k) < 6 || k[0] != 0 || k[1] != 0 || k[2] != 0 || k[3] != 0 || k[4] != scopesByte {
		return 0
	}
	switch k[5] {
	case scopesUndo, scopesMeta:
		return k[5]
	}
	return 0
}
