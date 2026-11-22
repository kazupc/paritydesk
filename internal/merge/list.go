package merge

import "sort"

// Set merges set members against a base. A member is in the result when
// either side has it or the base has it, unless a side whose base is valid
// removed it (it is in the base but not on that side). A side with an
// invalid base never removes. The result is sorted and has no duplicates.
func Set(base, leaving, entering []string, leavingBaseValid, enteringBaseValid bool) []string {
	inBase, inL, inE := toSet(base), toSet(leaving), toSet(entering)
	removed := func(x string) bool {
		if !inBase[x] {
			return false
		}
		return (leavingBaseValid && !inL[x]) || (enteringBaseValid && !inE[x])
	}
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{base, leaving, entering} {
		for _, x := range list {
			if seen[x] || removed(x) {
				continue
			}
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// OrderedList keeps the winner's order, inserts items only the loser has
// next to their nearest surviving neighbour, and drops items removed
// relative to the base (only when that side's base is valid).
func OrderedList(base, winner, loser []string, winnerBaseValid, loserBaseValid bool) []string {
	return OrderedListBases(base, base, winner, loser, winnerBaseValid, loserBaseValid)
}

// OrderedListBases is OrderedList with a separate base per side. The
// result follows the winner's order. A winner item the loser removed
// against its valid base is dropped. A loser-only item is dropped when the
// winner removed it against its valid base; otherwise it is inserted right
// after its nearest preceding neighbour (in the loser's order) that is in
// the result, or first when there is none. Duplicates are dropped.
func OrderedListBases(winnerBase, loserBase, winner, loser []string, winnerBaseValid, loserBaseValid bool) []string {
	inW, inL := toSet(winner), toSet(loser)
	wBase, lBase := toSet(winnerBase), toSet(loserBase)
	out := make([]string, 0, len(winner)+len(loser))
	seen := map[string]bool{}
	for _, x := range winner {
		if seen[x] || (loserBaseValid && lBase[x] && !inL[x]) {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	for i, x := range loser {
		if seen[x] || inW[x] || (winnerBaseValid && wBase[x]) {
			continue
		}
		seen[x] = true
		out = insertAt(out, insertPos(out, loser[:i]), x)
	}
	return out
}

// insertPos returns the index after the last element of prev (searched
// from the end) that is present in out, or 0.
func insertPos(out, prev []string) int {
	for j := len(prev) - 1; j >= 0; j-- {
		for k, y := range out {
			if y == prev[j] {
				return k + 1
			}
		}
	}
	return 0
}

func insertAt(s []string, i int, x string) []string {
	s = append(s, "")
	copy(s[i+1:], s[i:])
	s[i] = x
	return s
}

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
