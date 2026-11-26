package render

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/kazupc/paritydesk/internal/pderr"
)

// TestJSONWithError: a result that ends with a non-zero code carries the
// same error object a failed command prints on its own, after the
// result's own members, which keep their order.
func TestJSONWithError(t *testing.T) {
	var out bytes.Buffer
	p := &Printer{Out: &out, Mode: Mode{JSON: true}}
	v := struct {
		From string `json:"from"`
		Code int    `json:"code"`
	}{"a", 10}
	if err := p.JSONWithError(v, pderr.New(pderr.AppDidNotQuit, "quiescence", "files in the data folders are still in use")); err != nil {
		t.Fatal(err)
	}
	b := out.Bytes()
	if !bytes.HasPrefix(b, []byte(`{"v":1,"from":"a","code":10,"error":{"code":10,`)) || !bytes.HasSuffix(b, []byte("}}\n")) {
		t.Fatalf("document = %s", b)
	}
	var doc struct {
		Error ErrorView `json:"error"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Error.Message != "files in the data folders are still in use" || doc.Error.ID != "PD-E10" {
		t.Fatalf("error = %+v", doc.Error)
	}

	// A bare result code has no message beyond its catalog entry.
	out.Reset()
	if err := p.JSONWithError(v, pderr.New(pderr.AppDidNotQuit, "switch", "")); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte(`"message"`)) {
		t.Fatalf("bare code got a message: %s", out.Bytes())
	}
}
