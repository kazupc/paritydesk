package legacy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/ids"
)

// commandInput turns a printed apply command back into the plan input it
// stands for (no hint: the command never names the state file).
func commandInput(t *rapid.T, cmd string, folders []FolderFacts) (PlanInput, string) {
	in := PlanInput{Folders: folders, Aliases: map[string]ids.Alias{}}
	hash := ""
	parts := strings.Fields(cmd)
	for i := 0; i < len(parts); i++ {
		switch parts[i] {
		case "--legacy-alias":
			f, a, _ := strings.Cut(parts[i+1], "=")
			if _, dup := in.Aliases[f]; dup {
				t.Fatalf("folder %s twice in %q", f, cmd)
			}
			in.Aliases[f] = ids.Alias(a)
			i++
		case "--legacy-exclude":
			in.Exclude = append(in.Exclude, parts[i+1])
			i++
		case "--legacy-no-seed":
			in.NoSeed = true
		case "--legacy-plan":
			hash = parts[i+1]
			i++
		case "--quarantine", "--legacy-state":
			t.Fatalf("printed command carries %s: %q", parts[i], cmd)
		}
	}
	return in, hash
}

// Building the plan again from the flags the printed command carries gives
// exactly the hash it prints, for random aliases (colliding ones included),
// excludes, --legacy-no-seed, --quarantine and hints (a mid-switch hint
// included). Only a consistent hint that gives Claude a parked name cannot
// be carried; then only the inputs half differs (the orchestrator rebuilds
// the plan from the command's flags for that case).
func TestApplyCommandReproducesThePlan(t *testing.T) {
	names := []string{"Claude", "Claude-Home", "Claude-Work", "Claude-Lab", "Claude-work2"}
	aliases := []ids.Alias{"home", "work", "lab", "main", "work2"}
	labels := []string{"", "Home", "Work", "Other", "Main"}
	rapid.Check(t, func(t *rapid.T) {
		var folders []FolderFacts
		for i, n := range names {
			if i > 0 && !rapid.Bool().Draw(t, "has-"+n) {
				continue
			}
			acct := 0x10 + i
			if rapid.Bool().Draw(t, "dup-"+n) {
				acct = 0x10 + rapid.IntRange(0, len(names)-1).Draw(t, "acct-"+n)
			}
			folders = append(folders, FolderFacts{Name: n, Account: ids.AccountID(fakeID(acct)), Dev: 1, Ino: uint64(i + 1),
				Birth: time.Unix(int64(i), 0), ScanOrg: ids.OrgID(fakeID(0x100 + i))})
		}
		in := PlanInput{Folders: folders, Aliases: map[string]ids.Alias{},
			NoSeed: rapid.Bool().Draw(t, "noseed"), Quarantine: rapid.Bool().Draw(t, "quarantine")}
		for _, f := range folders {
			if rapid.Bool().Draw(t, "alias-"+f.Name) {
				in.Aliases[f.Name] = aliases[rapid.IntRange(0, len(aliases)-1).Draw(t, "aliasv-"+f.Name)]
			}
			if rapid.IntRange(0, 4).Draw(t, "excl-"+f.Name) == 0 {
				in.Exclude = append(in.Exclude, f.Name)
			}
		}
		if rapid.Bool().Draw(t, "hint") {
			in.Hint = &Hint{InDefault: labels[rapid.IntRange(0, len(labels)-1).Draw(t, "indefault")],
				Active: labels[rapid.IntRange(0, len(labels)-1).Draw(t, "active")]}
		}
		p := Build(in)
		cmd := ApplyCommand(p)
		again, printed := commandInput(t, cmd, folders)
		got := PlanHash(Build(again))
		if got != printed {
			c := p.Folder(ids.DefaultFolder)
			if in.Hint == nil || c == nil || c.ConfigFolder == "" {
				t.Fatalf("printed %q, rebuilt %s", cmd, got)
			}
			if CompareHash(printed, got) != HashInputsDiffer {
				t.Fatalf("hint parked name: folders half differs: %q", cmd)
			}
		}
		if strings.Contains(cmd, "quarantine") {
			t.Fatalf("command %q", cmd)
		}
	})
}

// A mid-switch hint refuses seeding; the hash covers that decision, so the
// printed command must say --legacy-no-seed.
func TestSeedAllowedInHash(t *testing.T) {
	fs := []FolderFacts{folder("Claude", 0x11), folder("Claude-Home", 0x12)}
	p := Build(PlanInput{Folders: fs, Hint: &Hint{Active: "Home2", InDefault: "Work"}})
	if p.SeedAllowed() || !strings.Contains(ApplyCommand(p), "--legacy-no-seed") {
		t.Fatalf("mid-switch: seed %v command %q", p.SeedAllowed(), ApplyCommand(p))
	}
	q := Build(PlanInput{Folders: fs, Aliases: map[string]ids.Alias{"Claude": "work"}})
	_, i1, _ := SplitHash(PlanHash(q))
	r := Build(PlanInput{Folders: fs, Aliases: map[string]ids.Alias{"Claude": "work"}, NoSeed: true})
	_, i2, _ := SplitHash(PlanHash(r))
	if i1 == i2 {
		t.Fatal("seed decision not hashed")
	}
}

