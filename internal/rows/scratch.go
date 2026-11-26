package rows

import (
	"path"
	"slices"
	"strings"

	"github.com/kazupc/paritydesk/internal/denylist"
	"github.com/kazupc/paritydesk/internal/merge"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
)

// scratchModule merges row scratch (spec-scratch 7): scratch workspaces
// mirrored at the same path. Files merge three-way with per-folder
// bases (newest change wins, the loser kept as a sibling); directories
// and symlinks are create-only; a ".git" folder is one unit taken whole
// from one side; workspaces are never removed.
type scratchModule struct{}

func (scratchModule) Row() model.RowID { return model.RowScratch }

// Merge implements Module.
func (scratchModule) Merge(in Input) (Output, error) {
	if err := checkRow(in, model.RowScratch); err != nil {
		return Output{}, err
	}
	sm := newScratchMerge(in)
	return sm.run()
}

// scratchMerge is one merge of row scratch.
type scratchMerge struct {
	in         Input
	pol        *ScratchPolicy
	refs       map[string]bool // the carry set
	referenced map[string]bool
	// s and t are both sides' items after the exclusion rules; t is T's
	// real content (the target of Ops).
	s, t           map[string]model.Item
	sBase, tBase   map[string]model.Item
	canon, lo, eo  *model.RowSnapshot // after exclusion and pruning
	merged         map[string]bool    // workspaces merged this run
	special        map[string]bool    // workspaces not merged (an "over" marker)
	d              map[string]model.Item
	out            Output
	notes          []model.Change
	noted          map[string]bool
	hints          map[string]model.ChangeKind
	unitsReplaced  bool
	deletionNoteWS map[string]bool
	// sHidden and tHidden are the paths where a side holds a directory
	// the exclusion rules drop (hiddenDirs); sLeaves and tLeaves the keys
	// where it holds a file or link they drop (hiddenLeaves).
	sHidden, tHidden map[string]bool
	sLeaves, tLeaves map[string]bool
	// prune is false without a policy: nothing is carried and canonical
	// passes through unpruned.
	prune bool
}

func newScratchMerge(in Input) *scratchMerge {
	pol := in.Policy.Scratch
	if pol == nil {
		pol = &ScratchPolicy{PropagateDeletions: true}
	}
	sm := &scratchMerge{
		in: in, pol: pol, prune: in.Policy.Scratch != nil,
		refs: map[string]bool{}, referenced: map[string]bool{},
		merged: map[string]bool{}, special: map[string]bool{},
		noted: map[string]bool{}, hints: map[string]model.ChangeKind{},
		deletionNoteWS: map[string]bool{},
	}
	for _, r := range sm.pol.Refs {
		sm.refs[r] = true
	}
	for _, r := range sm.pol.Referenced {
		sm.referenced[r] = true
	}
	for _, r := range sm.pol.Refs {
		sm.referenced[r] = true
	}
	return sm
}

// excludedKey applies the exclusion rules of 7.2 step 1 to one key: a
// malformed key, a path matching an exclude glob, a held-back folder, an
// unclean path or a reserved name.
func (sm *scratchMerge) excludedKey(k string) bool {
	ws, rel, _, err := scratch.SplitKey(k)
	if err != nil {
		return true
	}
	if sm.pol.Leaf != nil && !sm.pol.Leaf.MatchString(path.Base(ws)) {
		return true
	}
	if rel == "" {
		return false
	}
	if !plan.CleanRel(rel) || scratch.HeldBackKey(k, sm.pol.HeldBack) {
		return true
	}
	if slices.ContainsFunc(sm.pol.Exclude, func(g string) bool { return denylist.MatchGlob(g, rel) }) {
		return true
	}
	return scratch.MatchReserved(sm.pol.ReservedGlobs, path.Base(rel))
}

