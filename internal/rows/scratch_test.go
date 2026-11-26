package rows

import (
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
)

const (
	scrA    = "00000000-0000-4000-8000-00000000000a"
	scrB    = "00000000-0000-4000-8000-00000000000b"
	scrWS   = scrA + "/" + scrB + "/scratch-2026-01-02-0a0b0c"
	scrWS2  = scrA + "/" + scrB + "/scratch-2026-01-03-0d0e0f"
	scrDate = "2026-01-02"
)

var scrLeaf = regexp.MustCompile(`^scratch-[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9a-f]{6}$`)

// tree describes one folder's workspace: rel -> content ("" for a
// directory when rel ends in "/", "->x" for a symlink). A nil tree is a
// folder without the workspace.
type tree map[string]string

// scrSnap builds a ReadRefs-like snapshot of ws from a tree: a root
// marker, items, git unit items over their members. mtime stamps files.
func scrSnap(t testing.TB, ws string, tr tree, mtime int64) *model.RowSnapshot {
	t.Helper()
	s := emptySnapshot(model.RowScratch)
	s.Tombstones = nil
	if tr == nil {
		return s
	}
	root, err := scratch.RootItem(ws, 0o755, "")
	if err != nil {
		t.Fatal(err)
	}
	s.Items[root.Key] = root
	add := func(it model.Item, err error) {
		if err != nil {
			t.Fatal(err)
		}
		s.Items[it.Key] = it
	}
	dirs := map[string]bool{}
	for rel, content := range tr {
		r := strings.TrimSuffix(rel, "/")
		for d := path.Dir(r); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
		switch {
		case strings.HasSuffix(rel, "/"):
			dirs[r] = true
		case strings.HasPrefix(content, "->"):
			add(scratch.LinkItem(scratch.EntryKey(ws, r), strings.TrimPrefix(content, "->"), mtime))
		default:
			add(scratch.FileItem(scratch.EntryKey(ws, r), model.HashBytes([]byte(content)), int64(len(content)), 0o644, mtime))
		}
	}
	var units []string
	for d := range dirs {
		k := scratch.DirKey(ws, d)
		if scratch.IsGitUnitKey(k) {
			units = append(units, k)
			continue
		}
		add(scratch.DirItem(k, 0o755))
	}
	for _, u := range units {
		add(scratch.UnitItem(u, 0o755, s.Items))
	}
	return s
}

// scrCase is one merge of workspace scrWS.
type scrCase struct {
	s, sBase, t, tBase tree
	canon              *model.RowSnapshot // nil: derived from the bases
	sValid, tValid     bool
	sTime, tTime       int64
	pol                func(*ScratchPolicy)
}

func scrMerge(t *testing.T, c scrCase) (Output, *model.RowSnapshot, *model.RowSnapshot) {
	t.Helper()
	if c.sTime == 0 {
		c.sTime = 1000
	}
	if c.tTime == 0 {
		c.tTime = 1000
	}
	sSnap, tSnap := scrSnap(t, scrWS, c.s, c.sTime), scrSnap(t, scrWS, c.t, c.tTime)
	in := Input{
		Row:      model.RowScratch,
		Leaving:  Side{Alias: aliasA, Snapshot: sSnap, BaseValid: c.sValid, LiveEpoch: 7},
		Entering: Side{Alias: aliasB, Snapshot: tSnap, BaseValid: c.tValid, LiveEpoch: 3},
		Policy:   openPolicy(),
	}
	if c.sValid {
		in.Leaving.Base = scrSnap(t, scrWS, c.sBase, 500)
	}
	if c.tValid {
		in.Entering.Base = scrSnap(t, scrWS, c.tBase, 500)
	}
	in.Canonical = c.canon
	if in.Canonical == nil && c.sValid {
		in.Canonical = scrSnap(t, scrWS, c.sBase, 500)
	}
	in.Policy.Scratch = &ScratchPolicy{PropagateDeletions: true, Leaf: scrLeaf, ConflictDate: scrDate,
		Refs: []string{scrWS}, Referenced: []string{scrWS},
		Exclude: []string{".DS_Store", "**/.DS_Store", "**/.git/*.lock"}, HeldBack: []string{"node_modules"},
		ReservedGlobs: []string{"local_*.json.tmp"}}
	if c.pol != nil {
		c.pol(in.Policy.Scratch)
	}
	out, err := scratchModule{}.Merge(in)
	if err != nil {
		t.Fatal(err)
	}
	checkScratchInvariants(t, out, in.Policy.Scratch.Refs)
	return out, sSnap, tSnap
}

