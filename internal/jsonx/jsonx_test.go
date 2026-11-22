package jsonx

import (
	"errors"
	"testing"
)

func TestSetReplacesOnlyTheValue(t *testing.T) {
	doc := []byte("{\n  \"a.b\": 1.50,\n  \"c\": {\"x*y\": [1, 2]},\n  \"d\": \"<&>\"\n}\n")
	out, err := Set(doc, Path{"c", "x*y"}, []byte(`[3]`))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a.b\": 1.50,\n  \"c\": {\"x*y\": [3]},\n  \"d\": \"<&>\"\n}\n"
	if string(out) != want {
		t.Fatalf("got %q", out)
	}
}

func TestSetInsertsMissingKey(t *testing.T) {
	cases := map[string]string{
		`{}`:       `{"k@|#":true}`,
		`{"a":1}`:  `{"a":1,"k@|#":true}`,
		` { } `:    ` {"k@|#":true } `,
		`{"a":{}}`: `{"a":{},"k@|#":true}`,
	}
	for in, want := range cases {
		out, err := Set([]byte(in), Path{"k@|#"}, []byte(`true`))
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if string(out) != want {
			t.Fatalf("%s: got %s want %s", in, out, want)
		}
	}
}

func TestSetMissingParent(t *testing.T) {
	if _, err := Set([]byte(`{"a":1}`), Path{"b", "c"}, []byte(`1`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestDelete(t *testing.T) {
	cases := []struct{ in, key, want string }{
		{`{"a":1,"b":2,"c":3}`, "a", `{"b":2,"c":3}`},
		{`{"a":1,"b":2,"c":3}`, "b", `{"a":1,"c":3}`},
		{`{"a":1,"b":2,"c":3}`, "c", `{"a":1,"b":2}`},
		{`{"a":1}`, "a", `{}`},
	}
	for _, c := range cases {
		out, err := Delete([]byte(c.in), Path{c.key})
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != c.want {
			t.Fatalf("delete %s from %s: got %s", c.key, c.in, out)
		}
		if err := Validate(out); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDuplicateKeysRefused(t *testing.T) {
	if err := Validate([]byte(`{"a":{"x":1,"x":2}}`)); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("got %v", err)
	}
	if _, err := Get([]byte(`{"a":1,"a":2}`), Path{"a"}); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("got %v", err)
	}
}

func TestEscapedKeyMatchesLiterally(t *testing.T) {
	v, err := Get([]byte(`{"a.b":7,"a":{"b":8}}`), Path{"a.b"})
	if err != nil || string(v) != "7" {
		t.Fatalf("got %s %v", v, err)
	}
}

func TestTopLevelString(t *testing.T) {
	doc := []byte(`{"secret":{"x":"do-not-decode"},"lastKnownAccountUuid":"u","n":[1]}`)
	s, ok, err := TopLevelString(doc, "lastKnownAccountUuid")
	if err != nil || !ok || s != "u" {
		t.Fatalf("got %q %v %v", s, ok, err)
	}
	if ok, _ := HasTopLevelKey(doc, "secret"); !ok {
		t.Fatal("secret key not found")
	}
	if _, _, err := TopLevelString([]byte(`{"k":"a","k":"b"}`), "k"); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("got %v", err)
	}
}

func TestSyntaxErrors(t *testing.T) {
	for _, in := range []string{``, `{`, `{"a"}`, `[1,]`, `01`, `"\x"`, `{"a":1} x`, "\"\x01\""} {
		if err := Validate([]byte(in)); err == nil {
			t.Fatalf("%q: expected error", in)
		}
	}
}
