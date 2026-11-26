package idb

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
)

// LevelDB log ("journal") physical format: 32 KiB blocks of records with a
// 7-byte header (masked CRC-32C of type and payload, payload length, type).
// A block's last bytes that cannot hold a header are zero padding.
const (
	logBlockSize = 32 << 10
	logHeaderLen = 7
	recZero      = 0
	recFull      = 1
	recFirst     = 2
	recMiddle    = 3
	recLast      = 4
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// maskedCRC is LevelDB's masked CRC-32C.
func maskedCRC(b []byte) uint32 {
	c := crc32.Checksum(b, castagnoli)
	return (c>>15 | c<<17) + 0xa282ead8
}

// errTornTail marks an incomplete last record (internal to the scanner).
var errTornTail = errors.New("idb: incomplete last log record")

// scanLog reassembles the records of one log file's bytes and calls fn
// with each. In the newest log (newest), an incomplete record at the end
// of the file (a header or payload that runs past it, or a FIRST/MIDDLE
// chunk without its LAST) is ignored, as the C++ library does after a
// crash, and tailDropped reports it. Every other defect (checksum, type,
// length, a fragment out of order, an incomplete record in an older log)
// is ErrUnclean.
func scanLog(data []byte, newest bool, fn func(rec []byte) error) (tailDropped bool, err error) {
	var frag []byte
	inFrag := false
	torn := func() (bool, error) {
		if newest {
			return true, nil
		}
		return false, fail("log: incomplete record", ErrUnclean)
	}
	for off := 0; off < len(data); {
		left := logBlockSize - off%logBlockSize
		if left < logHeaderLen {
			off += left // block trailer
			continue
		}
		if len(data)-off < logHeaderLen {
			return torn()
		}
		h := data[off : off+logHeaderLen]
		length := int(binary.LittleEndian.Uint16(h[4:6]))
		typ := h[6]
		if typ == recZero && length == 0 && binary.LittleEndian.Uint32(h[0:4]) == 0 {
			// Zero padding (preallocated space): skip the rest of the block.
			off += left
			continue
		}
		if logHeaderLen+length > left {
			return false, fail("log: record length", ErrUnclean)
		}
		if off+logHeaderLen+length > len(data) {
			return torn()
		}
		payload := data[off+logHeaderLen : off+logHeaderLen+length]
		if maskedCRC(append([]byte{typ}, payload...)) != binary.LittleEndian.Uint32(h[0:4]) {
			return false, fail("log: checksum", ErrUnclean)
		}
		off += logHeaderLen + length
		switch typ {
		case recFull:
			if inFrag {
				return false, fail("log: fragment", ErrUnclean)
			}
			if err := fn(payload); err != nil {
				return false, err
			}
		case recFirst:
			if inFrag {
				return false, fail("log: fragment", ErrUnclean)
			}
			frag, inFrag = append(frag[:0], payload...), true
		case recMiddle:
			if !inFrag {
				return false, fail("log: fragment", ErrUnclean)
			}
			frag = append(frag, payload...)
		case recLast:
			if !inFrag {
				return false, fail("log: fragment", ErrUnclean)
			}
			frag = append(frag, payload...)
			inFrag = false
			if err := fn(frag); err != nil {
				return false, err
			}
		default:
			return false, fail("log: record type", ErrUnclean)
		}
	}
	if inFrag {
		return torn()
	}
	return false, nil
}

// batchEntry is one entry of a write batch.
type batchEntry struct {
	seq   uint64
	put   bool
	key   []byte
	value []byte
}

// decodeBatch decodes a write batch: 8-byte sequence, 4-byte count, then
// count entries (type 1 put: varint key, varint value; type 0 delete:
// varint key). Entry i has sequence seq+i.
func decodeBatch(rec []byte, fn func(batchEntry) error) error {
	if len(rec) < 12 {
		return fail("log: batch header", ErrUnclean)
	}
	seq := binary.LittleEndian.Uint64(rec)
	count := binary.LittleEndian.Uint32(rec[8:])
	b := rec[12:]
	readBytes := func() ([]byte, bool) {
		n, w := binary.Uvarint(b)
		if w <= 0 || n > uint64(len(b)-w) {
			return nil, false
		}
		v := b[w : w+int(n)]
		b = b[w+int(n):]
		return v, true
	}
	for i := uint32(0); i < count; i++ {
		if len(b) == 0 {
			return fail("log: batch count", ErrUnclean)
		}
		typ := b[0]
		b = b[1:]
		e := batchEntry{seq: seq + uint64(i)}
		var ok bool
		switch typ {
		case 1:
			e.put = true
			if e.key, ok = readBytes(); ok {
				e.value, ok = readBytes()
			}
		case 0:
			e.key, ok = readBytes()
		default:
			return fail("log: batch entry", ErrUnclean)
		}
		if !ok {
			return fail("log: batch entry", ErrUnclean)
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	if len(b) != 0 {
		return fail("log: batch trailing bytes", ErrUnclean)
	}
	return nil
}
