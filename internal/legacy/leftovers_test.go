package legacy

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
)

func fakeID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

// skillsFixedOrg is the org id the app always reads (manifest skip_names).
// It matches the fake-id pattern; only the skip test uses it.
const skillsFixedOrg = "00000000-0000-4000-8000-000000000001"

func testDirs() []compat.LegacyDir {
	return []compat.LegacyDir{
		{ID: "code-sessions", Root: "claude-code-sessions", Layout: compat.LayoutAcctOrg, Quarantine: true, UniqueCheck: true},
		{ID: "agent-sessions", Root: "local-agent-mode-sessions", Layout: compat.LayoutAcctOrg, SkipNames: []string{"skills-plugin"}, Quarantine: true, UniqueCheck: true},
		{ID: "agent-skills", Root: "local-agent-mode-sessions/skills-plugin", Layout: compat.LayoutOrgAcct, SkipNames: []string{skillsFixedOrg}, Quarantine: true},
		{ID: "scratch-workspaces", Root: "scratch-workspaces", Layout: compat.LayoutAcctOrg},
	}
}

func put(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestFindLeftovers(t *testing.T) {
	base := t.TempDir()
	home, work, dup := filepath.Join(base, "Claude-Home"), filepath.Join(base, "Claude"), filepath.Join(base, "Claude-Old")
	H, OH := fakeID(0x11), fakeID(0x12)
	W, OW := fakeID(0x21), fakeID(0x22)
	D, OD := fakeID(0x31), fakeID(0x32)
	OY, OX, inst := fakeID(0x41), fakeID(0x42), fakeID(0x99)
	cs, as, sk, sw := "claude-code-sessions/", "local-agent-mode-sessions/", "local-agent-mode-sessions/skills-plugin/", "scratch-workspaces/"

	put(t, home, cs+H+"/"+OH+"/local_a.json", "{}")                            // own
	put(t, home, cs+H+"/"+OW+"/scheduled-tasks.json", `{"scheduledTasks":[]}`) // stub under W's org
	put(t, home, cs+H+"/"+OD+"/local_b.json", "{}")                            // data under D's org
	put(t, home, cs+H+"/"+OX+"/local_c.json", "{}")                            // nobody's org: not reported
	put(t, home, cs+W+"/"+OW+"/local_w.json", "{\"x\":1}")                     // W's sessions, also in Claude
	put(t, work, cs+W+"/"+OW+"/local_w.json", "{\"x\":1}")
	put(t, home, as+W+"/"+OW+"/sess-1/a.json", "{}") // only here: unique
	put(t, home, as+inst+"/x/y.json", "{}")          // install id: unrecognized
	put(t, home, as+"00000000/z.json", "{}")         // short name
	put(t, home, as+D+"/"+OD+"/s.json", "{}")        // the duplicate folder's account
	put(t, dup, as+D+"/"+OD+"/s.json", "{}")
	mkdir(t, home, as+"not-a-uuid")
	for _, n := range []string{H, H + "-builtins", W, W + "-builtins", "notuuid"} {
		put(t, home, sk+OY+"/"+n+"/skill.md", "x")
	}
	put(t, home, sk+skillsFixedOrg+"/"+W+"/skill.md", "x")
	put(t, home, sw+W+"/"+OW+"/scratch-2026-01-01-abcdef/f.txt", "hello")
	if err := os.Symlink(work, filepath.Join(home, cs+fakeID(0x51))); err != nil {
		t.Fatal(err)
	}

	in := LeftoverInput{
		Folder: home, Account: ids.AccountID(H), OwnOrgs: []ids.OrgID{ids.OrgID(OH)}, OrgKnown: true,
		Others: map[ids.AccountID]OtherFolder{
			ids.AccountID(W):            {Name: "Claude", Path: work, Owner: "work"},
			ids.AccountID(D):            {Name: "Claude-Old", Path: dup, Owner: ""},
			ids.AccountID(fakeID(0x51)): {Name: "Claude-Link", Path: work, Owner: "link"},
		},
		OtherOrgs: map[ids.OrgID]OtherFolder{
			ids.OrgID(OW): {Name: "Claude", Path: work, Owner: "work"},
			ids.OrgID(OD): {Name: "Claude-Old", Path: dup},
			ids.OrgID(OY): {Name: "Claude", Path: work, Owner: "work"},
		},
		KnownIDs: map[string]ids.Alias{H: "home", OH: "home"},
		Dirs:     testDirs(), Stub: testStubRules(),
	}
	got, err := FindLeftovers(in)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		kind     LeftoverKind
		owner    ids.Alias
		eligible bool
		why      string
		unique   int
	}
	exp := map[string]want{
		cs + H + "/" + OW:               {KindMixedOrgStub, "work", true, "", -1},
		cs + H + "/" + OD:               {KindMixedOrg, "", false, WhyHoldsData, -1},
		cs + W:                          {KindOtherAccount, "work", true, "", 0},
		cs + fakeID(0x51):               {KindOtherAccount, "link", false, WhySymlink, -1},
		as + W:                          {KindOtherAccount, "work", false, WhyUniqueSessions, 1},
		as + inst:                       {KindUnrecognized, "", false, WhyUnrecognized, -1},
		as + "00000000":                 {KindUnrecognized, "", false, WhyShortID, -1},
		as + D:                          {KindOtherAccount, "", true, "", 0},
		sk + OY + "/" + W:               {KindOtherAccount, "work", true, "", -1},
		sk + OY + "/" + W + "-builtins": {KindOtherAccount, "work", true, "", -1},
		sw + W:                          {KindOtherAccount, "work", false, WhyScratch, -1},
	}
	var rels []string
	for _, l := range got {
		rels = append(rels, l.Rel)
		w, ok := exp[l.Rel]
		if !ok {
			t.Errorf("unexpected leftover %s (%s)", l.Rel, l.Kind)
			continue
		}
		if l.Kind != w.kind || l.Owner != w.owner || l.Eligible != w.eligible || l.Why != w.why || l.Unique != w.unique {
			t.Errorf("%s: %+v, want %+v", l.Rel, l, w)
		}
		delete(exp, l.Rel)
	}
	for rel := range exp {
		t.Errorf("missing leftover %s", rel)
	}
	if !slices.IsSorted(rels) {
		t.Errorf("not sorted: %v", rels)
	}
	for _, l := range got {
		if l.Rel == as+"00000000" && l.Possibly != "home" {
			t.Errorf("short id owner %q", l.Possibly)
		}
		if l.Rel == sw+W && (l.Files != 1 || l.Bytes != 5) {
			t.Errorf("counts %d %d", l.Files, l.Bytes)
		}
	}

	// The owner directory missing: every name is unique.
	in.Others[ids.AccountID(W)] = OtherFolder{Name: "Claude", Path: filepath.Join(base, "nowhere"), Owner: "work"}
	got, _ = FindLeftovers(in)
	for _, l := range got {
		if l.Rel == cs+W && (l.Eligible || l.Unique != 1 || l.Why != WhyUniqueSessions) {
			t.Errorf("owner directory missing: %+v", l)
		}
	}

	// Org unknown: the stub is reported, not eligible.
	in.OrgKnown = false
	got, _ = FindLeftovers(in)
	for _, l := range got {
		if l.Rel == cs+H+"/"+OW && (l.Eligible || l.Why != WhyOrgUnknown) {
			t.Errorf("org unknown: %+v", l)
		}
	}

	// Too many entries to fingerprint.
	in.OrgKnown, in.MaxEntries = true, 1
	got, _ = FindLeftovers(in)
	for _, l := range got {
		if l.Rel == as+D && (l.Eligible || l.Why != WhyTooLarge) {
			t.Errorf("too large: %+v", l)
		}
	}

	// An org two accounts share (in OwnOrgs and OtherOrgs) is never a
	// leftover under {acct}/{org}.
	in.MaxEntries = 0
	in.OwnOrgs = append(in.OwnOrgs, ids.OrgID(OW))
	got, _ = FindLeftovers(in)
	for _, l := range got {
		if l.Rel == cs+H+"/"+OW {
			t.Errorf("shared org reported: %+v", l)
		}
	}
}

func TestOwnOrgs(t *testing.T) {
	f := t.TempDir()
	A, O1, O2 := fakeID(0x11), fakeID(0x12), fakeID(0x13)
	put(t, f, "claude-code-sessions/"+A+"/"+O1+"/local_a.json", "{}")
	put(t, f, "claude-code-sessions/"+A+"/"+O2+"/scheduled-tasks.json", "")
	mkdir(t, f, "claude-code-sessions/"+A+"/not-an-org")
	own, stubs, err := OwnOrgs(f, "claude-code-sessions", ids.AccountID(A), testStubRules())
	if err != nil || !slices.Equal(own, []ids.OrgID{ids.OrgID(O1)}) || !slices.Equal(stubs, []ids.OrgID{ids.OrgID(O2)}) {
		t.Fatalf("own %v stubs %v err %v", own, stubs, err)
	}
	own, stubs, err = OwnOrgs(f, "claude-code-sessions", ids.AccountID(fakeID(0x99)), testStubRules())
	if err != nil || own != nil || stubs != nil {
		t.Fatalf("missing account: %v %v %v", own, stubs, err)
	}
}
