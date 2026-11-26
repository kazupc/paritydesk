package legacy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/compat"
)

const stubName = "scheduled-tasks.json"

func testStubRules() StubRules {
	return StubRulesFrom(compat.Legacy{
		IgnoreNames: []string{".DS_Store"},
		Stubs: []compat.LegacyStub{{File: stubName,
			EmptyMembers: []string{"scheduledTasks", "recordedSkips", "recordedPlaceSkips", "runRetries"},
			FlagMembers:  []string{"sundayAliasBoundaryStamped", "dayFieldsOrBoundaryStamped"}}},
	})
}

// stubDir makes a directory holding the fixture as scheduled-tasks.json
// plus extra files (name -> content).
func stubDir(t *testing.T, fixture string, extra map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	if fixture != "" {
		b, err := os.ReadFile(filepath.Join("testdata", "stubs", fixture))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, stubName), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for n, b := range extra {
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestIsStub(t *testing.T) {
	// A binary .DS_Store: never parsed.
	ds := []byte{0x00, 0x00, 0x00, 0x01, 'B', 'u', 'd', '1', 0xff, 0xfe}
	stubs := map[string]string{
		"zero":       "zero-bytes.json",
		"containers": "empty-containers.json",
		"flags":      "with-flags.json",
		"all":        "all-members.json",
		"object":     "empty-object.json",
		"null":       "null-and-space.json",
	}
	r := testStubRules()
	for name, fx := range stubs {
		ok, err := IsStub(stubDir(t, fx, nil), r)
		if err != nil || !ok {
			t.Errorf("%s: not a stub (%v)", name, err)
		}
	}
	if ok, err := IsStub(stubDir(t, "empty-containers.json", map[string][]byte{".DS_Store": ds}), r); err != nil || !ok {
		t.Errorf("with .DS_Store: %v %v", ok, err)
	}
	if ok, _ := IsStub(stubDir(t, "", nil), r); !ok {
		t.Error("an empty directory is a stub")
	}
	notStubs := map[string]string{
		"unparsable":     "not-unparsable.json",
		"nonempty array": "not-nonempty-array.json",
		"flag string":    "not-flag-string.json",
		"unknown member": "not-unknown-member.json",
	}
	for name, fx := range notStubs {
		if ok, err := IsStub(stubDir(t, fx, nil), r); err != nil || ok {
			t.Errorf("%s: a stub (%v)", name, err)
		}
	}
	if ok, _ := IsStub(stubDir(t, "zero-bytes.json", map[string][]byte{"local_x.json": []byte("{}")}), r); ok {
		t.Error("a session file beside it is not a stub")
	}
	sub := stubDir(t, "zero-bytes.json", nil)
	if err := os.Mkdir(filepath.Join(sub, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsStub(sub, r); ok {
		t.Error("a subdirectory is not a stub")
	}
	five := map[string][]byte{}
	for i := 0; i < 4; i++ {
		five[fmt.Sprintf(".DS_Store%d", i)] = nil
	}
	if ok, _ := IsStub(stubDir(t, "zero-bytes.json", five), r); ok {
		t.Error("5 files are not a stub")
	}
	big := stubDir(t, "", map[string][]byte{stubName: []byte(`{"scheduledTasks":[]` + strings.Repeat(" ", 65<<10) + `}`)})
	if ok, _ := IsStub(big, r); ok {
		t.Error("a 65 KiB file is not a stub")
	}
	link := stubDir(t, "", nil)
	if err := os.Symlink(filepath.Join(stubDir(t, "zero-bytes.json", nil), stubName), filepath.Join(link, stubName)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsStub(link, r); ok {
		t.Error("a symlinked stub file is not a stub")
	}
}

// isStubJSON is true exactly when every member is allowed with an
// allowed value.
func TestPropIsStubJSON(t *testing.T) {
	f := testStubRules().Files[stubName]
	rapid.Check(t, func(t *rapid.T) {
		names := append(append([]string{"other", "x"}, f.Empty...), f.Flags...)
		values := []string{`[]`, `{}`, `null`, `true`, `false`, `[1]`, `{"a":1}`, `"s"`, `0`}
		doc := map[string]json.RawMessage{}
		want := true
		for i := 0; i < rapid.IntRange(0, 6).Draw(t, "n"); i++ {
			k := rapid.SampledFrom(names).Draw(t, "k")
			v := rapid.SampledFrom(values).Draw(t, "v")
			doc[k] = json.RawMessage(v)
		}
		for k, v := range doc {
			switch {
			case contains(f.Empty, k):
				want = want && (string(v) == "[]" || string(v) == "{}" || string(v) == "null")
			case contains(f.Flags, k):
				want = want && (string(v) == "true" || string(v) == "false")
			default:
				want = false
			}
		}
		b, _ := json.Marshal(doc)
		if got := isStubJSON(b, f); got != want {
			t.Fatalf("isStubJSON(%s) = %v, want %v", b, got, want)
		}
	})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
