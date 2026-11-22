package redact

import (
	"strings"
	"testing"
)

const (
	home    = "/Users/plantedperson"
	uuidA   = "00000000-0000-4000-8000-00000000abcd"
	uuidB   = "00000000-0000-4000-8000-000000001234"
	planted = "plantedproject"
)

func TestString(t *testing.T) {
	r := Redactor{Home: home}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"uuid masked", "account " + uuidA + " ok", "account 0000\u2026 ok"},
		{"upper-case uuid", "id " + strings.ToUpper(uuidA), "id 0000\u2026"},
		{"session id", "local_" + uuidB, "local_0000\u2026"},
		{"home itself", "cwd " + home, "cwd ~"},
		{"home path", "open " + home + "/projects/" + planted + "/x.go", "open ~/" + shortHash("projects/"+planted+"/x.go")},
		{"path with spaces", `"` + home + "/Library/Application Support/Claude-x" + `" done`,
			`"~/` + shortHash("Library/Application Support/Claude-x") + `" done`},
		{"wrapped error", "read " + home + "/a b/c: permission denied", "read ~/" + shortHash("a b/c") + ": permission denied"},
		{"another user's home", "/Users/someoneelse/" + planted, "/Users/" + shortHash("someoneelse") + "/" + planted},
		{"no pii", "nothing here", "nothing here"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.String(tc.in); got != tc.want {
				t.Fatalf("String(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPath(t *testing.T) {
	r := Redactor{Home: home}
	tests := []struct{ in, want string }{
		{home, "~"},
		{home + "/", "~"},
		{home + "/Library/Logs/paritydesk", "~/" + shortHash("Library/Logs/paritydesk")},
		{"/tmp/x", "/tmp/x"},
		{"/tmp/" + uuidA, "/tmp/0000\u2026"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := r.Path(tc.in); got != tc.want {
			t.Errorf("Path(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := len(shortHash("x")); got != 8 {
		t.Errorf("hash length %d", got)
	}
}

func TestKey(t *testing.T) {
	r := Redactor{Home: home}
	tests := []struct{ in, want string }{
		{"code-projects-order." + uuidA, "code-projects-order.<id>"},
		{"LSS-persisted.dframe-store/" + uuidA + "/" + uuidB, "LSS-persisted.dframe-store/<id>/<id>"},
		{"device." + strings.Repeat("ab", 16), "device.<id>"},
		{"sidebarMode", "sidebarMode"},
	}
	for _, tc := range tests {
		if got := r.Key(tc.in); got != tc.want {
			t.Errorf("Key(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUnredacted(t *testing.T) {
	r := Redactor{Home: home, Unredacted: true}
	in := home + "/" + uuidA
	if r.String(in) != in || r.Path(in) != in || r.Key(in) != in {
		t.Fatal("unredacted mode changed its input")
	}
}

func TestPlantedPIINeverSurvives(t *testing.T) {
	r := Redactor{Home: home}
	inputs := []string{
		"switch failed at " + home + "/" + planted + "/worktree: busy",
		`{"path":"` + home + `/Documents/` + planted + `","account":"` + uuidA + `"}`,
		"cwd=" + home + "/" + planted + " other=/Users/plantedperson/" + planted,
		"/Users/plantedperson\t/Users/plantedperson/" + planted,
	}
	for _, in := range inputs {
		out := r.String(in)
		for _, secret := range []string{"plantedperson", planted, uuidA[9:]} {
			if strings.Contains(out, secret) {
				t.Errorf("%q survived in %q", secret, out)
			}
		}
	}
}

func FuzzString(f *testing.F) {
	f.Add(home + "/" + planted + " " + uuidA)
	f.Add("/Users/a/b: c " + uuidB)
	f.Fuzz(func(t *testing.T, s string) {
		out := Redactor{Home: home}.String(s)
		if strings.Contains(out, home) {
			t.Fatalf("home path survived: %q -> %q", s, out)
		}
		if uuidRE.MatchString(out) {
			t.Fatalf("uuid survived: %q -> %q", s, out)
		}
	})
}