// checkScratchInvariants: keys(Desired) ⊆ Refs, never x and x/..., no
// tombstones in Desired.
func checkScratchInvariants(t *testing.T, out Output, refs []string) {
	t.Helper()
	if out.Desired == nil {
		return
	}
	for k := range out.Desired.Items {
		ws := scratch.WorkspaceOf(k)
		found := false
		for _, r := range refs {
			found = found || r == ws
		}
		if !found {
			t.Errorf("Desired key %q outside the carry set", k)
		}
		v, _ := scratch.Decode(out.Desired.Items[k])
		if (v.K == scratch.KindFile || v.K == scratch.KindLink) && hasPrefixKey(out.Desired.Items, k) {
			t.Errorf("Desired holds both %q and keys below it", k)
		}
	}
	if len(out.Desired.Tombstones) > 0 {
		t.Error("Desired carries tombstones")
	}
}

func fileKey(rel string) string { return scratch.EntryKey(scrWS, rel) }

func desiredContent(t *testing.T, out Output, rel string) (string, bool) {
	t.Helper()
	it, ok := out.Desired.Items[fileKey(rel)]
	if !ok {
		return "", false
	}
	v, _ := scratch.Decode(it)
	return v.SHA256, true
}

func h(s string) string { return model.HashBytes([]byte(s)) }

func changeOf(out Output, key string) model.ChangeKind {
	for _, c := range out.Changes {
		if c.Key == key && c.Kind != model.ChangeSkipped {
			return c.Kind
		}
	}
	return ""
}

func hasScrNote(out Output, key, n string) bool {
	for _, c := range out.Changes {
		if c.Kind == model.ChangeSkipped && c.Key == key && c.Note == n {
			return true
		}
	}
	return false
}

// TestScratchCaseTable covers every row of spec-scratch 7.6.
func TestScratchCaseTable(t *testing.T) {
	base := tree{"f": "v0", "keep": "k"}
	tests := []struct {
		name     string
		c        scrCase
		want     string // content of f in Desired, "" = absent
		change   model.ChangeKind
		conflict bool
		confirm  bool
	}{
		{"unchanged both", scrCase{s: base, sBase: base, t: base, tBase: base, sValid: true, tValid: true}, "v0", "", false, false},
		{"S modified", scrCase{s: tree{"f": "v1", "keep": "k"}, sBase: base, t: base, tBase: base, sValid: true, tValid: true}, "v1", model.ChangeUpdated, false, false},
		{"S added", scrCase{s: tree{"f": "v1", "keep": "k"}, sBase: tree{"keep": "k"}, t: tree{"keep": "k"}, tBase: tree{"keep": "k"}, sValid: true, tValid: true}, "v1", model.ChangeAdded, false, false},
		{"T modified", scrCase{s: base, sBase: base, t: tree{"f": "v2", "keep": "k"}, tBase: base, sValid: true, tValid: true}, "v2", "", false, false},
		{"both modified equally", scrCase{s: tree{"f": "v3", "keep": "k"}, sBase: base, t: tree{"f": "v3", "keep": "k"}, tBase: base, sValid: true, tValid: true}, "v3", "", false, false},
		{"both modified differently", scrCase{s: tree{"f": "vs", "keep": "k"}, sBase: base, t: tree{"f": "vt", "keep": "k"}, tBase: base, sValid: true, tValid: true}, "vs", model.ChangeUpdated, true, false},
		{"S removed", scrCase{s: tree{"keep": "k"}, sBase: base, t: base, tBase: base, sValid: true, tValid: true}, "", model.ChangeDeleted, false, false},
		{"S removed, T modified", scrCase{s: tree{"keep": "k"}, sBase: base, t: tree{"f": "vt", "keep": "k"}, tBase: base, sValid: true, tValid: true}, "vt", model.ChangeRevived, false, false},
		{"both removed", scrCase{s: tree{"keep": "k"}, sBase: base, t: tree{"keep": "k"}, tBase: base, sValid: true, tValid: true}, "", "", false, false},
		{"invalid base, both differ", scrCase{s: tree{"f": "vs", "keep": "k"}, t: tree{"f": "vt", "keep": "k"}, tBase: base, tValid: true, sTime: 2000, tTime: 3000}, "vt", "", true, true},
		{"invalid base, one side absent", scrCase{s: tree{"f": "vs", "keep": "k"}, t: tree{"keep": "k"}, tBase: base, tValid: true}, "vs", model.ChangeAdded, false, false},
		{"invalid base, T only", scrCase{s: tree{"keep": "k"}, t: tree{"f": "vt", "keep": "k"}, sBase: base, sValid: true}, "vt", "", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, _, _ := scrMerge(t, tc.c)
			got, ok := desiredContent(t, out, "f")
			switch {
			case tc.want == "" && ok:
				t.Errorf("f present in Desired")
			case tc.want != "" && got != h(tc.want):
				t.Errorf("f = %s, want content %q", got, tc.want)
			}
			if c := changeOf(out, fileKey("f")); c != tc.change {
				t.Errorf("change %q, want %q", c, tc.change)
			}
			if (len(out.Conflicts) > 0) != tc.conflict {
				t.Errorf("conflicts %+v", out.Conflicts)
			}
			if tc.conflict {
				sib := "f (conflict from " + string(out.Conflicts[0].Loser) + " " + scrDate + ")"
				if _, ok := out.Desired.Items[fileKey(sib)]; !ok {
					t.Errorf("no sibling %q: %v", sib, out.Desired.Keys())
				}
				if _, ok := out.Canonical.Items[fileKey(sib)]; !ok {
					t.Error("the sibling is not in canonical")
				}
			}
			if out.NeedsConfirmation != tc.confirm {
				t.Errorf("NeedsConfirmation %v", out.NeedsConfirmation)
			}
			if _, ok := desiredContent(t, out, "keep"); !ok {
				t.Error("an untouched file was lost")
			}
		})
	}
}

