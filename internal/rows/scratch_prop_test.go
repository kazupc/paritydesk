package rows

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
)

// The scratch property tests (spec-scratch 14.2) draw a common base
// workspace tree over a small path alphabet (including a .git unit and a
// held-back folder), then each folder's edits of it: add, modify,
// remove, chmod, mkdir, symlink, file<->directory swaps and git changes,
// with random mtimes. Bases are valid or invalid, canonical is the
// leaving base or a random third folder's tree, and per-key skips are
// folded into Desired the way the orchestrator does.

// pent is one entry of a generated workspace tree.
type pent struct {
	kind  string // pFile, pDir or pLink
	val   string // a file's content, a link's target
	mode  uint32 // a file's mode
	mtime int64  // a file's or link's mtime (ms)
}

const (
	pFile = "file"
	pDir  = "dir"
	pLink = "link"
)

// ptree is a workspace: rel -> entry. Directories are explicit entries or
// implied by what is below them. A nil tree is a folder without the
// workspace.
type ptree map[string]pent

var (
	propFiles   = []string{"a", "a/x", "b", "d/c", "d/e", "d/f/g", "e/h", ".git/HEAD", ".git/o/1", ".git/o/2", "node_modules/x"}
	propDirs    = []string{"a", "d", "d/f", "e", "k", ".git/o"}
	propLinks   = []string{"l", "a", "d/l", ".git/l"}
	propTargets = []string{"a", "d/c", "../x", "../../../../../y", "/abs"}
	propVals    = []string{"1", "2", "3", "4"}
	propModes   = []uint32{0o600, 0o644, 0o755}
	propAll     = append(append(append([]string(nil), propFiles...), propDirs...), propLinks...)
)

func (tr ptree) clone() ptree {
	out := make(ptree, len(tr))
	for k, v := range tr {
		out[k] = v
	}
	return out
}

// put sets rel to en and keeps the tree well formed: an ancestor that is
// a file or link is removed (it becomes a directory), and a file or link
// replaces everything below rel.
func (tr ptree) put(rel string, en pent) {
	for d := path.Dir(rel); d != "."; d = path.Dir(d) {
		if a, ok := tr[d]; ok && a.kind != pDir {
			delete(tr, d)
		}
	}
	if en.kind != pDir {
		tr.removeBelow(rel)
	}
	tr[rel] = en
}

func (tr ptree) removeBelow(rel string) {
	for k := range tr {
		if strings.HasPrefix(k, rel+"/") {
			delete(tr, k)
		}
	}
}

func (tr ptree) remove(rel string) {
	delete(tr, rel)
	tr.removeBelow(rel)
}

