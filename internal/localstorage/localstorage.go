package localstorage

import (
	"bytes"
	"errors"
	"unicode/utf16"
	"unicode/utf8"
)

// Origin is the only origin paritydesk reads or writes.
const Origin = "https://claude.ai"

// MetaKey is the origin's metadata key. paritydesk never writes it (D17).
var MetaKey = []byte("META:" + Origin)

// keyPrefix is "_" + Origin + NUL.
var keyPrefix = []byte("_" + Origin + "\x00")

// Flags.
const (
	FlagUTF16  byte = 0x00
	FlagLatin1 byte = 0x01
)

// Errors.
var (
	ErrEmpty           = errors.New("localstorage: empty value")
	ErrUnknownFlag     = errors.New("localstorage: unknown string flag")
	ErrOddUTF16        = errors.New("localstorage: odd UTF-16 length")
	ErrNotRoundTrip    = errors.New("localstorage: value does not round-trip; refusing to edit")
	ErrNotOriginKey    = errors.New("localstorage: key is not a claude.ai key")
	ErrInvalidUTF8Name = errors.New("localstorage: key name is not valid UTF-8")
)

// KeyPrefix returns a copy of the origin key prefix, for prefix scans.
func KeyPrefix() []byte { return append([]byte(nil), keyPrefix...) }

// Key returns the LevelDB key for a Local Storage item name.
func Key(name string) ([]byte, error) {
	if !utf8.ValidString(name) {
		return nil, ErrInvalidUTF8Name
	}
	enc := EncodeString(name)
	out := make([]byte, 0, len(keyPrefix)+len(enc))
	out = append(out, keyPrefix...)
	return append(out, enc...), nil
}

// ParseKey returns the item name of a LevelDB key of the claude.ai origin.
func ParseKey(k []byte) (string, error) {
	if !bytes.HasPrefix(k, keyPrefix) {
		return "", ErrNotOriginKey
	}
	v, err := Decode(k[len(keyPrefix):])
	if err != nil {
		return "", err
	}
	return v.String()
}

// Value is a raw Local Storage string: its flag and its payload bytes.
type Value struct {
	flag byte
	data []byte
}

// Decode wraps raw stored bytes. It checks the flag and, for UTF-16, the
// length; it does not interpret the text.
func Decode(raw []byte) (Value, error) {
	if len(raw) == 0 {
		return Value{}, ErrEmpty
	}
	switch raw[0] {
	case FlagLatin1:
	case FlagUTF16:
		if (len(raw)-1)%2 != 0 {
			return Value{}, ErrOddUTF16
		}
	default:
		return Value{}, ErrUnknownFlag
	}
	return Value{flag: raw[0], data: append([]byte(nil), raw[1:]...)}, nil
}

// Flag returns the stored flag byte.
func (v Value) Flag() byte { return v.flag }

// Bytes returns the stored form: flag followed by payload.
func (v Value) Bytes() []byte { return append([]byte{v.flag}, v.data...) }

// Units returns the text as UTF-16 code units, exactly as stored.
func (v Value) Units() []uint16 {
	if v.flag == FlagLatin1 {
		u := make([]uint16, len(v.data))
		for i, b := range v.data {
			u[i] = uint16(b)
		}
		return u
	}
	u := make([]uint16, len(v.data)/2)
	for i := range u {
		u[i] = uint16(v.data[2*i]) | uint16(v.data[2*i+1])<<8
	}
	return u
}

// String decodes the value to a Go string. It fails with ErrNotRoundTrip
// unless re-encoding the string yields exactly the stored bytes.
func (v Value) String() (string, error) {
	s := string(utf16.Decode(v.Units()))
	if !bytes.Equal(EncodeString(s), v.Bytes()) {
		return "", ErrNotRoundTrip
	}
	return s, nil
}

// EncodeString encodes s the way Chromium does: Latin-1 with flag 0x01 if
// every code unit is <= 0xFF, otherwise UTF-16LE with flag 0x00.
func EncodeString(s string) []byte {
	units := utf16.Encode([]rune(s))
	latin := true
	for _, u := range units {
		if u > 0xFF {
			latin = false
			break
		}
	}
	if latin {
		out := make([]byte, 1+len(units))
		out[0] = FlagLatin1
		for i, u := range units {
			out[1+i] = byte(u)
		}
		return out
	}
	out := make([]byte, 1+2*len(units))
	out[0] = FlagUTF16
	for i, u := range units {
		out[1+2*i] = byte(u)
		out[2+2*i] = byte(u >> 8)
	}
	return out
}

// DecodeText is Decode followed by String.
func DecodeText(raw []byte) (string, error) {
	v, err := Decode(raw)
	if err != nil {
		return "", err
	}
	return v.String()
}