// hiddenDirs returns the paths (keys without the trailing slash) where a
// side holds a directory the exclusion rules drop: an excluded directory
// item, or a held-back folder the read noted. A file or link the side's
// base had at such a path was replaced by that directory, so it must not
// read as removed: a change of the rules never looks like a deletion
// (7.2 step 1).
func (sm *scratchMerge) hiddenDirs(snap *model.RowSnapshot) map[string]bool {
	out := map[string]bool{}
	if snap == nil {
		return out
	}
	for k := range snap.Items {
		if strings.HasSuffix(k, "/") && k != scratch.RootKey(scratch.WorkspaceOf(k)) && sm.excludedKey(k) {
			out[strings.TrimSuffix(k, "/")] = true
		}
	}
	for _, n := range snap.Notes {
		if n.Note == scratch.NoteHeldBack && strings.HasSuffix(n.Key, "/") {
			out[strings.TrimSuffix(n.Key, "/")] = true
		}
	}
	return out
}

// hiddenLeaves returns the keys where a side holds a file or link the
// exclusion rules drop. What the side's base had below such a key was
// replaced by that file or link, so it must not read as removed either.
func (sm *scratchMerge) hiddenLeaves(snap *model.RowSnapshot) map[string]bool {
	out := map[string]bool{}
	if snap == nil {
		return out
	}
	for k := range snap.Items {
		if !strings.HasSuffix(k, "/") && sm.excludedKey(k) {
			out[k] = true
		}
	}
	return out
}

// underLeaf reports whether k lies below one of the leaf keys.
func underLeaf(k string, leaves map[string]bool) bool {
	for p := path.Dir(strings.TrimSuffix(k, "/")); p != "." && p != "/"; p = path.Dir(p) {
		if leaves[p] {
			return true
		}
	}
	return false
}

// unhide returns cur with base's items at hidden paths, or below hidden
// leaves, that the side no longer lists, so the engine reads them as
// unchanged.
func unhide(cur, base map[string]model.Item, hidden, leaves map[string]bool) map[string]model.Item {
	if len(hidden) == 0 && len(leaves) == 0 || base == nil {
		return cur
	}
	out := cur
	copied := false
	for k, it := range base {
		if _, ok := cur[k]; ok || !hidden[k] && !underLeaf(k, leaves) {
			continue
		}
		if !copied {
			out = make(map[string]model.Item, len(cur)+1)
			for ck, cv := range cur {
				out[ck] = cv
			}
			copied = true
		}
		out[k] = it
	}
	return out
}

// filterItems returns the items that pass keep.
func filterItems(items map[string]model.Item, keep func(string) bool) map[string]model.Item {
	out := make(map[string]model.Item, len(items))
	for k, it := range items {
		if keep(k) {
			out[k] = it
		}
	}
	return out
}

// filterSnap returns a copy of s with only the items and tombstones that
// pass keep (nil stays nil).
func filterSnap(s *model.RowSnapshot, keep func(string) bool) *model.RowSnapshot {
	if s == nil {
		return nil
	}
	c := cloneOrEmpty(s, model.RowScratch)
	c.Items = filterItems(c.Items, keep)
	for k := range c.Tombstones {
		if !keep(k) {
			delete(c.Tombstones, k)
		}
	}
	c.Order = nil
	return c
}

func snapItems(s *model.RowSnapshot) map[string]model.Item {
	if s == nil {
		return map[string]model.Item{}
	}
	return s.Items
}

