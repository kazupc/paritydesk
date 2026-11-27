package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type alias string

type point struct{ X, Y int }

// Text marshals to its rendering, unmarshals as Raw, lists join,
// MapStrings reaches nested arguments, Tx normalizes.
func TestTextJSON(t *testing.T) {
	Register(map[Msg]string{"test.text": "t %s"})
	tx := Tx("test.text", "a")
	b, err := json.Marshal(tx)
	if err != nil || string(b) != `"t a"` {
		t.Fatalf("marshal = %s %v", b, err)
	}
	var back Text
	if err := json.Unmarshal([]byte(`"t <b>"`), &back); err != nil || back.String() != "t <b>" || back.ID != "" {
		t.Fatalf("unmarshal = %+v %v", back, err)
	}
	if b, _ := json.Marshal(Raw("<a&b>")); string(b) != "\"\\u003ca\\u0026b\\u003e\"" {
		t.Errorf("json.Marshal applies its HTML escaping: %s", b)
	}
	l := List{Raw("x"), Tx("test.text", "y")}
	if got := l.String(); got != "x; t y" {
		t.Errorf("list = %q", got)
	}
	if got := (CommaList{Raw("x"), Raw("y")}).String(); got != "x, y" {
		t.Errorf("comma list = %q", got)
	}
	nested := Tx("test.text", Tx("test.text", "in")).MapStrings(strings.ToUpper)
	if got := nested.String(); got != "t t IN" {
		t.Errorf("MapStrings nested = %q", got)
	}
	if got := Raw("raw").MapStrings(strings.ToUpper).String(); got != "RAW" {
		t.Errorf("MapStrings raw = %q", got)
	}
	n := Tx("test.text", alias("a"), int32(4), point{1, 2})
	if _, ok := n.Args[0].(string); !ok {
		t.Errorf("named string kept its type: %T", n.Args[0])
	}
	if _, ok := n.Args[1].(int64); !ok {
		t.Errorf("int32 became %T", n.Args[1])
	}
	if s, ok := n.Args[2].(string); !ok || s != "{1 2}" {
		t.Errorf("struct became %T %v", n.Args[2], n.Args[2])
	}
	if got := Tx("test.text", "long text").Cut(4).String(); got != "t l…" {
		t.Errorf("Cut = %q", got)
	}
}

// Error renders its text, unwraps to its base and is a Texter.
func TestErrorType(t *testing.T) {
	Register(map[Msg]string{"test.err": "something %s"})
	e := NewErr("test.err", "broke")
	if e.Error() != "something broke" {
		t.Fatalf("Error() = %q", e.Error())
	}
	base := errors.New("base")
	w := fmt.Errorf("ctx: %w", WrapErr(base, Tx("test.err", "x")))
	if !errors.Is(w, base) {
		t.Error("errors.Is lost the base")
	}
	var tx Texter
	if !errors.As(w, &tx) || tx.I18nText().ID != "test.err" {
		t.Error("errors.As found no Texter")
	}
}
