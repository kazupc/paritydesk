package legacy

import (
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/ids"
)

func folder(name string, acct int) FolderFacts {
	f := FolderFacts{Name: name, Dev: 1, Ino: uint64(len(name)) + uint64(acct), Birth: time.Unix(int64(acct), 0)}
	if acct != 0 {
		f.Account = ids.AccountID(fakeID(acct))
	} else {
		f.IdentityErr = "no account is recorded"
	}
	return f
}

func statusOf(t *testing.T, p *Plan, name string) FolderPlan {
	t.Helper()
	fp := p.Folder(name)
	if fp == nil {
		t.Fatalf("no plan line for %s", name)
	}
	return *fp
}

func TestBuildBasic(t *testing.T) {
	p := Build(PlanInput{
		Folders: []FolderFacts{folder("Claude-Home", 0x12), folder("Claude", 0x11)},
		Aliases: map[string]ids.Alias{"Claude": "work"},
	})
	if p.Status != PlanOK || p.Folders[0].Name != "Claude" {
		t.Fatalf("plan %+v", p)
	}
	c := statusOf(t, p, "Claude")
	if c.Status != StatusAdopt || c.Alias != "work" || c.AliasSource != SourceFlag || c.ConfigFolder != "" || !c.Active {
		t.Errorf("Claude %+v", c)
	}
	h := statusOf(t, p, "Claude-Home")
	if h.Status != StatusAdopt || h.Alias != "home" || h.AliasSource != SourceFolder || h.ConfigFolder != "Claude-Home" {
		t.Errorf("Claude-Home %+v", h)
	}
	if got := len(p.Adopted()); got != 2 {
		t.Errorf("adopted %d", got)
	}
	// Without an alias Claude needs one.
	p = Build(PlanInput{Folders: []FolderFacts{folder("Claude", 0x11), folder("Claude-home", 0x12)}})
	if c := statusOf(t, p, "Claude"); c.Status != StatusSkip || c.Reason != ReasonNeedsAlias {
		t.Errorf("needs alias: %+v", c)
	}
	if h := statusOf(t, p, "Claude-home"); h.ConfigFolder != "" {
		t.Errorf("default parked name recorded: %+v", h)
	}
}

func TestBuildReasons(t *testing.T) {
	unread := folder("Claude-Unread", 0x13)
	unread.ScanErr = true
	foreign := folder("Claude-Other", 0x14)
	foreign.Foreign = true
	reg := folder("Claude-Reg", 0x15)
	reg.Bound, reg.BoundHasState = "reg", true
	fin := folder("Claude-Fin", 0x16)
	fin.Bound = "fin"
	p := Build(PlanInput{
		Folders: []FolderFacts{
			folder("Claude", 0x11), folder("Claude-Excl", 0x12), folder("Claude-a b", 0x17), folder("Claude-3p", 0x18),
			unread, foreign, folder("Claude-NoID", 0), reg, fin,
			folder("Claude-Dup1", 0x19), folder("Claude-Dup2", 0x19), folder("Claude-Copy", 0x11),
			folder("Claude-...", 0x1a), folder("Claude-Taken", 0x1b), folder("Claude-a.b", 0x1c), folder("Claude-a_b", 0x1d),
			folder("Claude-Work", 0x1e),
		},
		Aliases:  map[string]ids.Alias{"Claude": "work", "Claude-Work": "w2"},
		Exclude:  []string{"Claude-Excl"},
		Reserved: []string{"Claude-Other"},
		Existing: []ExistingProfile{{Alias: "taken", Parked: "Claude-taken"}, {Alias: "reg", Parked: "Claude-Reg"}, {Alias: "fin", Parked: "Claude-Fin"}},
	})
	want := map[string][3]string{
		"Claude":        {"skip", ReasonParkCollision, "Claude-work"},
		"Claude-Excl":   {"skip", ReasonExcluded, ""},
		"Claude-a b":    {"skip", ReasonUnsupportedName, ""},
		"Claude-3p":     {"skip", ReasonReserved, ""},
		"Claude-Unread": {"skip", ReasonUnreadable, ""},
		"Claude-Other":  {"skip", ReasonReserved, ""},
		"Claude-NoID":   {"skip", ReasonNoIdentity, "no account is recorded"},
		"Claude-Reg":    {"registered", "", ""},
		"Claude-Fin":    {"finish", "", ""},
		"Claude-Dup1":   {"skip", ReasonDuplicate, "Claude-Dup2"},
		"Claude-Dup2":   {"skip", ReasonDuplicate, "Claude-Dup1"},
		"Claude-Copy":   {"skip", ReasonDuplicate, "Claude"},
		"Claude-...":    {"skip", ReasonNoAlias, "Claude-..."},
		"Claude-Taken":  {"skip", ReasonAliasTaken, "taken"},
		"Claude-a.b":    {"skip", ReasonAliasCollision, "a-b"},
		"Claude-a_b":    {"skip", ReasonAliasCollision, "a-b"},
		"Claude-Work":   {"adopt", "", ""},
	}
	for name, w := range want {
		fp := statusOf(t, p, name)
		if string(fp.Status) != w[0] || fp.Reason != w[1] || fp.ReasonArg != w[2] {
			t.Errorf("%s: %s/%s/%s, want %v", name, fp.Status, fp.Reason, fp.ReasonArg, w)
		}
	}
	if fp := statusOf(t, p, "Claude-Fin"); fp.Alias != "fin" || fp.AliasSource != SourceConfig {
		t.Errorf("finish alias %+v", fp)
	}
	if fp := statusOf(t, p, "Claude-Reg"); fp.Alias != "reg" {
		t.Errorf("registered alias %+v", fp)
	}
	// Foreign without a reserved name.
	p = Build(PlanInput{Folders: []FolderFacts{folder("Claude", 0x11), foreign}, Aliases: map[string]ids.Alias{"Claude": "work"}})
	if fp := statusOf(t, p, "Claude-Other"); fp.Reason != ReasonForeign {
		t.Errorf("foreign %+v", fp)
	}
}