// run performs the whole merge.
func (sm *scratchMerge) run() (Output, error) {
	in := sm.in
	notExcluded := func(k string) bool { return !sm.excludedKey(k) }
	sm.s = filterItems(snapItems(in.Leaving.Snapshot), notExcluded)
	sm.t = filterItems(snapItems(in.Entering.Snapshot), notExcluded)
	if in.Leaving.BaseValid {
		sm.sBase = filterItems(snapItems(in.Leaving.Base), notExcluded)
	}
	if in.Entering.BaseValid {
		sm.tBase = filterItems(snapItems(in.Entering.Base), notExcluded)
	}
	sm.sHidden, sm.tHidden = sm.hiddenDirs(in.Leaving.Snapshot), sm.hiddenDirs(in.Entering.Snapshot)
	sm.sLeaves, sm.tLeaves = sm.hiddenLeaves(in.Leaving.Snapshot), sm.hiddenLeaves(in.Entering.Snapshot)
	// Step 2: prune canonical and overlays to referenced workspaces.
	keepCanon := func(k string) bool {
		return notExcluded(k) && (!sm.prune || sm.referenced[scratch.WorkspaceOf(k)])
	}
	sm.canon = filterSnap(in.Canonical, keepCanon)
	sm.lo = filterSnap(in.LeavingOverlay, keepCanon)
	sm.eo = filterSnap(in.EnteringOverlay, keepCanon)
	// Step 3: classify the carry set.
	for ws := range sm.refs {
		if over(sm.s[scratch.RootKey(ws)]) || over(sm.t[scratch.RootKey(ws)]) {
			sm.special[ws] = true
			for _, side := range []map[string]model.Item{sm.s, sm.t} {
				if it, ok := side[scratch.RootKey(ws)]; ok && over(it) {
					sm.addNote(ws, noteForOver(it))
				}
			}
			continue
		}
		sm.merged[ws] = true
	}
	engIn, err := sm.engineInput()
	if err != nil {
		return Output{}, err
	}
	spec := keyedSpec{row: model.RowScratch, fixup: sm.reviveEdits}
	out, err := runKeyed(engIn, spec)
	if err != nil {
		return Output{}, err
	}
	for _, c := range out.Changes {
		// A file T lacks is "added" even when the engine restored it
		// from one side: only a revival against a removal is reported.
		if c.Kind == model.ChangeRevived {
			sm.hints[c.Key] = c.Kind
		}
	}
	sm.out = out
	engDesired := map[string]model.Item{}
	if out.Desired != nil {
		for k, it := range out.Desired.Items {
			engDesired[k] = it
		}
	}
	sm.d = map[string]model.Item{}
	for k, it := range engDesired {
		sm.d[k] = it
	}
	if !in.Policy.EnteringImports {
		// The target keeps its own state; nothing is carried.
		sm.d = map[string]model.Item{}
		for k, it := range sm.t {
			sm.d[k] = it
		}
	} else {
		sm.createOnlyDirs()
		sm.createOnlyLinks()
		sm.typeConflicts()
		if err := sm.gitUnits(engDesired); err != nil {
			return Output{}, err
		}
		if !sm.pol.PropagateDeletions {
			if err := sm.deletionsOff(); err != nil {
				return Output{}, err
			}
		}
		if err := sm.conflictCopies(); err != nil {
			return Output{}, err
		}
		sm.realisable()
	}
	sm.specialMarkers()
	sm.restrictToRefs()
	return sm.output(), nil
}

// over reports whether a root marker says its workspace is not merged.
func over(it model.Item) bool {
	v, ok := scratch.Decode(it)
	return ok && v.K == scratch.KindRoot && v.Over != ""
}

// noteForOver maps a marker's "over" member to its note.
func noteForOver(it model.Item) string {
	v, _ := scratch.Decode(it)
	switch v.Over {
	case scratch.OverFiles:
		return scratch.NoteOversizeFiles
	case scratch.OverBytes:
		return scratch.NoteOversizeBytes
	case scratch.OverBudget:
		return scratch.NoteBudget
	case scratch.OverLink:
		return scratch.NoteRootLink
	case scratch.OverReadOnly:
		return scratch.NoteReadOnly
	}
	return scratch.NoteUnreadable
}

// addNote records one constant note per (key, note).
func (sm *scratchMerge) addNote(key, n string) {
	id := key + "\x00" + n
	if sm.noted[id] {
		return
	}
	sm.noted[id] = true
	sm.notes = append(sm.notes, model.Change{Row: model.RowScratch, Kind: model.ChangeSkipped, Key: key, Note: n})
}

// engineKey reports whether k takes part in the engine: a key of a
// merged workspace that is not a git unit member.
func (sm *scratchMerge) engineKey(k string) bool {
	if !sm.merged[scratch.WorkspaceOf(k)] {
		return false
	}
	_, member := scratch.GitUnit(k)
	return !member
}