// TestScratchGitUnits covers every row of spec-scratch 7.4.
func TestScratchGitUnits(t *testing.T) {
	g0 := tree{".git/HEAD": "h0", ".git/refs/x": "r0", "f": "x"}
	gs := tree{".git/HEAD": "hs", ".git/new": "n", "f": "x"}
	gt := tree{".git/HEAD": "ht", ".git/refs/x": "r0", "f": "x"}
	unit := scratch.DirKey(scrWS, ".git")
	divergedCanon := func(t *testing.T) *model.RowSnapshot {
		c := scrSnap(t, scrWS, g0, 500)
		div, _ := scratch.DivergedItem(unit)
		c.Items[unit] = div
		return c
	}
	members := func(out Output) string {
		var ks []string
		for k, it := range out.Desired.Items {
			if u, ok := scratch.GitUnit(k); ok && u == unit && scratch.KindOf(it) == scratch.KindFile {
				v, _ := scratch.Decode(it)
				ks = append(ks, strings.TrimPrefix(k, unit)+"="+v.SHA256[:6])
			}
		}
		sort.Strings(ks)
		return strings.Join(ks, ",")
	}
	sideMembers := func(t *testing.T, tr tree) string {
		return members(Output{Desired: scrSnap(t, scrWS, tr, 1)})
	}
	t.Run("one-sided change carries S whole", func(t *testing.T) {
		out, _, _ := scrMerge(t, scrCase{s: gs, sBase: g0, t: g0, tBase: g0, sValid: true, tValid: true})
		if members(out) != sideMembers(t, gs) {
			t.Errorf("members %s, want S's %s", members(out), sideMembers(t, gs))
		}
		if changeOf(out, unitKeyFile(".git/refs/x")) != model.ChangeDeleted {
			t.Error("T's member not in S is not removed")
		}
	})
	t.Run("T changed only keeps T", func(t *testing.T) {
		out, _, _ := scrMerge(t, scrCase{s: g0, sBase: g0, t: gt, tBase: g0, sValid: true, tValid: true})
		if members(out) != sideMembers(t, gt) {
			t.Errorf("members %s", members(out))
		}
	})
	t.Run("both changed: T keeps its own, diverged", func(t *testing.T) {
		out, _, _ := scrMerge(t, scrCase{s: gs, sBase: g0, t: gt, tBase: g0, sValid: true, tValid: true})
		if members(out) != sideMembers(t, gt) || !hasScrNote(out, unit, scratch.NoteGitConflict) {
			t.Errorf("members %s notes %+v", members(out), out.Changes)
		}
		if !scratch.IsDiverged(out.Canonical.Items[unit]) {
			t.Error("canonical is not the diverged marker")
		}
		for _, c := range out.Conflicts {
			if c.Key == unit {
				t.Error("the unit conflict was not dropped")
			}
		}
	})
	t.Run("diverged, agreed again", func(t *testing.T) {
		out, sSnap, _ := scrMerge(t, scrCase{s: gt, sBase: g0, t: gt, tBase: g0, sValid: true, tValid: true, canon: divergedCanon(t)})
		if members(out) != sideMembers(t, gt) || out.Canonical.Items[unit].Hash != sSnap.Items[unit].Hash {
			t.Errorf("members %s canon %s", members(out), out.Canonical.Items[unit].Value)
		}
	})
	t.Run("diverged, T lacks: created from S", func(t *testing.T) {
		out, _, _ := scrMerge(t, scrCase{s: gs, sBase: g0, t: tree{"f": "x"}, tBase: tree{"f": "x"}, sValid: true, tValid: true, canon: divergedCanon(t)})
		if members(out) != sideMembers(t, gs) || !scratch.IsDiverged(out.Canonical.Items[unit]) {
			t.Errorf("members %s", members(out))
		}
	})
	t.Run("diverged, still different", func(t *testing.T) {
		out, _, _ := scrMerge(t, scrCase{s: gs, sBase: gs, t: gt, tBase: gt, sValid: true, tValid: true, canon: divergedCanon(t)})
		if members(out) != sideMembers(t, gt) || !hasScrNote(out, unit, scratch.NoteGitDiverged) || !scratch.IsDiverged(out.Canonical.Items[unit]) {
			t.Errorf("members %s notes %+v", members(out), out.Changes)
		}
	})
	t.Run("S removed the unit", func(t *testing.T) {
		out, _, _ := scrMerge(t, scrCase{s: tree{"f": "x"}, sBase: g0, t: g0, tBase: g0, sValid: true, tValid: true})
		if members(out) != "" || changeOf(out, unitKeyFile(".git/HEAD")) != model.ChangeDeleted {
			t.Errorf("members %s changes %+v", members(out), out.Changes)
		}
	})
	t.Run("S's escaping links are not taken with the unit", func(t *testing.T) {
		sl := tree{".git/HEAD": "hs", ".git/hooks/abs": "->/usr/local/bin/x", ".git/hooks/up": "->../../../../../x",
			".git/hooks/in": "->abs", "f": "x"}
		tl := tree{".git/HEAD": "h0", ".git/hooks/up": "->elsewhere", "f": "x"}
		for name, tr := range map[string]tree{"T lacks the unit": {"f": "x"}, "T has the unit": tl} {
			base := tree{"f": "x"}
			if name == "T has the unit" {
				base = tl
			}
			out, _, _ := scrMerge(t, scrCase{s: sl, sBase: base, t: tr, tBase: base, sValid: true, tValid: true})
			for k, it := range out.Desired.Items {
				if v, ok := scratch.Decode(it); ok && v.K == scratch.KindLink && !scratch.LinkInside(k, v.Target) {
					if tit, inT := scrSnap(t, scrWS, tr, 1).Items[k]; !inT || tit.Hash != it.Hash {
						t.Errorf("%s: escaping link %s -> %s in Desired", name, k, v.Target)
					}
				}
			}
			if !hasScrNote(out, unitKeyFile(".git/hooks/abs"), scratch.NoteLinkKept) {
				t.Errorf("%s: no link-kept note: %+v", name, out.Changes)
			}
			if _, ok := out.Desired.Items[unitKeyFile(".git/hooks/in")]; !ok {
				t.Errorf("%s: the link inside the workspace was dropped", name)
			}
			if _, ok := out.Desired.Items[unitKeyFile(".git/HEAD")]; !ok {
				t.Errorf("%s: S's unit was not taken", name)
			}
			// The unit item is recomputed over the members actually kept.
			want, err := scratch.UnitItem(unit, 0o755, out.Desired.Items)
			if err != nil || want.Hash != out.Desired.Items[unit].Hash {
				t.Errorf("%s: unit item not recomputed: %v", name, err)
			}
		}
	})
	t.Run("content from a third folder", func(t *testing.T) {
		third := scrSnap(t, scrWS, tree{".git/HEAD": "third", "f": "x"}, 500)
		out, _, _ := scrMerge(t, scrCase{s: g0, sBase: g0, t: g0, tBase: g0, sValid: true, tValid: true, canon: third})
		if members(out) != sideMembers(t, g0) || !hasScrNote(out, unit, scratch.NoteBytes) {
			t.Errorf("members %s notes %+v", members(out), out.Changes)
		}
	})
}