func TestBuildNoActive(t *testing.T) {
	p := Build(PlanInput{Folders: []FolderFacts{folder("Claude-Home", 0x12)}})
	if p.Status != PlanNoActive {
		t.Errorf("status %s", p.Status)
	}
	p = Build(PlanInput{Folders: []FolderFacts{folder("Claude", 0), folder("Claude-Home", 0x12)}})
	if p.Status != PlanNoActiveIdentity {
		t.Errorf("status %s", p.Status)
	}
}

func TestBuildHint(t *testing.T) {
	fs := []FolderFacts{folder("Claude", 0x11), folder("Claude-Home", 0x12)}
	p := Build(PlanInput{Folders: fs, Hint: &Hint{Active: "Work", InDefault: "Work"}})
	c := statusOf(t, p, "Claude")
	if !p.HintUsed || c.Alias != "work" || c.AliasSource != SourceHint || c.ConfigFolder != "Claude-Work" || len(p.HintWarnings) != 0 {
		t.Errorf("consistent hint: %+v %+v", p, c)
	}
	// A flag wins over the hint.
	p = Build(PlanInput{Folders: fs, Hint: &Hint{InDefault: "Work"}, Aliases: map[string]ids.Alias{"Claude": "job"}})
	if c := statusOf(t, p, "Claude"); c.Alias != "job" || c.AliasSource != SourceFlag || p.HintUsed || c.ConfigFolder != "Claude-Work" {
		t.Errorf("flag over hint: %+v", c)
	}
	// Stale: the label has a parked folder.
	p = Build(PlanInput{Folders: fs, Hint: &Hint{InDefault: "home"}})
	if c := statusOf(t, p, "Claude"); c.Reason != ReasonNeedsAlias || p.HintUsed || !slices.Equal(p.HintWarnings, []HintWarning{HintStale}) {
		t.Errorf("stale: %+v %v", c, p.HintWarnings)
	}
	// Mid-switch: warned, still used.
	p = Build(PlanInput{Folders: fs, Hint: &Hint{Active: "Home", InDefault: "Work"}})
	if !slices.Equal(p.HintWarnings, []HintWarning{HintMidSwitch}) || statusOf(t, p, "Claude").Alias != "work" {
		t.Errorf("mid-switch: %+v", p)
	}
	// The label's folder name fails validation: no config folder.
	p = Build(PlanInput{Folders: fs, Hint: &Hint{InDefault: "3p"}, Aliases: map[string]ids.Alias{"Claude": "work"}})
	if c := statusOf(t, p, "Claude"); c.ConfigFolder != "" {
		t.Errorf("reserved label name used: %+v", c)
	}
	// The label equals the default parked name: no config folder.
	p = Build(PlanInput{Folders: fs, Hint: &Hint{InDefault: "work"}})
	if c := statusOf(t, p, "Claude"); c.ConfigFolder != "" || c.Alias != "work" {
		t.Errorf("default name recorded: %+v", c)
	}
	// ApplyCommand turns the hint into an explicit alias.
	p = Build(PlanInput{Folders: fs, Hint: &Hint{InDefault: "Work"}, Exclude: []string{"Claude-Home"}, NoSeed: true, Quarantine: true})
	cmd := ApplyCommand(p)
	if !strings.HasPrefix(cmd, "paritydesk profile adopt --from-legacy --apply --legacy-alias Claude=work --legacy-exclude Claude-Home --legacy-no-seed --legacy-plan ") ||
		strings.Contains(cmd, "quarantine") || strings.Contains(cmd, "Work") {
		t.Errorf("apply command %q", cmd)
	}
	// The printed hash is the plan without --quarantine.
	q := *p
	q.Quarantine = false
	if !strings.HasSuffix(cmd, PlanHash(&q)) || !p.Quarantine {
		t.Error("apply command hash")
	}
}

