package localstorage

import (
	"bytes"
	"errors"
	"testing"

	"pgregory.net/rapid"
)

func TestKeyRoundTrip(t *testing.T) {
	k, err := Key("LSS-persisted.code-projects-order.00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(k, []byte("_https://claude.ai\x00\x01")) {
		t.Fatalf("unexpected key prefix %q", k[:21])
	}
	name, err := ParseKey(k)
	if err != nil || name != "LSS-persisted.code-projects-order.00000000-0000-4000-8000-000000000001" {
		t.Fatalf("got %q %v", name, err)
	}
}

func TestFlagChoice(t *testing.T) {
	if EncodeString("\u00e9")[0] != FlagLatin1 {
		t.Fatal("U+00E9 must be Latin-1")
	}
	if EncodeString("\u0101")[0] != FlagUTF16 {
		t.Fatal("U+0101 must be UTF-16")
	}
	if got := EncodeString("\u20ac"); !bytes.Equal(got, []byte{0x00, 0xAC, 0x20}) {
		t.Fatalf("got % x", got)
	}
}

func TestLoneSurrogateRefused(t *testing.T) {
	raw := []byte{FlagUTF16, 0x00, 0xD8, 0x41, 0x00} // lone high surrogate, then 'A'
	v, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.String(); !errors.Is(err, ErrNotRoundTrip) {
		t.Fatalf("got %v", err)
	}
	if !bytes.Equal(v.Bytes(), raw) {
		t.Fatal("raw bytes must survive")
	}
}

func TestUTF16FlagWithLatinTextRefused(t *testing.T) {
	// Stored as UTF-16 although Latin-1 would do: not what Chromium writes,
	// so editing is refused rather than silently changing the flag.
	if _, err := DecodeText([]byte{FlagUTF16, 'a', 0}); !errors.Is(err, ErrNotRoundTrip) {
		t.Fatalf("got %v", err)
	}
}

func TestEncodeDecodeProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.String().Draw(t, "s")
		got, err := DecodeText(EncodeString(s))
		if err != nil {
			t.Fatal(err)
		}
		// []rune conversion replaces invalid UTF-8, so compare normalized.
		if got != string([]rune(s)) {
			t.Fatalf("round trip changed the text")
		}
	})
}

func TestEnvelope(t *testing.T) {
	in := []byte(`{"value":["a","b"],"tabId":"","timestamp":5}`)
	v, ts, err := EnvelopeValue(in)
	if err != nil || string(v) != `["a","b"]` || ts != 5 {
		t.Fatalf("got %s %d %v", v, ts, err)
	}
	out, err := SetEnvelopeValue(in, []byte(`["b","a"]`), 9)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"value":["b","a"],"tabId":"","timestamp":9}` {
		t.Fatalf("got %s", out)
	}
}