func (tr ptree) keysOf(kind string) []string {
	var out []string
	for k, en := range tr {
		if en.kind == kind {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// propEdits applies up to maxN random edits to a copy of base.
func propEdits(t *rapid.T, base ptree, label string, maxN int) ptree {
	out := base.clone()
	n := rapid.IntRange(0, maxN).Draw(t, label+"-edits")
	file := func() pent {
		return pent{kind: pFile, val: rapid.SampledFrom(propVals).Draw(t, label+"-val"),
			mode: 0o644, mtime: rapid.Int64Range(1, 9).Draw(t, label+"-mtime")}
	}
	for i := 0; i < n; i++ {
		switch rapid.IntRange(0, 6).Draw(t, label+"-op") {
		case 0: // add or modify a file
			out.put(rapid.SampledFrom(propFiles).Draw(t, label+"-file"), file())
		case 1: // remove
			out.remove(rapid.SampledFrom(propAll).Draw(t, label+"-rm"))
		case 2: // chmod (the mtime stays)
			if files := out.keysOf(pFile); len(files) > 0 {
				p := rapid.SampledFrom(files).Draw(t, label+"-chmod")
				en := out[p]
				en.mode = rapid.SampledFrom(propModes).Draw(t, label+"-mode")
				out[p] = en
			}
		case 3: // mkdir
			out.put(rapid.SampledFrom(propDirs).Draw(t, label+"-mkdir"), pent{kind: pDir})
		case 4: // symlink
			out.put(rapid.SampledFrom(propLinks).Draw(t, label+"-link"), pent{kind: pLink,
				val: rapid.SampledFrom(propTargets).Draw(t, label+"-target"), mtime: rapid.Int64Range(1, 9).Draw(t, label+"-lmtime")})
		case 5: // file <-> directory swap
			p := rapid.SampledFrom([]string{"a", "d", "e"}).Draw(t, label+"-swap")
			if en, ok := out[p]; ok && en.kind == pFile {
				out.put(p+"/x", file())
			} else {
				out.put(p, file())
			}
		case 6: // a git change: a commit writes or prunes objects
			p := rapid.SampledFrom([]string{".git/HEAD", ".git/o/1", ".git/o/2", ".git/o/3"}).Draw(t, label+"-git")
			if rapid.Bool().Draw(t, label+"-gitrm") {
				out.remove(p)
			} else {
				out.put(p, file())
			}
		}
	}
	return out
}

// fataler is what the snapshot builder needs of a test.
type fataler interface {
	Fatalf(format string, args ...any)
}

// propSnap builds a ReadRefs-like snapshot of ws from a tree: a root
// marker, directories, files, links, and a git unit item over its
// members.
func propSnap(t fataler, ws string, tr ptree) *model.RowSnapshot {
	s := emptySnapshot(model.RowScratch)
	s.Tombstones = nil
	if tr == nil {
		return s
	}
	add := func(it model.Item, err error) {
		if err != nil {
			t.Fatalf("%v", err)
		}
		s.Items[it.Key] = it
	}
	add(scratch.RootItem(ws, 0o755, ""))
	dirs := map[string]bool{}
	for rel, en := range tr {
		for d := path.Dir(rel); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
		switch en.kind {
		case pDir:
			dirs[rel] = true
		case pLink:
			add(scratch.LinkItem(scratch.EntryKey(ws, rel), en.val, en.mtime))
		default:
			add(scratch.FileItem(scratch.EntryKey(ws, rel), model.HashBytes([]byte(en.val)), int64(len(en.val)), en.mode, en.mtime))
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

// propCase is one merge of workspace scrWS: S leaving (alias a), T
// entering (alias b).
type propCase struct {
	s, t, sBase, tBase ptree
	third              ptree // a third folder's tree as canonical; nil: S's base when valid
	sValid, tValid     bool
}

func drawCase(rt *rapid.T) propCase {
	base := propEdits(rt, ptree{}, "base", 8)
	c := propCase{
		s: propEdits(rt, base, "s", 4), t: propEdits(rt, base, "t", 4), sBase: base, tBase: base,
		sValid: rapid.Bool().Draw(rt, "sValid"), tValid: rapid.Bool().Draw(rt, "tValid"),
	}
	if rapid.Bool().Draw(rt, "thirdCanonical") {
		c.third = propEdits(rt, base, "c", 3)
	}
	return c
}

func propPolicy() *ScratchPolicy {
	return &ScratchPolicy{PropagateDeletions: true, Leaf: scrLeaf, ConflictDate: scrDate,
		Refs: []string{scrWS}, Referenced: []string{scrWS},
		Exclude: []string{".DS_Store", "**/.DS_Store", "**/.git/*.lock"}, HeldBack: []string{"node_modules"},
		ReservedGlobs: []string{"local_*.json.tmp"}}
}

// propInput is the module input of a case.
func propInput(rt *rapid.T, c propCase, pol *ScratchPolicy) Input {
	in := Input{
		Row:      model.RowScratch,
		Leaving:  Side{Alias: aliasA, Snapshot: propSnap(rt, scrWS, c.s), BaseValid: c.sValid, LiveEpoch: 7},
		Entering: Side{Alias: aliasB, Snapshot: propSnap(rt, scrWS, c.t), BaseValid: c.tValid, LiveEpoch: 3},
		Policy:   openPolicy(),
	}
	if c.sValid {
		in.Leaving.Base = propSnap(rt, scrWS, c.sBase)
		in.Canonical = propSnap(rt, scrWS, c.sBase)
	}
	if c.tValid {
		in.Entering.Base = propSnap(rt, scrWS, c.tBase)
	}
	if c.third != nil {
		in.Canonical = propSnap(rt, scrWS, c.third)
	}
	in.Policy.Scratch = pol
	return in
}

func mergeProp(rt fataler, in Input) Output {
	out, err := scratchModule{}.Merge(in)
	if err != nil {
		rt.Fatalf("merge: %v", err)
	}
	return out
}

// snapDigest is a stable digest of a snapshot's items, to see that the
// module never changes its inputs.
func snapDigest(s *model.RowSnapshot) string {
	var ks []string
	for k, it := range s.Items {
		ks = append(ks, k+"="+it.Hash+fmt.Sprint(it.Stamp.AppTime))
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

func fileContents(s *model.RowSnapshot) map[string]bool {
	out := map[string]bool{}
	for _, it := range s.Items {
		if v, ok := scratch.Decode(it); ok && v.K == scratch.KindFile {
			out[v.SHA256] = true
		}
	}
	return out
}

func unitFiles(items map[string]model.Item, unit string) string {
	var ks []string
	for k, it := range items {
		if u, ok := scratch.GitUnit(k); ok && u == unit && scratch.KindOf(it) == scratch.KindFile {
			ks = append(ks, k+"="+it.Hash)
		}
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

func deletedKeys(out Output) map[string]bool {
	del := map[string]bool{}
	for _, ch := range out.Changes {
		if ch.Kind == model.ChangeDeleted {
			del[ch.Key] = true
		}
	}
	return del
}

func hasNote(out Output, key, note string) bool {
	for _, ch := range out.Changes {
		if ch.Kind == model.ChangeSkipped && ch.Key == key && ch.Note == note {
			return true
		}
	}
	return false
}

// checkWellFormed is property 5: never both x and x/…, every Desired file
// content is in S or T, keys are clean and inside the carry set, no
// tombstones.
func checkWellFormed(rt *rapid.T, out Output, sSnap, tSnap *model.RowSnapshot) {
	have := fileContents(sSnap)
	for k := range fileContents(tSnap) {
		have[k] = true
	}
	for k, it := range out.Desired.Items {
		ws, rel, _, err := scratch.SplitKey(k)
		if err != nil || ws != scrWS {
			rt.Fatalf("Desired key %q outside the carry set (%v)", k, err)
		}
		if rel != "" && !plan.CleanRel(rel) {
			rt.Fatalf("Desired key %q is not clean", k)
		}
		v, _ := scratch.Decode(it)
		if (v.K == scratch.KindFile || v.K == scratch.KindLink) && hasPrefixKey(out.Desired.Items, k) {
			rt.Fatalf("Desired holds both %q and keys below it", k)
		}
		if v.K == scratch.KindFile && !have[v.SHA256] {
			rt.Fatalf("Desired %q names content neither side holds", k)
		}
	}
	if len(out.Desired.Tombstones) > 0 {
		rt.Fatal("Desired carries tombstones")
	}
}

// TestPropScratchMerge checks properties 1, 2, 5 and 7 (and that held-back
// folders are never touched) on random merges.
func TestPropScratchMerge(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		c := drawCase(rt)
		in := propInput(rt, c, propPolicy())
		sSnap, tSnap := in.Leaving.Snapshot, in.Entering.Snapshot
		sBefore := snapDigest(sSnap)
		out := mergeProp(rt, in)
		desired := fileContents(out.Desired)
		del := deletedKeys(out)
		tBase := map[string]model.Item{}
		if c.tValid {
			tBase = in.Entering.Base.Items
		}
		// 1. No loss, T's side: every T content is in Desired (same or
		// sibling key), quarantined, or (git units) kept in T.
		for k, it := range tSnap.Items {
			v, _ := scratch.Decode(it)
			if v.K != scratch.KindFile || desired[v.SHA256] || strings.Contains(k, "/node_modules/") {
				continue // held-back folders are never read, so never removed
			}
			if del[k] {
				continue // quarantined
			}
			// A normal update: T had not changed the file since its valid
			// base, so its old bytes are the base's (and the generation's).
			if b, ok := tBase[k]; c.sValid && c.tValid && ok && b.Hash == it.Hash {
				continue
			}
			rt.Fatalf("T's %s lost without a quarantine", k)
		}
		// 1. No loss, S's side: S is never changed, and the content of
		// every conflict's loser (either side) is in Desired, at a
		// sibling key or (equal content) the same key, unless no free
		// name was found. A canonical value an edit replaced (no loser
		// side) is a third folder's, kept in the conflict list only.
		if snapDigest(sSnap) != sBefore {
			rt.Fatal("the merge changed S's snapshot")
		}
		for _, cf := range out.Conflicts {
			if _, member := scratch.GitUnit(cf.Key); member || cf.Loser == "" || cf.Reason == reasonCanonical {
				continue
			}
			var loser model.Item
			found := false
			for _, side := range []*model.RowSnapshot{sSnap, tSnap} {
				if it, ok := side.Items[cf.Key]; ok && it.Hash == cf.LoserHash {
					loser, found = it, true
				}
			}
			if !found {
				rt.Fatalf("the loser of %+v is on neither side", cf)
			}
			if scratch.KindOf(loser) != scratch.KindFile || hasNote(out, cf.Key, scratch.NoteConflictName) {
				continue
			}
			if lv, _ := scratch.Decode(loser); !desired[lv.SHA256] {
				rt.Fatalf("the loser of %+v is not in Desired", cf)
			}
		}
		// 2. No deletion with an invalid base.
		if !c.sValid || !c.tValid {
			if len(del) > 0 {
				rt.Fatalf("deletion %v under an invalid base", del)
			}
		}
		// 5. Well-formed Desired.
		checkWellFormed(rt, out, sSnap, tSnap)
		// 7. Git units are never mixed.
		unit := scratch.DirKey(scrWS, ".git")
		if _, ok := out.Desired.Items[unit]; ok {
			d := unitFiles(out.Desired.Items, unit)
			if d != unitFiles(sSnap.Items, unit) && d != unitFiles(tSnap.Items, unit) {
				rt.Fatalf("unit mixed: %s", d)
			}
		}
		// Held-back folders are never touched.
		if _, ok := out.Desired.Items[fileKey("node_modules/x")]; ok {
			rt.Fatal("a held-back folder was carried")
		}
		for _, ch := range out.Changes {
			if strings.Contains(ch.Key, "node_modules") && ch.Kind != model.ChangeSkipped {
				rt.Fatalf("change in a held-back folder: %+v", ch)
			}
		}
	})
}

// TestPropScratchIdempotent (property 3): S = T = Desired with bases =
// Desired gives no change.
func TestPropScratchIdempotent(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		tr := propEdits(rt, ptree{}, "x", 10)
		in := propInput(rt, propCase{s: tr, sBase: tr, t: tr, tBase: tr, sValid: true, tValid: true}, propPolicy())
		out := mergeProp(rt, in)
		for _, ch := range out.Changes {
			if ch.Kind != model.ChangeSkipped {
				rt.Fatalf("change %+v", ch)
			}
		}
	})
}

// TestPropScratchExclusionMonotone (property 6): adding an exclude glob or
// a held-back name never adds a ChangeDeleted ("a change of these rules
// never looks like a deletion", 7.2 step 1). The one deletion a rule may
// add is a removal the leaving folder really made, with nothing left on
// that path (at, below or above it), which without the rule a third
// folder's now excluded entry had held back through a type conflict.
func TestPropScratchExclusionMonotone(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		c := drawCase(rt)
		before := deletedKeys(mergeProp(rt, propInput(rt, c, propPolicy())))
		pol := propPolicy()
		if rapid.Bool().Draw(rt, "glob") {
			pol.Exclude = append(pol.Exclude, rapid.SampledFrom([]string{"d/**", "a", "**/c", "d/*", "*/x", "b", "e/**", "l"}).Draw(rt, "exclude"))
		} else {
			pol.HeldBack = append(pol.HeldBack, rapid.SampledFrom([]string{"d", "a", "e", "f", "k"}).Draw(rt, "heldBack"))
		}
		in := propInput(rt, c, pol)
		after := mergeProp(rt, in)
		for k := range deletedKeys(after) {
			if before[k] {
				continue
			}
			_, rel, _, _ := scratch.SplitKey(k)
			held := false
			for p := range c.s {
				held = held || p == rel || strings.HasPrefix(p, rel+"/") || strings.HasPrefix(rel, p+"/")
			}
			if held || c.third == nil {
				rt.Fatalf("exclude %v / held back %v made %s look deleted", pol.Exclude, pol.HeldBack, k)
			}
		}
	})
}

// foldSkips folds random per-key skips into a copy of Desired the way the
// orchestrator does (Desired only): a skipped file key takes T's item, or
// leaves Desired when T lacks it. It returns the new Desired and the
// skipped keys.
func foldSkips(rt *rapid.T, out Output, tSnap *model.RowSnapshot) (*model.RowSnapshot, []string) {
	d := out.Desired.Clone()
	var cand []string
	for _, k := range unionKeys(out.Desired.Items, tSnap.Items) {
		if _, member := scratch.GitUnit(k); member {
			continue // units are atomic
		}
		di, dOK := out.Desired.Items[k]
		ti, tOK := tSnap.Items[k]
		if dOK && scratch.KindOf(di) == scratch.KindFile && (!tOK || ti.Hash != di.Hash) ||
			!dOK && tOK && scratch.KindOf(ti) == scratch.KindFile {
			cand = append(cand, k)
		}
	}
	var skipped []string
	for _, k := range cand {
		if !rapid.Bool().Draw(rt, "skip") {
			continue
		}
		skipped = append(skipped, k)
		if ti, ok := tSnap.Items[k]; ok {
			d.Items[k] = ti
		} else {
			delete(d.Items, k)
		}
	}
	return d, skipped
}

// TestPropScratchSkipsNeverRevert (property 8): after per-key skips (Desired
// only), a reverse switch with no edits never writes back, at a skipped
// key, anything but the first merge's decision: the content T kept by the
// skip never wins over it. When that decision was taken by mtime (a
// conflict of both folders' versions while a base was invalid), what the
// reverse switch writes is never older than the newest version either
// side held for the key. (With valid bases the later epoch decides, and a
// key one side does not hold takes canonical, so a third folder's older
// mtime may win there without any skip.)
func TestPropScratchSkipsNeverRevert(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		c := drawCase(rt)
		in := propInput(rt, c, propPolicy())
		sSnap, tSnap := in.Leaving.Snapshot, in.Entering.Snapshot
		out := mergeProp(rt, in)
		tAfter, skipped := foldSkips(rt, out, tSnap)
		// T now holds the folded Desired, its base the re-read; S's base
		// is its harvest. Reverse switch: T leaves, S enters.
		rev := Input{
			Row:       model.RowScratch,
			Leaving:   Side{Alias: aliasB, Snapshot: tAfter.Clone(), Base: tAfter.Clone(), BaseValid: true, LiveEpoch: 8},
			Entering:  Side{Alias: aliasA, Snapshot: sSnap.Clone(), Base: sSnap.Clone(), BaseValid: true, LiveEpoch: 7},
			Canonical: out.Canonical, Policy: openPolicy(),
		}
		rev.Policy.Scratch = propPolicy()
		back := mergeProp(rt, rev)
		for _, k := range skipped {
			it, ok := back.Desired.Items[k]
			if !ok || scratch.KindOf(it) != scratch.KindFile {
				continue
			}
			if cur, has := sSnap.Items[k]; has && cur.Hash == it.Hash {
				continue // nothing written
			}
			decided, had := out.Desired.Items[k]
			if !had || decided.Hash != it.Hash {
				rt.Fatalf("skipped %s: the reverse switch writes %s, not the first merge's decision", k, it.Value)
			}
			byMtime := false
			for _, cf := range out.Conflicts {
				byMtime = byMtime || cf.Key == k && twoSided(cf)
			}
			if c.sValid && c.tValid || !byMtime {
				continue // decided by epoch or taken from canonical
			}
			newest := int64(-1)
			for _, side := range []*model.RowSnapshot{sSnap, tSnap} {
				if v, ok := side.Items[k]; ok && scratch.KindOf(v) == scratch.KindFile {
					newest = max(newest, v.Stamp.AppTime)
				}
			}
			if it.Stamp.AppTime < newest {
				rt.Fatalf("skipped %s: the reverse switch writes mtime %d over %d", k, it.Stamp.AppTime, newest)
			}
		}
	})
}

// scrFolder is one folder of the convergence simulation.
type scrFolder struct {
	cur, base *model.RowSnapshot
	valid     bool
	live      uint64
}

// switchOnce merges leaving l into entering e the way the orchestrator
// does: e's content becomes Desired, both bases advance.
func switchOnce(rt fataler, l, e *scrFolder, canon **model.RowSnapshot, epoch *uint64, ll, el string) Output {
	in := Input{
		Row:       model.RowScratch,
		Leaving:   Side{Alias: aliasOf(ll), Snapshot: l.cur.Clone(), Base: l.base, BaseValid: l.valid, LiveEpoch: l.live},
		Entering:  Side{Alias: aliasOf(el), Snapshot: e.cur.Clone(), Base: e.base, BaseValid: e.valid, LiveEpoch: e.live},
		Canonical: *canon, Policy: openPolicy(),
	}
	in.Policy.Scratch = propPolicy()
	out := mergeProp(rt, in)
	*canon = out.Canonical
	l.base, l.valid = l.cur.Clone(), true
	e.cur = out.Desired.Clone()
	e.base, e.valid = out.Desired.Clone(), true
	*epoch++
	l.live = *epoch - 1
	e.live = *epoch
	return out
}

// TestPropScratchConvergence: S→T, T→S, S→T with no edits in between;
// the third plan is empty (spec-scratch 14.2 property 4), and canonical
// never holds an unreferenced workspace (property 9).
func TestPropScratchConvergence(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		c := drawCase(rt)
		s := &scrFolder{cur: propSnap(rt, scrWS, c.s), base: propSnap(rt, scrWS, c.sBase), valid: c.sValid, live: 2}
		tf := &scrFolder{cur: propSnap(rt, scrWS, c.t), base: propSnap(rt, scrWS, c.tBase), valid: c.tValid, live: 1}
		var canon *model.RowSnapshot
		switch {
		case c.third != nil:
			canon = propSnap(rt, scrWS, c.third)
		case s.valid:
			canon = propSnap(rt, scrWS, c.sBase)
		}
		// An unreferenced workspace in canonical is pruned.
		if canon != nil {
			it, err := scratch.FileItem(scratch.EntryKey(scrWS2, "old"), model.HashBytes([]byte("x")), 1, 0o644, 1)
			if err != nil {
				rt.Fatal(err)
			}
			canon.Items[it.Key] = it
		}
		epoch := uint64(2)
		switchOnce(rt, s, tf, &canon, &epoch, "a", "b")
		switchOnce(rt, tf, s, &canon, &epoch, "b", "a")
		third := switchOnce(rt, s, tf, &canon, &epoch, "a", "b")
		for _, ch := range third.Changes {
			if ch.Kind != model.ChangeSkipped {
				rt.Fatalf("third plan not empty: %+v", third.Changes)
			}
		}
		for k := range canon.Items {
			if scratch.WorkspaceOf(k) != scrWS {
				rt.Fatalf("canonical holds %s", k)
			}
		}
		for k := range canon.Tombstones {
			if scratch.WorkspaceOf(k) != scrWS {
				rt.Fatalf("canonical holds a tombstone of %s", k)
			}
		}
	})
}

func aliasOf(s string) ids.Alias { return ids.Alias(s) }
