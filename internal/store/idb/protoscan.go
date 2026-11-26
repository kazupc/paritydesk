package idb

import (
	"bytes"
	"encoding/binary"
)

// Bounds of the protobuf wire-format scan of scope entries.
const (
	protoMaxDepth  = 4
	protoMaxFields = 4096
)

// Touches reports whether a LevelDB-scopes undo/cleanup entry may concern
// the key: it walks value as protobuf wire format (varint, 64-bit,
// length-delimited, 32-bit; groups are not scannable), recursing into
// length-delimited fields up to depth 4 and at most 4096 fields in all.
// It returns true when any length-delimited byte string equals dataKey or
// starts with storePrefix, and also when the value is not scannable. The
// schema of these entries is not known here (spec-pins V2), so no field
// number is relied on.
func Touches(value, dataKey, storePrefix []byte) bool {
	fields := 0
	hit, ok := protoWalk(value, dataKey, storePrefix, 0, &fields)
	return hit || !ok
}

// protoWalk scans one message. ok is false when b is not a well-formed
// message or a bound was exceeded.
func protoWalk(b, dataKey, storePrefix []byte, depth int, fields *int) (hit, ok bool) {
	for len(b) > 0 {
		*fields++
		if *fields > protoMaxFields {
			return false, false
		}
		tag, n := binary.Uvarint(b)
		if n <= 0 || tag>>3 == 0 {
			return false, false
		}
		b = b[n:]
		switch tag & 7 {
		case 0: // varint
			_, n := binary.Uvarint(b)
			if n <= 0 {
				return false, false
			}
			b = b[n:]
		case 1: // 64-bit
			if len(b) < 8 {
				return false, false
			}
			b = b[8:]
		case 5: // 32-bit
			if len(b) < 4 {
				return false, false
			}
			b = b[4:]
		case 2: // length-delimited
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) {
				return false, false
			}
			s := b[n : n+int(l)]
			b = b[n+int(l):]
			if bytes.Equal(s, dataKey) || (len(storePrefix) > 0 && bytes.HasPrefix(s, storePrefix)) {
				return true, true
			}
			if depth+1 < protoMaxDepth && len(s) > 0 {
				// A nested message, or plain bytes: a scan that fails here
				// only means the field is not a message.
				saved := *fields
				if h, ok := protoWalk(s, dataKey, storePrefix, depth+1, fields); ok && h {
					return true, true
				} else if !ok {
					if *fields > protoMaxFields {
						return false, false
					}
					*fields = saved
				}
			}
		default: // groups (3, 4) and invalid wire types
			return false, false
		}
	}
	return false, true
}