// engineInput builds the keyed engine's input (7.2 steps 4 and 5).
func (sm *scratchMerge) engineInput() (Input, error) {
	in := sm.in
	tEng := map[string]model.Item{}
	for k, it := range unhide(sm.t, sm.tBase, sm.tHidden, sm.tLeaves) {
		tEng[k] = it
	}
	sEng := unhide(sm.s, sm.sBase, sm.sHidden, sm.sLeaves)
	// Step 4: a merged workspace T lacks is read as T's base (unchanged),
	// so S's state is taken whole and nothing is deleted.
	for ws := range sm.merged {
		if _, ok := sm.t[scratch.RootKey(ws)]; ok || sm.tBase == nil {
			continue
		}
		for k, it := range sm.tBase {
			if scratch.WorkspaceOf(k) == ws {
				tEng[k] = it
			}
		}
	}
	snap := func(items map[string]model.Item) *model.RowSnapshot {
		s := emptySnapshot(model.RowScratch)
		s.Tombstones = nil
		s.Items = filterItems(items, sm.engineKey)
		return s
	}
	out := in
	out.Row = model.RowScratch
	out.Leaving.Snapshot = nil
	if in.Leaving.Snapshot != nil {
		out.Leaving.Snapshot = snap(sEng)
	}
	out.Entering.Snapshot = nil
	if in.Entering.Snapshot != nil {
		out.Entering.Snapshot = snap(tEng)
	}
	if in.Leaving.BaseValid {
		out.Leaving.Base = snap(sm.sBase)
	}
	if in.Entering.BaseValid {
		out.Entering.Base = snap(sm.tBase)
	}
	out.Canonical = filterSnap(sm.canon, sm.engineKey)
	if in.Canonical == nil {
		out.Canonical = nil
	}
	out.LeavingOverlay = filterSnap(sm.lo, sm.engineKey)
	out.EnteringOverlay = filterSnap(sm.eo, sm.engineKey)
	return out, nil
}

// reviveEdits keeps an edit against a removal: a file T changed since its
// valid base and S removed comes back in canonical (case table 7.6,
// "removed | modified"), and the conflict the epoch rule recorded is
// dropped. While either base is invalid, a file both sides hold with
// different content is a conflict decided by the newer mtime, so the
// loser is kept as a sibling (7.6, "either base invalid").
func (sm *scratchMerge) reviveEdits(m *merger) error {
	if m.union {
		return sm.unionConflicts(m)
	}
	for k, it := range m.e.items {
		if scratch.KindOf(it) != scratch.KindFile {
			continue
		}
		if _, inCanon := m.canon.Items[k]; inCanon {
			continue
		}
		if b, ok := m.e.base[k]; ok && b.Hash == it.Hash {
			continue // T did not change it
		}
		_, sHas := m.l.items[k]
		_, sHad := m.l.base[k]
		if sHas || !sHad {
			continue // not a removal by S
		}
		m.canon.Items[k] = it
		delete(m.canon.Tombstones, k)
		m.hints[k] = model.ChangeRevived
		m.conflicts = slices.DeleteFunc(m.conflicts, func(c model.Conflict) bool { return c.Key == k })
	}
	return nil
}

// unionConflicts records a conflict for every file (and git unit) both
// sides hold with different content while a base is invalid: the newer
// mtime wins (then the leaving side), and a file's loser becomes a
// sibling; a unit keeps each side's copy (7.4).
//
// Only a two-sided conflict counts as already decided: a canonical value
// one side's version replaced (Reason canonical, no loser side) does not
// stop the two folders' versions from being compared, and it gives way to
// the two-sided conflict, whose loser is kept.
func (sm *scratchMerge) unionConflicts(m *merger) error {
	conflicted := map[string]bool{}
	for _, c := range m.conflicts {
		if twoSided(c) {
			conflicted[c.Key] = true
		}
	}
	for _, k := range sortedKeys(m.e.items) {
		e := m.e.items[k]
		l, ok := m.l.items[k]
		kind := scratch.KindOf(e)
		if !ok || conflicted[k] || l.Hash == e.Hash || kind != scratch.KindOf(l) || (kind != scratch.KindFile && kind != scratch.KindGit) {
			continue
		}
		w, why := merge.Prefer(merge.Version{Item: &l}, merge.Version{Item: &e})
		win, lose := l, e
		if w == merge.Entering {
			win, lose = e, l
		}
		m.canon.Items[k] = win
		delete(m.canon.Tombstones, k)
		m.conflicts = slices.DeleteFunc(m.conflicts, func(c model.Conflict) bool { return c.Key == k })
		m.addConflict(k, w, lose.Hash, why)
	}
	return nil
}

