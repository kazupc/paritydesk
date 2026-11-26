package scratch_test

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
)

func FuzzRefs(f *testing.F) {
	f.Add(`{"cwd":"/d/Claude/scratch-workspaces/` + ws1 + `/x"}`)
	f.Add(`{"gitAnchors":[{"gitRoot":"/d/Claude/scratch-workspaces/a/b/c"}]}`)
	f.Add(`[1,2]`)
	f.Fuzz(func(t *testing.T, rec string) {
		if !json.Valid([]byte(rec)) {
			return
		}
		snap := &model.RowSnapshot{Row: model.RowSessions, Items: map[string]model.Item{"k": {Key: "k", Value: json.RawMessage(rec)}}}
		refs, unrec, err := scratch.Refs(snap, members, []string{"/d/Claude/scratch-workspaces"}, leafR)
		if err != nil || unrec < 0 {
			return
		}
		for _, r := range refs {
			if !scratch.ValidWorkspaceKey(r.Key) || !leafR.MatchString(r.Leaf) {
				t.Fatalf("invalid ref %+v", r)
			}
		}
	})
}

func FuzzSplitKey(f *testing.F) {
	for _, k := range []string{ws1 + "/", ws1 + "/a/", ws1 + "/a/b", "x", ws1 + "/../a"} {
		f.Add(k)
	}
	f.Fuzz(func(t *testing.T, key string) {
		ws, rel, kind, err := scratch.SplitKey(key)
		if err != nil {
			return
		}
		if !scratch.ValidWorkspaceKey(ws) || (kind != scratch.KeyRoot && !scratch.ValidRel(rel)) {
			t.Fatalf("SplitKey(%q) accepted an invalid key", key)
		}
		var back string
		switch kind {
		case scratch.KeyRoot:
			back = scratch.RootKey(ws)
		case scratch.KeyDir:
			back = scratch.DirKey(ws, rel)
		default:
			back = scratch.EntryKey(ws, rel)
		}
		if back != key {
			t.Fatalf("SplitKey(%q) does not round trip: %q", key, back)
		}
	})
}

func FuzzSplitRefs(f *testing.F) {
	f.Add(ws1 + "," + ws2)
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		keys, err := scratch.SplitRefs(s)
		if err != nil {
			return
		}
		if scratch.JoinRefs(keys) != s {
			t.Fatalf("SplitRefs(%q) does not round trip", s)
		}
	})
}

func FuzzConflictName(f *testing.F) {
	f.Add("dir/app.py", "home")
	f.Add(".env", "a")
	f.Fuzz(func(t *testing.T, rel, alias string) {
		if !scratch.ValidRel(rel) || strings.ContainsAny(alias, "/\x00") {
			return
		}
		name, ok := scratch.ConflictName(rel, "home", "2026-01-02", nil)
		if !ok {
			return
		}
		base := name[strings.LastIndex(name, "/")+1:]
		if len(base) > 255 || !utf8.ValidString(name) || name == rel {
			t.Fatalf("ConflictName(%q) = %q", rel, name)
		}
	})
}

func FuzzFoldName(f *testing.F) {
	f.Add("A\u00e9")
	f.Add("e\u0301")
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}
		a := scratch.FoldName(s)
		if scratch.FoldName(a) != a {
			t.Fatalf("FoldName is not idempotent on %q", s)
		}
		if scratch.FoldName(strings.ToUpper(s)) != scratch.FoldName(strings.ToLower(s)) && utf8.RuneCountInString(s) == utf8.RuneCountInString(strings.ToUpper(s)) && isASCII(s) {
			t.Fatalf("FoldName(%q) differs by case", s)
		}
	})
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