func TestPlanHash(t *testing.T) {
	mk := func(fs []FolderFacts, mod func(*PlanInput)) *Plan {
		in := PlanInput{Folders: fs, Aliases: map[string]ids.Alias{"Claude": "work"}}
		if mod != nil {
			mod(&in)
		}
		p := Build(in)
		for i := range p.Folders {
			p.Folders[i].Org = ids.OrgID(fakeID(0x99))
			p.Folders[i].ScanOrg = ids.OrgID(fakeID(0x99))
		}
		return p
	}
	fs := []FolderFacts{folder("Claude", 0x11), folder("Claude-Home", 0x12), folder("Claude-Lab", 0x13)}
	base := PlanHash(mk(fs, nil))
	f0, i0, ok := SplitHash(base)
	if !ok {
		t.Fatalf("hash %q", base)
	}
	for k := 0; k < 5; k++ {
		perm := slices.Clone(fs)
		rand.New(rand.NewSource(int64(k))).Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
		if PlanHash(mk(perm, nil)) != base {
			t.Fatal("hash depends on input order")
		}
	}
	folders := map[string]func() *Plan{
		"folder id": func() *Plan { c := slices.Clone(fs); c[1].Ino++; return mk(c, nil) },
		"account":   func() *Plan { c := slices.Clone(fs); c[2].Account = ids.AccountID(fakeID(0x55)); return mk(c, nil) },
		"org":       func() *Plan { p := mk(fs, nil); p.Folders[1].ScanOrg = ""; return p },
		"leftover": func() *Plan {
			p := mk(fs, nil)
			p.Folders[1].Leftovers = []Leftover{{Rel: "x", Kind: KindOtherAccount}}
			return p
		},
	}
	for name, f := range folders {
		ff, _, _ := SplitHash(PlanHash(f()))
		if ff == f0 {
			t.Errorf("%s: folders half unchanged", name)
		}
	}
	inputs := map[string]func(*PlanInput){
		"alias":      func(in *PlanInput) { in.Aliases["Claude-Home"] = "house" },
		"exclude":    func(in *PlanInput) { in.Exclude = []string{"Claude-Lab"} },
		"quarantine": func(in *PlanInput) { in.Quarantine = true },
		"no seed":    func(in *PlanInput) { in.NoSeed = true },
	}
	for name, mod := range inputs {
		ff, ii, _ := SplitHash(PlanHash(mk(fs, mod)))
		if ff != f0 || ii == i0 {
			t.Errorf("%s: folders %v inputs %v changed", name, ff != f0, ii != i0)
		}
	}
	for _, bad := range []string{"", "abc", base + "x", strings.ToUpper(base), strings.Replace(base, ".", "", 1)} {
		if _, _, ok := SplitHash(bad); ok {
			t.Errorf("SplitHash(%q) accepted", bad)
		}
	}
}
