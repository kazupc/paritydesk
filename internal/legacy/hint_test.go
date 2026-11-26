package legacy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseHint(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want Hint
		warn []HintWarning
		err  error
	}{
		{"valid", `{"active":"Work","in_default":"Work","layout_seeded":{"Work":true,"Home":false}}`,
			Hint{Active: "Work", InDefault: "Work", LayoutSeededCount: 2}, nil, nil},
		{"unknown members ignored", `{"in_default":"Home","extra":[1,2,{"x":1}]}`, Hint{InDefault: "Home"}, nil, nil},
		{"wrong type", `{"active":5,"in_default":["x"],"layout_seeded":"x"}`, Hint{}, []HintWarning{HintType}, nil},
		{"null label", `{"in_default":null}`, Hint{}, []HintWarning{HintType}, nil},
		{"label with slash", `{"in_default":"a/b"}`, Hint{}, []HintWarning{HintLabel}, nil},
		{"label with space", `{"in_default":"a b"}`, Hint{}, []HintWarning{HintLabel}, nil},
		{"label with NUL", `{"in_default":"a\u0000b"}`, Hint{}, []HintWarning{HintLabel}, nil},
		{"label with control", `{"in_default":"a\u001bb"}`, Hint{}, []HintWarning{HintLabel}, nil},
		{"label 49", `{"in_default":"` + strings.Repeat("a", 49) + `"}`, Hint{}, []HintWarning{HintLabel}, nil},
		{"label 48", `{"in_default":"` + strings.Repeat("a", 48) + `"}`, Hint{InDefault: strings.Repeat("a", 48)}, nil, nil},
		{"duplicate keys: last wins", `{"in_default":"A","in_default":"B"}`, Hint{InDefault: "B"}, nil, nil},
		{"layout 65", `{"layout_seeded":{` + members(65) + `}}`, Hint{}, []HintWarning{HintType}, nil},
		{"layout 64", `{"layout_seeded":{` + members(64) + `}}`, Hint{LayoutSeededCount: 64}, nil, nil},
		{"array", `["x"]`, Hint{}, nil, ErrHintSyntax},
		{"string", `"x"`, Hint{}, nil, ErrHintSyntax},
		{"empty", ``, Hint{}, nil, ErrHintSyntax},
		{"depth 9", `{"a":` + strings.Repeat("[", 8) + strings.Repeat("]", 8) + `}`, Hint{}, nil, ErrHintSyntax},
		{"depth 8", `{"a":` + strings.Repeat("[", 7) + strings.Repeat("]", 7) + `}`, Hint{}, nil, nil},
		{"trailing data", `{"in_default":"a"} {}`, Hint{}, nil, ErrHintSyntax},
		{"trailing garbage", `{"in_default":"a"}x`, Hint{}, nil, ErrHintSyntax},
		{"trailing space", "{\"in_default\":\"a\"}\n  ", Hint{InDefault: "a"}, nil, nil},
	}
	for _, c := range cases {
		h, w, err := ParseHint([]byte(c.doc))
		if !errors.Is(err, c.err) {
			t.Errorf("%s: err %v, want %v", c.name, err, c.err)
			continue
		}
		if err != nil {
			continue
		}
		if *h != c.want || !reflect.DeepEqual(w, c.warn) {
			t.Errorf("%s: %+v %v, want %+v %v", c.name, *h, w, c.want, c.warn)
		}
	}
}

func members(n int) string {
	var parts []string
	for i := 0; i < n; i++ {
		parts = append(parts, `"m`+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('a'+i/26))+`":true`)
	}
	return strings.Join(parts, ",")
}

func TestReadHint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	good := filepath.Join(dir, "state.json")
	if err := os.WriteFile(good, []byte(`{"active":"A","in_default":"A"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, MaxHintBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]error{
		"state.json":           nil,
		"link.json":            ErrHintSymlink,
		"big.json":             ErrHintTooLarge,
		"missing.json":         ErrHintMissing,
		"":                     ErrHintNotRegular, // the directory itself
		"fifo":                 ErrHintNotRegular,
		"relative/state.json":  ErrHintNotAbs,
		"subdir/../state.json": nil,
	}
	for name, want := range cases {
		p := filepath.Join(dir, name)
		switch name {
		case "relative/state.json":
			p = name
		case "subdir/../state.json":
			p = dir + "/" + name
			if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		done := make(chan error, 1)
		go func() { _, _, err := ReadHint(p); done <- err }()
		select {
		case <-ctx.Done():
			t.Fatalf("%s: ReadHint blocked", name)
		case err := <-done:
			if !errors.Is(err, want) {
				t.Errorf("%s: err %v, want %v", name, err, want)
			}
		}
	}
	h, _, err := ReadHint(good)
	if err != nil || h.InDefault != "A" {
		t.Fatalf("%+v %v", h, err)
	}
	if os.Geteuid() != 0 {
		locked := filepath.Join(dir, "locked.json")
		if err := os.WriteFile(locked, []byte(`{}`), 0o000); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadHint(locked); !errors.Is(err, ErrHintUnreadable) {
			t.Errorf("unreadable: %v", err)
		}
	}
	// Error texts never carry the path or content.
	for _, e := range []error{ErrHintNotAbs, ErrHintNotRegular, ErrHintSymlink, ErrHintMissing, ErrHintTooLarge, ErrHintUnreadable, ErrHintSyntax} {
		if strings.Contains(e.Error(), dir) || strings.Contains(e.Error(), "/") {
			t.Errorf("%v names a path", e)
		}
	}
}