func unitKeyFile(rel string) string { return fileKey(rel) }

func TestScratchWorkspaceRules(t *testing.T) {
	t.Run("absent target workspace recreated whole", func(t *testing.T) {
		out, _, _ := scrMerge(t, scrCase{s: tree{"a": "1", "b": "2"}, sBase: tree{"a": "1", "b": "2"}, t: nil, tBase: tree{"a": "1", "b": "2", "c": "3"}, sValid: true, tValid: true})
		for _, rel := range []string{"a", "b"} {
			if _, ok := desiredContent(t, out, rel); !ok {
				t.Errorf("%s missing", rel)
			}
		}
		for _, c := range out.Changes {
			if c.Kind == model.ChangeDeleted {
				t.Errorf("deletion %+v", c)
			}
		}
		if _, ok := out.Canonical.Tombstones[fileKey("c")]; ok || hasTomb(out.Canonical, fileKey("a")) {
			t.Error("a missing workspace recorded removals")
		}
	})
	t.Run("special workspace passes through", func(t *testing.T) {
		in := scrCase{s: tree{"a": "1"}, sValid: true, sBase: tree{"a": "0"}, t: tree{"a": "t"}, tBase: tree{"a": "t"}, tValid: true}
		sSnap := scrSnap(t, scrWS, in.s, 1000)
		over, _ := scratch.RootItem(scrWS, 0o755, scratch.OverFiles)
		sSnap.Items = map[string]model.Item{over.Key: over}
		out := mergeWith(t, sSnap, scrSnap(t, scrWS, in.t, 1000), in)
		if got, _ := desiredContent(t, out, "a"); got != h("t") || !hasScrNote(out, scrWS, scratch.NoteOversizeFiles) {
			t.Errorf("special workspace: %s %+v", got, out.Changes)
		}
	})
	t.Run("exclusion change never deletes", func(t *testing.T) {
		out, _, _ := scrMerge(t, scrCase{s: tree{"a": "1"}, sBase: tree{"a": "1", "x/.DS_Store": "d"}, t: tree{"a": "1", "x/.DS_Store": "d"},
			tBase: tree{"a": "1", "x/.DS_Store": "d"}, sValid: true, tValid: true})
		for _, c := range out.Changes {
			if c.Kind == model.ChangeDeleted {
				t.Errorf("deletion %+v", c)
			}
		}
	})
	t.Run("an excluded file over a folder never deletes the folder", func(t *testing.T) {
		exclA := func(p *ScratchPolicy) { p.Exclude = append(p.Exclude, "a") }
		// S replaced folder a by a file the rules now drop: T keeps a/x.
		out, _, _ := scrMerge(t, scrCase{s: tree{"b": "2", "a": "f"}, sBase: tree{"b": "2", "a/x": "1"},
			t: tree{"b": "2", "a/x": "1"}, tBase: tree{"b": "2", "a/x": "1"}, sValid: true, tValid: true, pol: exclA})
		if _, ok := desiredContent(t, out, "a/x"); !ok {
			t.Errorf("a/x dropped: %v", out.Desired.Keys())
		}
		for _, c := range out.Changes {
			if c.Kind == model.ChangeDeleted {
				t.Errorf("deletion %+v", c)
			}
		}
		// T holds such a file where S has a folder: T's file form stays,
		// nothing is written below it.
		out, _, _ = scrMerge(t, scrCase{s: tree{"b": "2", "a/x": "1"}, sBase: tree{"b": "2", "a/x": "1"},
			t: tree{"b": "2", "a": "f"}, tBase: tree{"b": "2", "a/x": "1"}, sValid: true, tValid: true, pol: exclA})
		if hasPrefixKey(out.Desired.Items, fileKey("a")) || !hasScrNote(out, fileKey("a"), scratch.NoteTypeConflict) {
			t.Errorf("wrote below T's excluded file: %v %+v", out.Desired.Keys(), out.Changes)
		}
	})
	t.Run("dirs and links create-only", func(t *testing.T) {
		out, _, _ := scrMerge(t, scrCase{s: tree{"a": "1", "ln": "->a"}, sBase: tree{"a": "1", "d/": "", "ln": "->b"},
			t: tree{"a": "1", "d/": "", "ln": "->b"}, tBase: tree{"a": "1", "d/": "", "ln": "->b"}, sValid: true, tValid: true})
		if _, ok := out.Desired.Items[scratch.DirKey(scrWS, "d")]; !ok {
			t.Error("a directory was removed")
		}
		v, _ := scratch.Decode(out.Desired.Items[fileKey("ln")])
		if v.Target != "b" || !hasScrNote(out, fileKey("ln"), scratch.NoteLinkKept) {
			t.Errorf("link %+v", v)
		}
		out2, _, _ := scrMerge(t, scrCase{s: tree{"esc": "->../../x", "abs": "->/etc/x"}, t: tree{}, tValid: false})
		for _, k := range []string{"esc", "abs"} {
			if _, ok := out2.Desired.Items[fileKey(k)]; ok {
				t.Errorf("escaping link %s carried", k)
			}
		}
	})
	t.Run("type conflict keeps T's form", func(t *testing.T) {
		out, _, _ := scrMerge(t, scrCase{s: tree{"p/q": "1"}, sBase: tree{"p": "f"}, t: tree{"p": "f"}, tBase: tree{"p": "f"}, sValid: true, tValid: true})
		if _, ok := desiredContent(t, out, "p"); !ok || !hasScrNote(out, fileKey("p"), scratch.NoteTypeConflict) {
			t.Errorf("type conflict: %v %+v", out.Desired.Keys(), out.Changes)
		}
		if _, ok := desiredContent(t, out, "p/q"); ok {
			t.Error("S's subtree was kept next to T's file")
		}
	})
	t.Run("deletions off", func(t *testing.T) {
		out, _, _ := scrMerge(t, scrCase{s: tree{"a": "1"}, sBase: tree{"a": "1", "b": "2", ".git/HEAD": "h"}, t: tree{"a": "1", "b": "2", ".git/HEAD": "h"},
			tBase: tree{"a": "1", "b": "2", ".git/HEAD": "h"}, sValid: true, tValid: true,
			pol: func(p *ScratchPolicy) { p.PropagateDeletions = false }})
		if _, ok := desiredContent(t, out, "b"); !ok || !hasScrNote(out, scrWS, scratch.NoteDeletionOff) {
			t.Errorf("deletions off: %v", out.Desired.Keys())
		}
		if _, ok := desiredContent(t, out, ".git/HEAD"); !ok {
			t.Error("a unit removal was carried with deletions off")
		}
	})
	t.Run("realisable only", func(t *testing.T) {
		third := scrSnap(t, scrWS, tree{"a": "third"}, 500)
		out, _, _ := scrMerge(t, scrCase{s: tree{"a": "1"}, sBase: tree{"a": "1"}, t: tree{"a": "1"}, tBase: tree{"a": "1"}, sValid: true, tValid: true, canon: third})
		if got, _ := desiredContent(t, out, "a"); got != h("1") || !hasScrNote(out, fileKey("a"), scratch.NoteBytes) {
			t.Errorf("realisable: %s %+v", got, out.Changes)
		}
		if got := out.Canonical.Items[fileKey("a")]; scrSHA(got) != h("third") {
			t.Error("the winner left canonical")
		}
	})
	t.Run("canonical pruned to referenced workspaces", func(t *testing.T) {
		canon := scrSnap(t, scrWS, tree{"a": "1"}, 500)
		other := scrSnap(t, scrWS2, tree{"z": "9"}, 500)
		for k, it := range other.Items {
			canon.Items[k] = it
		}
		canon.Tombstones = map[string]model.Tombstone{scratch.EntryKey(scrWS2, "gone"): {Key: scratch.EntryKey(scrWS2, "gone")}}
		out, _, _ := scrMerge(t, scrCase{s: tree{"a": "1"}, sBase: tree{"a": "1"}, t: tree{"a": "1"}, tBase: tree{"a": "1"}, sValid: true, tValid: true, canon: canon})
		for k := range out.Canonical.Items {
			if scratch.WorkspaceOf(k) == scrWS2 {
				t.Errorf("unreferenced %s kept", k)
			}
		}
		if len(out.Canonical.Tombstones) != 0 {
			t.Error("tombstones of an unreferenced workspace kept")
		}
		// Referenced but not carried: passes through.
		out2, _, _ := scrMerge(t, scrCase{s: tree{"a": "1"}, sBase: tree{"a": "1"}, t: tree{"a": "1"}, tBase: tree{"a": "1"}, sValid: true, tValid: true, canon: canon,
			pol: func(p *ScratchPolicy) { p.Referenced = []string{scrWS, scrWS2} }})
		if _, ok := out2.Canonical.Items[scratch.EntryKey(scrWS2, "z")]; !ok {
			t.Error("a referenced workspace outside the carry set lost its canonical items")
		}
		for k := range out2.Desired.Items {
			if scratch.WorkspaceOf(k) == scrWS2 {
				t.Error("Desired holds a workspace outside the carry set")
			}
		}
	})
}

