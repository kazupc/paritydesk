package idb

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/golang/snappy"
)

// Value envelope bytes (D5, D7).
const (
	tagVersion        = 0xff // Blink and V8 version tags
	tagTrailerOffset  = 0xfe // Blink >= 21: trailer offset/size (12 bytes)
	wrapPseudoVersion = 0x11 // IndexedDB value wrapping marker after 0xff
	wrapBlob          = 0x01 // value replaced by a blob reference
	wrapSnappy        = 0x02 // value compressed with raw snappy
	tagOneByteString  = 0x22 // V8 '"': Latin-1 string
	tagTwoByteString  = 0x63 // V8 'c': UTF-16LE string
	tagUTF8String     = 0x53 // V8 'S': UTF-8 string
	trailerLen        = 12
	maxPadding        = 8
	envelopeSlack     = 64
)

// decoded is a decoded value.
type decoded struct {
	text      string
	wrapped   string
	blink, v8 uint32
}

// decodeValue decodes a winning ObjectStoreData value (4.4).
func decodeValue(b []byte, spec Spec) (decoded, error) {
	var d decoded
	if len(b) > spec.MaxValueBytes+envelopeSlack {
		return d, fail("value", ErrTooLarge)
	}
	_, n := binary.Uvarint(b) // IDB record version
	if n <= 0 {
		return d, fail("value: record version", ErrUnsupported)
	}
	b = b[n:]
	if len(b) >= 2 && b[0] == tagVersion && b[1] == wrapPseudoVersion {
		if len(b) < 3 {
			return d, fail("value: wrapper", ErrUnsupported)
		}
		switch b[2] {
		case wrapBlob:
			return d, fail("value", ErrBlob)
		case wrapSnappy:
			dl, err := snappy.DecodedLen(b[3:])
			if err != nil {
				return d, fail("value: snappy", ErrUnsupported)
			}
			if dl > spec.MaxValueBytes {
				return d, fail("value: snappy", ErrTooLarge)
			}
			inner, err := snappy.Decode(nil, b[3:])
			if err != nil {
				return d, fail("value: snappy", ErrUnsupported)
			}
			b, d.wrapped = inner, "snappy"
		default:
			return d, fail("value: wrapper", ErrUnsupported)
		}
	}
	// Blink envelope.
	if len(b) == 0 || b[0] != tagVersion {
		return d, fail("value: blink envelope", ErrUnsupported)
	}
	v, n := binary.Uvarint(b[1:])
	if n <= 0 || v > 1<<32-1 {
		return d, fail("value: blink version", ErrUnsupported)
	}
	b = b[1+n:]
	if v < uint64(spec.BlinkMin) || v > uint64(spec.BlinkMax) {
		return d, fail("value: blink version", ErrVersion)
	}
	d.blink = uint32(v)
	if v >= 21 {
		if len(b) < 1+trailerLen || b[0] != tagTrailerOffset {
			return d, fail("value: blink trailer", ErrUnsupported)
		}
		for _, c := range b[1 : 1+trailerLen] {
			if c != 0 {
				return d, fail("value: blink trailer", ErrUnsupported)
			}
		}
		b = b[1+trailerLen:]
	}
	// V8 header.
	if len(b) == 0 || b[0] != tagVersion {
		return d, fail("value: v8 header", ErrUnsupported)
	}
	w, n := binary.Uvarint(b[1:])
	if n <= 0 || w > 1<<32-1 {
		return d, fail("value: v8 version", ErrUnsupported)
	}
	b = b[1+n:]
	if w < uint64(spec.V8Min) || w > uint64(spec.V8Max) {
		return d, fail("value: v8 version", ErrVersion)
	}
	d.v8 = uint32(w)
	for i := 0; i < maxPadding && len(b) > 0 && b[0] == 0; i++ {
		b = b[1:]
	}
	if len(b) == 0 {
		return d, fail("value: v8 tag", ErrUnsupported)
	}
	tag := b[0]
	l, n := binary.Uvarint(b[1:])
	if n <= 0 || l > uint64(len(b)-1-n) {
		return d, fail("value: v8 string", ErrUnsupported)
	}
	body, rest := b[1+n:1+n+int(l)], b[1+n+int(l):]
	if len(rest) != 0 {
		return d, fail("value: trailing bytes", ErrUnsupported)
	}
	switch tag {
	case tagOneByteString:
		var sb strings.Builder
		sb.Grow(len(body))
		for _, c := range body {
			sb.WriteRune(rune(c))
		}
		d.text = sb.String()
	case tagTwoByteString:
		if len(body)%2 != 0 {
			return d, fail("value: v8 string", ErrUnsupported)
		}
		units := make([]uint16, len(body)/2)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(body[2*i:])
		}
		if !validUTF16(units) {
			return d, fail("value: v8 string", ErrUnsupported)
		}
		d.text = string(utf16.Decode(units))
	case tagUTF8String:
		if !utf8.Valid(body) {
			return d, fail("value: v8 string", ErrUnsupported)
		}
		d.text = string(body)
	default:
		return d, fail("value: v8 tag", ErrUnsupported)
	}
	if len(d.text) > spec.MaxValueBytes {
		return decoded{}, fail("value", ErrTooLarge)
	}
	return d, nil
}

// validUTF16 reports whether units hold no unpaired surrogate.
func validUTF16(units []uint16) bool {
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case u >= 0xD800 && u <= 0xDBFF:
			if i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] > 0xDFFF {
				return false
			}
			i++
		case u >= 0xDC00 && u <= 0xDFFF:
			return false
		}
	}
	return true
}