// twoSided reports whether a conflict is between the two folders' versions
// (a loser side), not a canonical value one side replaced.
func twoSided(c model.Conflict) bool {
	return c.Loser != "" && c.Reason != reasonCanonical
}

// createOnlyDirs: a directory (or root marker) T has stays with T's value
// (7.3 step 1).
func (sm *scratchMerge) createOnlyDirs() {
	for k, it := range sm.t {
		if !sm.merged[scratch.WorkspaceOf(k)] {
			continue
		}
		if _, member := scratch.GitUnit(k); member {
			continue
		}
		switch scratch.KindOf(it) {
		case scratch.KindDir, scratch.KindRoot:
			sm.d[k] = it
		}
	}
}

// createOnlyLinks: a link T has keeps T's value; a link where T holds a
// file keeps T's file; a new link whose target is absolute or leaves its
// workspace is dropped (7.3 step 2).
func (sm *scratchMerge) createOnlyLinks() {
	for k, it := range sm.t {
		if !sm.merged[scratch.WorkspaceOf(k)] || scratch.KindOf(it) != scratch.KindLink {
			continue
		}
		if _, member := scratch.GitUnit(k); member {
			continue
		}
		if d, ok := sm.d[k]; !ok || d.Hash != it.Hash {
			sm.addNote(k, scratch.NoteLinkKept)
		}
		sm.d[k] = it
	}
	for k, it := range sm.d {
		if scratch.KindOf(it) != scratch.KindLink {
			continue
		}
		if t, inT := sm.t[k]; inT {
			// A link where T holds a file is a change of type: T's form
			// is kept (7.3 step 3), so a file is never replaced by a link.
			if scratch.KindOf(t) != scratch.KindLink {
				sm.d[k] = t
				sm.addNote(k, scratch.NoteTypeConflict)
			}
			continue
		}
		v, _ := scratch.Decode(it)
		if !scratch.LinkInside(k, v.Target) {
			delete(sm.d, k)
			sm.addNote(k, scratch.NoteLinkKept)
		}
	}
}

