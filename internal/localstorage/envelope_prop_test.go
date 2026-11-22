package localstorage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/jsonx"
)

// wsGen draws JSON whitespace, including none.
var wsGen = rapid.SampledFrom([]string{"", "", " ", "\n  ", "\t", "\r\n"})

// jsonValue draws a JSON value text with random whitespace and escapes.
func jsonValue(depth int) *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		kind := rapid.IntRange(0, 5).Draw(t, "kind")
		if depth <= 0 && kind >= 4 {
			kind = 0
		}
		switch kind {
		case 0:
			return strconv.Itoa(rapid.IntRange(-1000, 1000).Draw(t, "int"))
		case 1:
			return rapid.SampledFrom([]string{"true", "false", "null", "1.5e3", "-0.25"}).Draw(t, "lit")
		case 2, 3:
			return quote(t, rapid.String().Draw(t, "str"))
		case 4:
			n := rapid.IntRange(0, 3).Draw(t, "n")
			parts := make([]string, n)
			for i := range parts {
				parts[i] = wsGen.Draw(t, "ws") + jsonValue(depth-1).Draw(t, "elem") + wsGen.Draw(t, "ws")
			}
			return "[" + strings.Join(parts, ",") + "]"
		default:
			names := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z]{1,4}`), 0, 3, rapid.ID[string]).Draw(t, "names")
			parts := make([]string, len(names))
			for i, k := range names {
				parts[i] = wsGen.Draw(t, "ws") + quote(t, k) + wsGen.Draw(t, "ws") + ":" + wsGen.Draw(t, "ws") +
					jsonValue(depth-1).Draw(t, "val") + wsGen.Draw(t, "ws")
			}
			return "{" + strings.Join(parts, ",") + "}"
		}
	})
}

// quote encodes s as a JSON string, sometimes with every character escaped.
func quote(t *rapid.T, s string) string {
	b, err := jsonx.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !rapid.Bool().Draw(t, "escape") {
		return string(b)
	}
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range []rune(s) {
		if r > 0xffff {
			b, _ := json.Marshal(string(r))
			sb.Write(b[1 : len(b)-1])
			continue
		}
		fmt.Fprintf(&sb, `\u%04x`, r)
	}
	sb.WriteByte('"')
	return sb.String()
}

type member struct{ key, raw string }

var reserved = map[string]bool{"value": true, "tabId": true, "timestamp": true}

// envelopeDoc draws an LSS envelope with members in random order, an
// optional tabId and timestamp, and unknown members.
func envelopeDoc(t *rapid.T) (doc string, ms []member) {
	ms = append(ms, member{"value", jsonValue(3).Draw(t, "value")})
	if rapid.Bool().Draw(t, "hasTab") {
		ms = append(ms, member{"tabId", quote(t, rapid.String().Draw(t, "tab"))})
	}
	if rapid.Bool().Draw(t, "hasTS") {
		ms = append(ms, member{"timestamp", strconv.FormatInt(rapid.Int64Min(0).Draw(t, "ts"), 10)})
	}
	extra := rapid.SliceOfNDistinct(rapid.StringMatching(`[A-Za-z_.:-]{1,10}`), 0, 4, rapid.ID[string]).Draw(t, "extra")
	for _, k := range extra {
		if !reserved[k] {
			ms = append(ms, member{k, jsonValue(2).Draw(t, "extraVal")})
		}
	}
	ms = rapid.Permutation(ms).Draw(t, "order")
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = wsGen.Draw(t, "ws") + quote(t, m.key) + wsGen.Draw(t, "ws") + ":" + wsGen.Draw(t, "ws") + m.raw + wsGen.Draw(t, "ws")
	}
	return wsGen.Draw(t, "lead") + "{" + strings.Join(parts, ",") + "}" + wsGen.Draw(t, "trail"), ms
}

func TestEnvelopeEditKeepsOtherMembersProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		doc, ms := envelopeDoc(t)
		text := []byte(doc)
		if err := jsonx.Validate(text); err != nil {
			t.Fatalf("generator produced invalid JSON: %v\n%s", err, doc)
		}
		oldValue, err := jsonx.Get(text, jsonx.Path{"value"})
		if err != nil {
			t.Fatal(err)
		}
		oldTS, tsErr := jsonx.Get(text, jsonx.Path{"timestamp"})
		newValue := json.RawMessage(wsGen.Draw(t, "nvws") + jsonValue(3).Draw(t, "newValue"))
		ts := rapid.Int64Min(0).Draw(t, "newTS")

		out, err := SetEnvelopeValue(text, newValue, ts)
		if err != nil {
			t.Fatal(err)
		}
		if err := jsonx.Validate(out); err != nil {
			t.Fatalf("result invalid: %v", err)
		}
		got, gotTS, err := EnvelopeValue(out)
		if err != nil || !bytes.Equal(got, bytes.TrimSpace(newValue)) || gotTS != ts {
			t.Fatalf("EnvelopeValue = %s %d %v", got, gotTS, err)
		}
		// tabId and unknown members keep their exact bytes.
		for _, m := range ms {
			if m.key == "value" || m.key == "timestamp" {
				continue
			}
			raw, err := jsonx.Get(out, jsonx.Path{m.key})
			if err != nil || string(raw) != m.raw {
				t.Fatalf("member %q changed: %s -> %s (%v)", m.key, m.raw, raw, err)
			}
		}
		// Undoing the two edits restores the original byte for byte.
		back, err := jsonx.Set(out, jsonx.Path{"value"}, oldValue)
		if err != nil {
			t.Fatal(err)
		}
		if tsErr == nil {
			back, err = jsonx.Set(back, jsonx.Path{"timestamp"}, oldTS)
		} else {
			back, err = jsonx.Delete(back, jsonx.Path{"timestamp"})
		}
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(back, text) {
			t.Fatalf("bytes outside the edited members changed:\n%q\n%q", text, back)
		}
	})
}

func TestEnvelopeEditInsideStoredValueProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		doc, _ := envelopeDoc(t)
		ts := rapid.Int64Min(0).Draw(t, "ts")
		// Round trip through the Local Storage codec as the store does.
		stored := EncodeString(doc)
		text, err := DecodeText(stored)
		if err != nil {
			t.Fatal(err)
		}
		out, err := SetEnvelopeValue([]byte(text), json.RawMessage(`{"k":1}`), ts)
		if err != nil {
			t.Fatal(err)
		}
		tab1, err1 := jsonx.Get([]byte(text), jsonx.Path{"tabId"})
		tab2, err2 := jsonx.Get(out, jsonx.Path{"tabId"})
		if (err1 == nil) != (err2 == nil) || !bytes.Equal(tab1, tab2) {
			t.Fatalf("tabId changed: %s -> %s", tab1, tab2)
		}
		if _, err := DecodeText(EncodeString(string(out))); err != nil {
			t.Fatalf("edited value does not round-trip: %v", err)
		}
	})
}
