package localstorage

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"unicode/utf8"
)

var seedRaw = [][]byte{
	nil,
	{FlagLatin1},
	{FlagUTF16},
	{FlagLatin1, 'a', 0xe9, 0xff},
	{FlagUTF16, 0x00, 0x4e},
	{FlagUTF16, 'a', 0x00},              // Latin text under the UTF-16 flag
	{FlagUTF16, 0x00, 0xd8},             // lone high surrogate
	{FlagUTF16, 0x3d, 0xd8, 0x00, 0xde}, // surrogate pair
	{FlagUTF16, 0x00},                   // odd length
	{0x02, 'x'},                         // unknown flag
}

func FuzzDecode(f *testing.F) {
	for _, s := range seedRaw {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		v, err := Decode(raw)
		if err != nil {
			return
		}
		if !bytes.Equal(v.Bytes(), raw) {
			t.Fatalf("Bytes() = %x, want %x", v.Bytes(), raw)
		}
		if v.Flag() != raw[0] {
			t.Fatalf("flag changed")
		}
		_ = v.Units()
		s, err := v.String()
		if err != nil {
			if !errors.Is(err, ErrNotRoundTrip) {
				t.Fatalf("unexpected error %v", err)
			}
			return
		}
		if !utf8.ValidString(s) {
			t.Fatalf("decoded text is not valid UTF-8")
		}
		if !bytes.Equal(EncodeString(s), raw) {
			t.Fatalf("EncodeString(String()) = %x, want %x", EncodeString(s), raw)
		}
		if got, err := DecodeText(raw); err != nil || got != s {
			t.Fatalf("DecodeText disagrees: %q %v", got, err)
		}
	})
}

func FuzzKey(f *testing.F) {
	for _, s := range []string{"", "a", "LSS-persisted.theme", "\u03a9", "\U0001f600", "nul\x00name", "\xff", "\xed\xa0\x80"} {
		f.Add(s, []byte(s))
	}
	f.Fuzz(func(t *testing.T, name string, raw []byte) {
		// ParseKey never panics on arbitrary bytes.
		_, _ = ParseKey(raw)
		_, _ = ParseKey(append(KeyPrefix(), raw...))
		k, err := Key(name)
		if !utf8.ValidString(name) {
			if !errors.Is(err, ErrInvalidUTF8Name) {
				t.Fatalf("invalid UTF-8 name accepted: %v", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(k, KeyPrefix()) {
			t.Fatalf("key lacks the origin prefix")
		}
		got, err := ParseKey(k)
		if err != nil || got != name {
			t.Fatalf("ParseKey(Key(%q)) = %q, %v", name, got, err)
		}
	})
}

func TestNewEnvelopeRoundTrip(t *testing.T) {
	for _, v := range []string{`1`, `"s"`, `{"a":[1,2]}`, `null`, `"<&>"`} {
		env, err := NewEnvelope(json.RawMessage(v), 42)
		if err != nil {
			t.Fatal(err)
		}
		got, ts, err := EnvelopeValue(env)
		if err != nil || string(got) != v || ts != 42 {
			t.Fatalf("%s: got %s %d %v", v, got, ts, err)
		}
	}
	if _, err := NewEnvelope(json.RawMessage(`{"a":1,"a":2}`), 1); err == nil {
		t.Fatal("duplicate keys accepted")
	}
}