func TestCompareHash(t *testing.T) {
	a := strings.Repeat("a", 32)
	b := strings.Repeat("b", 32)
	for _, c := range []struct{ given, now, want string }{
		{a + "." + a, a + "." + a, HashSame},
		{a + "." + b, a + "." + a, HashInputsDiffer},
		{b + "." + a, a + "." + a, HashFoldersDiffer},
		{"junk", a + "." + a, HashFoldersDiffer},
	} {
		if got := CompareHash(c.given, c.now); got != c.want {
			t.Errorf("CompareHash(%q, %q) = %q, want %q", c.given, c.now, got, c.want)
		}
	}
}

func TestFindLeftoversReviewCases(t *testing.T) {
	base := t.TempDir()
	home, work := filepath.Join(base, "Claude-Home"), filepath.Join(base, "Claude")
	H, OH, W, OW := fakeID(0x11), fakeID(0x12), fakeID(0x21), fakeID(0x22)
	cs, sk := "claude-code-sessions/", "local-agent-mode-sessions/skills-plugin/"
	put(t, home, cs+H+"/"+OH+"/local_a.json", "{}")
	put(t, work, cs+W+"/"+OW+"/local_w.json", "{}")
	in := LeftoverInput{Folder: home, Account: ids.AccountID(H), OwnOrgs: []ids.OrgID{ids.OrgID(OH)}, OrgKnown: true,
		Others:    map[ids.AccountID]OtherFolder{ids.AccountID(W): {Name: "Claude", Path: work, Owner: "work"}},
		OtherOrgs: map[ids.OrgID]OtherFolder{ids.OrgID(OW): {Name: "Claude", Path: work, Owner: "work"}},
		Dirs:      testDirs(), Stub: testStubRules()}
	find := func() map[string]Leftover {
		t.Helper()
		got, err := FindLeftovers(in)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]Leftover{}
		for _, l := range got {
			out[l.Rel] = l
		}
		return out
	}

	// A file (not a directory) named like another folder's account, at
	// either layout: reported only, never eligible.
	put(t, home, cs+W, "not a dir")
	put(t, home, sk+OW+"/"+W, "not a dir")
	got := find()
	for _, rel := range []string{cs + W, sk + OW + "/" + W} {
		if l := got[rel]; l.Kind != KindUnrecognized || l.Eligible || l.Why != WhyUnrecognized {
			t.Errorf("%s: %+v", rel, l)
		}
	}

	// Real scheduled tasks only this leftover holds count as unique; an
	// empty stub does not; the same bytes in the owner do not.
	if err := os.Remove(filepath.Join(home, cs+W)); err != nil {
		t.Fatal(err)
	}
	put(t, home, cs+W+"/"+OW+"/local_w.json", "{}")
	tasks := `{"scheduledTasks":[{"id":"t1"}],"recordedSkips":{}}`
	put(t, home, cs+W+"/"+OW+"/scheduled-tasks.json", tasks)
	if l := find()[cs+W]; l.Eligible || l.Unique != 1 || l.Why != WhyUniqueSessions {
		t.Errorf("real tasks only here: %+v", l)
	}
	put(t, work, cs+W+"/"+OW+"/scheduled-tasks.json", tasks)
	if l := find()[cs+W]; !l.Eligible || l.Unique != 0 {
		t.Errorf("same tasks in the owner: %+v", l)
	}
	put(t, work, cs+W+"/"+OW+"/scheduled-tasks.json", `{"scheduledTasks":[{"id":"other"}]}`)
	if l := find()[cs+W]; l.Eligible || l.Unique != 1 {
		t.Errorf("different tasks in the owner: %+v", l)
	}
	put(t, home, cs+W+"/"+OW+"/scheduled-tasks.json", `{"scheduledTasks":[]}`)
	if l := find()[cs+W]; !l.Eligible || l.Unique != 0 {
		t.Errorf("empty stub: %+v", l)
	}

	// A protected path anywhere below a leftover keeps it in place.
	put(t, home, cs+W+"/"+OW+"/scratchowed_x", "1")
	put(t, work, cs+W+"/"+OW+"/scratchowed_x", "1")
	in.Protected = func(rel string) bool { return strings.HasSuffix(rel, "/scratchowed_x") }
	if l := find()[cs+W]; l.Eligible || l.Why != WhyDenied {
		t.Errorf("protected inside: %+v", l)
	}

	// The owner of another account's org is named for mixed-org entries.
	in.Protected = nil
	put(t, home, cs+H+"/"+OW+"/scheduled-tasks.json", "")
	if l := find()[cs+H+"/"+OW]; l.Kind != KindMixedOrgStub || l.Owner != "work" || l.OwnerFolder != "Claude" {
		t.Errorf("mixed org owner: %+v", l)
	}
}