func scrSHA(it model.Item) string {
	v, _ := scratch.Decode(it)
	return v.SHA256
}

func hasTomb(s *model.RowSnapshot, k string) bool {
	_, ok := s.Tombstones[k]
	return ok
}

// mergeWith merges explicit snapshots with the case's bases and policy.
func mergeWith(t *testing.T, sSnap, tSnap *model.RowSnapshot, c scrCase) Output {
	t.Helper()
	in := Input{
		Row:      model.RowScratch,
		Leaving:  Side{Alias: aliasA, Snapshot: sSnap, BaseValid: c.sValid, LiveEpoch: 7},
		Entering: Side{Alias: aliasB, Snapshot: tSnap, BaseValid: c.tValid, LiveEpoch: 3},
		Policy:   openPolicy(),
	}
	if c.sValid {
		in.Leaving.Base = scrSnap(t, scrWS, c.sBase, 500)
		in.Canonical = scrSnap(t, scrWS, c.sBase, 500)
	}
	if c.tValid {
		in.Entering.Base = scrSnap(t, scrWS, c.tBase, 500)
	}
	in.Policy.Scratch = &ScratchPolicy{PropagateDeletions: true, Leaf: scrLeaf, ConflictDate: scrDate, Refs: []string{scrWS}, Referenced: []string{scrWS}}
	out, err := scratchModule{}.Merge(in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestScratchPropertyFindings pins the cases the property tests found
// (spec-scratch 14.2): each one failed before its fix.
func TestScratchPropertyFindings(t *testing.T) {
	t.Run("a canonical value does not hide a conflict of both folders", func(t *testing.T) {
		// S's base is invalid; T is unchanged; canonical (a third folder)
		// holds T's version. Both folders hold f with different content:
		// the loser is kept as a sibling (7.6, "either base invalid").
		base := tree{"f": "v0"}
		out, _, _ := scrMerge(t, scrCase{s: tree{"f": "vs"}, t: base, tBase: base, tValid: true,
			canon: scrSnap(t, scrWS, base, 500)})
		if got, _ := desiredContent(t, out, "f"); got != h("vs") {
			t.Errorf("f = %s", got)
		}
		sib := "f (conflict from " + string(aliasB) + " " + scrDate + ")"
		if got, _ := desiredContent(t, out, sib); got != h("v0") {
			t.Errorf("T's version is not kept as %q: %v", sib, out.Desired.Keys())
		}
		if len(out.Conflicts) != 1 || out.Conflicts[0].Loser != aliasB {
			t.Errorf("conflicts %+v", out.Conflicts)
		}
	})
	t.Run("a canonical value is not a git conflict", func(t *testing.T) {
		// T lacks the unit; canonical holds a third folder's unit: S's
		// unit is created whole, not kept apart as diverged.
		out, _, _ := scrMerge(t, scrCase{s: tree{".git/HEAD": "1"}, t: tree{}, canon: scrSnap(t, scrWS, tree{".git/l": "->HEAD"}, 500)})
		if got, _ := desiredContent(t, out, ".git/HEAD"); got != h("1") {
			t.Errorf("S's unit was not created: %v", out.Desired.Keys())
		}
		unit := scratch.DirKey(scrWS, ".git")
		if hasScrNote(out, unit, scratch.NoteGitConflict) || scratch.IsDiverged(out.Canonical.Items[unit]) {
			t.Error("the unit was marked diverged")
		}
	})
	t.Run("a link never replaces T's file", func(t *testing.T) {
		base := tree{"a": "1"}
		for _, valid := range []bool{true, false} {
			out, _, _ := scrMerge(t, scrCase{s: tree{"a": "->b"}, sBase: base, t: base, tBase: base, sValid: valid, tValid: true})
			if got, ok := desiredContent(t, out, "a"); !ok || got != h("1") {
				t.Errorf("valid %v: T's file was replaced: %v", valid, out.Desired.Items[fileKey("a")])
			}
			if !hasScrNote(out, fileKey("a"), scratch.NoteTypeConflict) || changeOf(out, fileKey("a")) != "" {
				t.Errorf("valid %v: changes %+v", valid, out.Changes)
			}
		}
	})
	heldE := func(p *ScratchPolicy) { p.HeldBack = append(p.HeldBack, "e") }
	t.Run("a file replaced by a held-back folder on S is not deleted", func(t *testing.T) {
		base := tree{"e": "1", "k": "k"}
		out, _, _ := scrMerge(t, scrCase{s: tree{"e/h": "1", "k": "k"}, sBase: base, t: base, tBase: base, sValid: true, tValid: true, pol: heldE})
		if changeOf(out, fileKey("e")) != "" {
			t.Errorf("changes %+v", out.Changes)
		}
		if _, ok := desiredContent(t, out, "e"); !ok {
			t.Error("T's file left Desired")
		}
		// The same through the read's held-back note (ReadRefs never
		// lists the folder).
		sSnap := scrSnap(t, scrWS, tree{"k": "k"}, 1000)
		sSnap.Notes = []model.Change{{Row: model.RowScratch, Kind: model.ChangeSkipped, Key: scratch.DirKey(scrWS, "e"), Note: scratch.NoteHeldBack}}
		c := scrCase{sBase: base, tBase: base, sValid: true, tValid: true}
		out = mergeWithPolicy(t, sSnap, scrSnap(t, scrWS, base, 1000), c, heldE)
		if changeOf(out, fileKey("e")) != "" {
			t.Errorf("with the note: changes %+v", out.Changes)
		}
	})
	t.Run("a file replaced by a held-back folder on T stays out of Desired", func(t *testing.T) {
		base := tree{"e": "1", "k": "k"}
		out, _, _ := scrMerge(t, scrCase{s: base, sBase: base, t: tree{"e/h": "1", "k": "k"}, tBase: base, sValid: true, tValid: true, pol: heldE})
		if _, ok := desiredContent(t, out, "e"); ok {
			t.Error("Desired writes a file where T holds a held-back folder")
		}
		if changeOf(out, fileKey("e")) != "" {
			t.Errorf("changes %+v", out.Changes)
		}
		if _, ok := out.Canonical.Items[fileKey("e")]; !ok {
			t.Error("canonical lost the file S still holds")
		}
	})
}

// mergeWithPolicy is mergeWith with the policy of scrMerge adjusted by pol.
func mergeWithPolicy(t *testing.T, sSnap, tSnap *model.RowSnapshot, c scrCase, pol func(*ScratchPolicy)) Output {
	t.Helper()
	in := Input{
		Row:      model.RowScratch,
		Leaving:  Side{Alias: aliasA, Snapshot: sSnap, BaseValid: c.sValid, LiveEpoch: 7},
		Entering: Side{Alias: aliasB, Snapshot: tSnap, BaseValid: c.tValid, LiveEpoch: 3},
		Policy:   openPolicy(),
	}
	if c.sValid {
		in.Leaving.Base = scrSnap(t, scrWS, c.sBase, 500)
		in.Canonical = scrSnap(t, scrWS, c.sBase, 500)
	}
	if c.tValid {
		in.Entering.Base = scrSnap(t, scrWS, c.tBase, 500)
	}
	in.Policy.Scratch = &ScratchPolicy{PropagateDeletions: true, Leaf: scrLeaf, ConflictDate: scrDate,
		Refs: []string{scrWS}, Referenced: []string{scrWS}, HeldBack: []string{"node_modules"}}
	pol(in.Policy.Scratch)
	out, err := scratchModule{}.Merge(in)
	if err != nil {
		t.Fatal(err)
	}
	checkScratchInvariants(t, out, in.Policy.Scratch.Refs)
	return out
}