// hasPrefixKey reports whether items hold a key below x+"/".
func hasPrefixKey(items map[string]model.Item, x string) bool {
	p := x + "/"
	for k := range items {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// typeConflicts keeps T's form where a path is a file or link on one side
// and a folder on the other (7.3 step 3).
func (sm *scratchMerge) typeConflicts() {
	cand := map[string]bool{}
	for _, items := range []map[string]model.Item{sm.d, sm.t} {
		for k, it := range items {
			if !sm.merged[scratch.WorkspaceOf(k)] {
				continue
			}
			switch scratch.KindOf(it) {
			case scratch.KindFile, scratch.KindLink:
				cand[k] = true
			}
		}
	}
	// A path where T holds a directory the rules drop keeps that form.
	for _, x := range sortedSet(sm.tHidden) {
		if it, ok := sm.d[x]; ok && sm.merged[scratch.WorkspaceOf(x)] {
			switch scratch.KindOf(it) {
			case scratch.KindFile, scratch.KindLink:
				delete(sm.d, x)
				sm.addNote(x, scratch.NoteTypeConflict)
			}
		}
	}
	// A path where T holds a file or link the rules drop keeps that form.
	for _, x := range sortedSet(sm.tLeaves) {
		if sm.merged[scratch.WorkspaceOf(x)] && hasPrefixKey(sm.d, x) {
			sm.dropPrefix(x + "/")
			sm.addNote(x, scratch.NoteTypeConflict)
		}
	}
	keys := sortedSet(cand)
	for _, x := range keys {
		_, hasEntry := sm.d[x]
		hasSub := hasPrefixKey(sm.d, x)
		tIt, tEntry := sm.t[x]
		tSub := hasPrefixKey(sm.t, x)
		if !(hasEntry && hasSub) && !(hasSub && tEntry && !hasEntry) && !(hasEntry && tSub && !hasSub) {
			continue
		}
		sm.addNote(x, scratch.NoteTypeConflict)
		switch {
		case tEntry:
			sm.d[x] = tIt
			sm.dropPrefix(x + "/")
		case tSub:
			delete(sm.d, x)
			sm.dropPrefix(x + "/")
			for k, it := range sm.t {
				if strings.HasPrefix(k, x+"/") {
					sm.d[k] = it
				}
			}
		default:
			sm.dropPrefix(x + "/")
		}
	}
}

// dropPrefix removes every Desired key starting with p.
func (sm *scratchMerge) dropPrefix(p string) {
	for k := range sm.d {
		if strings.HasPrefix(k, p) {
			delete(sm.d, k)
		}
	}
}

// unitSide names the side a unit is taken from.
type unitSide int

const (
	unitAbsent unitSide = iota
	unitT
	unitS
)

// gitUnits sets every git unit's members from exactly one side (7.4).
func (sm *scratchMerge) gitUnits(engDesired map[string]model.Item) error {
	units := map[string]bool{}
	for _, items := range []map[string]model.Item{engDesired, sm.s, sm.t, snapItems(sm.canon)} {
		for k := range items {
			if sm.merged[scratch.WorkspaceOf(k)] && scratch.IsGitUnitKey(k) {
				units[k] = true
			}
		}
	}
	// Only a two-sided conflict keeps each side's unit: a canonical value
	// one side replaced is not a change of the other folder's repository.
	conflicted := map[string]bool{}
	for _, c := range sm.out.Conflicts {
		if units[c.Key] && twoSided(c) {
			conflicted[c.Key] = true
		}
	}
	sm.out.Conflicts = slices.DeleteFunc(sm.out.Conflicts, func(c model.Conflict) bool { return units[c.Key] })
	for _, u := range sortedSet(units) {
		if _, fileForm := sm.d[strings.TrimSuffix(u, "/")]; fileForm {
			sm.dropPrefix(u)
			continue
		}
		s, sOK := sm.s[u]
		t, tOK := sm.t[u]
		c, cOK := snapItems(sm.canon)[u]
		d, dOK := engDesired[u]
		sv, tv, dv := digest(s), digest(t), digest(d)
		var side unitSide
		switch {
		case cOK && scratch.IsDiverged(c):
			switch {
			case sOK && tOK && sv == tv:
				side = unitT
				sm.out.Canonical.Items[u] = s
				delete(sm.out.Canonical.Tombstones, u)
			case !tOK && sOK:
				side = unitS
				sm.setCanonDiverged(u, c)
			default:
				side = unitT
				sm.setCanonDiverged(u, c)
				sm.addNote(u, scratch.NoteGitDiverged)
			}
		case conflicted[u]:
			side = unitT
			div, err := scratch.DivergedItem(u)
			if err != nil {
				return err
			}
			sm.setCanonDiverged(u, div)
			sm.addNote(u, scratch.NoteGitConflict)
		case !dOK:
			side = unitAbsent
		case tOK && dv == tv:
			side = unitT
		case sOK && dv == sv:
			side = unitS
		default:
			side = unitT
			sm.addNote(u, scratch.NoteBytes)
		}
		if err := sm.setUnit(u, side); err != nil {
			return err
		}
	}
	return nil
}

func digest(it model.Item) string {
	v, _ := scratch.Decode(it)
	if v.Diverged {
		return ""
	}
	return v.Digest
}

func (sm *scratchMerge) setCanonDiverged(u string, div model.Item) {
	sm.out.Canonical.Items[u] = div
	delete(sm.out.Canonical.Tombstones, u)
}

// setUnit sets Desired of unit u and its members from one side. T's own
// directories and links inside the unit are kept (they are never
// removed), and the unit's item is recomputed when they were added.
func (sm *scratchMerge) setUnit(u string, side unitSide) error {
	sm.dropPrefix(u)
	tMembers := map[string]model.Item{}
	for k, it := range sm.t {
		if strings.HasPrefix(k, u) && k != u {
			tMembers[k] = it
		}
	}
	switch side {
	case unitT:
		if t, ok := sm.t[u]; ok {
			sm.d[u] = t
			for k, it := range tMembers {
				sm.d[k] = it
			}
		}
		return nil
	case unitS:
		sm.unitsReplaced = sm.unitsReplaced || sm.t[u].Hash != sm.s[u].Hash
	}
	members := map[string]model.Item{}
	kept := false
	if side == unitS {
		for k, it := range sm.s {
			if !strings.HasPrefix(k, u) || k == u {
				continue
			}
			// A link of S's unit whose target is absolute or leaves the
			// workspace is never created (7.3 step 2); T's own link at
			// the same path is kept below.
			if v, ok := scratch.Decode(it); ok && v.K == scratch.KindLink && !scratch.LinkInside(k, v.Target) {
				if t, inT := tMembers[k]; !inT || t.Hash != it.Hash {
					sm.addNote(k, scratch.NoteLinkKept)
					kept = true
					continue
				}
			}
			members[k] = it
		}
	}
	for k, it := range tMembers {
		switch scratch.KindOf(it) {
		case scratch.KindDir, scratch.KindLink:
			if m, ok := members[k]; !ok || m.Hash != it.Hash {
				members[k] = it
				kept = true
			}
		}
	}
	// A file of the chosen side below a kept T link, or where T keeps a
	// directory, cannot be written: T's form wins.
	for k, it := range members {
		if scratch.KindOf(it) != scratch.KindFile {
			continue
		}
		for tk, tit := range tMembers {
			if scratch.KindOf(tit) == scratch.KindLink && (k == tk || strings.HasPrefix(k, tk+"/")) ||
				scratch.KindOf(tit) == scratch.KindDir && k+"/" == tk {
				delete(members, k)
				kept = true
				break
			}
		}
	}
	if side == unitAbsent && len(members) == 0 {
		return nil
	}
	for k, it := range members {
		sm.d[k] = it
	}
	if side == unitS && !kept {
		sm.d[u] = sm.s[u]
		return nil
	}
	var mode uint32
	if v, ok := scratch.Decode(sm.s[u]); ok && side == unitS {
		mode = v.Mode
	} else if v, ok := scratch.Decode(sm.t[u]); ok {
		mode = v.Mode
	}
	it, err := scratch.UnitItem(u, mode, members)
	if err != nil {
		return err
	}
	sm.d[u] = it
	return nil
}

// deletionsOff puts back files (and whole units) T has that Desired
// lacks, one note per workspace (7.3 step 5).
func (sm *scratchMerge) deletionsOff() error {
	for _, k := range sortedKeys(sm.t) {
		it := sm.t[k]
		ws := scratch.WorkspaceOf(k)
		if !sm.merged[ws] {
			continue
		}
		if _, ok := sm.d[k]; ok {
			continue
		}
		if _, member := scratch.GitUnit(k); member {
			continue
		}
		switch scratch.KindOf(it) {
		case scratch.KindFile:
			sm.d[k] = it
		case scratch.KindGit:
			if err := sm.setUnit(k, unitT); err != nil {
				return err
			}
		default:
			continue
		}
		if !sm.deletionNoteWS[ws] {
			sm.deletionNoteWS[ws] = true
			sm.addNote(ws, scratch.NoteDeletionOff)
		}
	}
	return nil
}

// conflictCopies keeps every file conflict's loser next to the winner as
// "<stem> (conflict from <alias> <date>)<ext>" (7.3 step 6, section 8).
// The sibling also enters canonical, so the next switch back carries it.
func (sm *scratchMerge) conflictCopies() error {
	for _, c := range sm.out.Conflicts {
		if !twoSided(c) {
			continue
		}
		if _, member := scratch.GitUnit(c.Key); member {
			continue
		}
		ws, rel, kind, err := scratch.SplitKey(c.Key)
		if err != nil || kind != scratch.KeyEntry {
			continue
		}
		loser, ok := sm.loserItem(c)
		if !ok || scratch.KindOf(loser) != scratch.KindFile {
			continue
		}
		taken := func(r string) bool {
			f := scratch.FoldRel(ws + "/" + r)
			for _, items := range []map[string]model.Item{sm.d, sm.t} {
				for k := range items {
					if scratch.FoldRel(strings.TrimSuffix(k, "/")) == f {
						return true
					}
				}
			}
			return false
		}
		name, ok := scratch.ConflictName(rel, c.Loser, sm.pol.ConflictDate, taken)
		if !ok {
			sm.addNote(c.Key, scratch.NoteConflictName)
			continue
		}
		key := scratch.EntryKey(ws, name)
		loser.Key = key
		sm.d[key] = loser
		sm.out.Canonical.Items[key] = loser
		delete(sm.out.Canonical.Tombstones, key)
	}
	return nil
}

// loserItem finds the losing version of a conflict on either side.
func (sm *scratchMerge) loserItem(c model.Conflict) (model.Item, bool) {
	for _, items := range []map[string]model.Item{sm.s, sm.t} {
		if it, ok := items[c.Key]; ok && it.Hash == c.LoserHash {
			return it, true
		}
	}
	return model.Item{}, false
}

// realisable keeps T's item for every file whose content neither folder
// holds (7.3 step 7).
func (sm *scratchMerge) realisable() {
	have := map[string]bool{}
	for _, items := range []map[string]model.Item{sm.s, sm.t} {
		for _, it := range items {
			if v, ok := scratch.Decode(it); ok && v.K == scratch.KindFile {
				have[v.SHA256] = true
			}
		}
	}
	for _, k := range sortedKeys(sm.d) {
		v, ok := scratch.Decode(sm.d[k])
		if !ok || v.K != scratch.KindFile || have[v.SHA256] {
			continue
		}
		if t, ok := sm.t[k]; ok {
			sm.d[k] = t
		} else {
			delete(sm.d, k)
		}
		sm.addNote(k, scratch.NoteBytes)
	}
}

// specialMarkers: Desired holds T's items of workspaces not merged this
// run (7.3 step 8).
func (sm *scratchMerge) specialMarkers() {
	for ws := range sm.special {
		sm.dropPrefix(ws + "/")
		for k, it := range sm.t {
			if scratch.WorkspaceOf(k) == ws {
				sm.d[k] = it
			}
		}
	}
}

// restrictToRefs keeps Desired within the carry set (invariant
// keys(Desired) ⊆ workspaces in Refs).
func (sm *scratchMerge) restrictToRefs() {
	for k := range sm.d {
		if !sm.refs[scratch.WorkspaceOf(k)] {
			delete(sm.d, k)
		}
	}
}

// output assembles the module's output: canonical and overlays with the
// items of workspaces the engine did not see passed through, Desired,
// changes for files, links and units, the notes, and the confirmation
// rule of 7.3 step 9.
func (sm *scratchMerge) output() Output {
	pass := func(eng, src *model.RowSnapshot) *model.RowSnapshot {
		out := cloneOrEmpty(eng, model.RowScratch)
		if src != nil {
			for k, it := range src.Items {
				if !sm.engineKey(k) {
					if _, member := scratch.GitUnit(k); !member {
						out.Items[k] = it
					}
				}
			}
			for k, t := range src.Tombstones {
				if !sm.engineKey(k) {
					out.Tombstones[k] = t
				}
			}
		}
		return out
	}
	o := Output{
		Canonical:       pass(sm.out.Canonical, sm.canon),
		LeavingOverlay:  pass(sm.out.LeavingOverlay, sm.lo),
		EnteringOverlay: pass(sm.out.EnteringOverlay, sm.eo),
		Conflicts:       sm.out.Conflicts,
	}
	d := emptySnapshot(model.RowScratch)
	d.Tombstones = nil
	d.Items = sm.d
	o.Desired = d
	replaced := false
	for _, k := range unionKeys(sm.d, sm.t) {
		var before, after *model.Item
		if it, ok := sm.t[k]; ok {
			before = &it
		}
		if it, ok := sm.d[k]; ok {
			after = &it
		}
		kind := scratch.KindOf(pick(after, before))
		if kind != scratch.KindFile && kind != scratch.KindLink && kind != scratch.KindGit {
			continue
		}
		ck := merge.KindOf(before, after, sm.hints[k])
		if ck == "" {
			continue
		}
		if ck == model.ChangeUpdated && kind == scratch.KindFile {
			replaced = true
		}
		o.Changes = append(o.Changes, model.Change{Row: model.RowScratch, Kind: ck, Key: k, Note: noteFor(ck)})
	}
	o.Changes = append(o.Changes, sm.notes...)
	union := !sm.in.Leaving.BaseValid || !sm.in.Entering.BaseValid
	o.NeedsConfirmation = union && (len(o.Conflicts) > 0 || replaced || sm.unitsReplaced)
	return o
}

func pick(a, b *model.Item) model.Item {
	if a != nil {
		return *a
	}
	if b != nil {
		return *b
	}
	return model.Item{}
}
