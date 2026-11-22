package plan

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/kazupc/paritydesk/internal/denylist"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

const validateOp = "plan/validate"

// tempNamePatterns are base-name globs of temporary files (editors, atomic
// writers, Chromium). No op may create or write a file whose base name
// matches one, so paritydesk never leaves anything the app could mistake
// for a pending write (D30). Matching is case-insensitive.
var tempNamePatterns = []string{
	"*.tmp",
	"*.tmp.*",
	"*.temp",
	"*~",
	".#*",
	"#*#",
	"*.swp",
	"*.swx",
	"*.crdownload",
	"*.part",
	".paritydesk-tmp*",
}

// invalid returns an Internal validation error. msg names ops by index,
// kind, row, path or key only; never content.
func invalid(format string, args ...any) *pderr.Error {
	return pderr.New(pderr.Internal, validateOp, fmt.Sprintf(format, args...))
}

// Validate runs every check of phase P5: the denylist (manifest lists plus
// the fixed denylist floor), path containment (clean relative paths, no
// names matching the app's tmp recovery globs or temp-file names), the LS
// allow-list and META key, limits, the mapping of every destructive op to
// a deleted change (or a NonUserDataReasons reason), the deletion breaker
// (exit 22), the secret boundary, and the worktree guard (exit 23). It
// returns nil or a *pderr.Error.
//
// Structural refusals (everything except the breaker and the worktree
// guard) are code Internal: they mean an adapter or the merge produced an
// op that must never exist. They are checked first, then the breaker,
// then the worktree guard.
func Validate(in ValidateInput) error {
	if in.Plan == nil {
		return invalid("no plan")
	}
	if err := checkLimits(in); err != nil {
		return err
	}
	deleted := deletedChanges(in.Plan.Changes)
	for i, op := range in.Plan.Ops {
		if err := checkOp(in, deleted, i, op); err != nil {
			return err
		}
	}
	if err := checkBreaker(in); err != nil {
		return err
	}
	return checkWorktrees(in)
}

// checkOp runs every per-op check.
func checkOp(in ValidateInput, deleted map[string]bool, i int, op Op) error {
	checks := []func(ValidateInput, int, Op) error{checkTarget, checkHashes, checkSecret}
	for _, c := range checks {
		if err := c(in, i, op); err != nil {
			return err
		}
	}
	return checkDestructive(deleted, i, op)
}

// checkLimits enforces MaxOps and MaxBytes (zero means no limit).
func checkLimits(in ValidateInput) error {
	if in.MaxOps > 0 && len(in.Plan.Ops) > in.MaxOps {
		return invalid("plan has %d ops, more than the limit of %d", len(in.Plan.Ops), in.MaxOps)
	}
	var total int64
	for _, op := range in.Plan.Ops {
		total += int64(len(op.Content))
	}
	if in.MaxBytes > 0 && total > in.MaxBytes {
		return invalid("plan writes %d bytes, more than the limit of %d", total, in.MaxBytes)
	}
	return nil
}

// checkTarget checks the op's kind, path or key, and content form.
func checkTarget(in ValidateInput, i int, op Op) error {
	switch op.Kind {
	case OpWriteFile, OpQuarantine:
		if op.Key != "" {
			return invalid("op %d (%s): a file op may not name a Local Storage key", i, op.Kind)
		}
		return checkPath(in, i, op)
	case OpLSPut, OpLSDelete:
		if op.Path != "" {
			return invalid("op %d (%s): a Local Storage op may not name a file", i, op.Kind)
		}
		return checkLSKey(in, i, op)
	default:
		return invalid("op %d: unknown kind %q", i, op.Kind)
	}
}

// cleanRel reports whether rel is a clean, slash-separated relative path
// without "..", "." or empty segments, NUL, or a leading slash.
func cleanRel(rel string) bool {
	if rel == "" || strings.ContainsRune(rel, 0) || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return false
	}
	if path.Clean(rel) != rel {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// checkPath applies containment, the deny floor and lists, the allow list,
// and (for writes) the tmp recovery globs and temp-file names.
func checkPath(in ValidateInput, i int, op Op) error {
	p := op.Path
	if !cleanRel(p) {
		return invalid("op %d (%s): path is not a clean relative path", i, op.Kind)
	}
	if denylist.MatchPath(p) || matchAny(in.DenyPaths, p) {
		return invalid("op %d (%s): path %s is never touched", i, op.Kind, p)
	}
	if len(in.AllowPaths) > 0 && !matchAny(in.AllowPaths, p) {
		return invalid("op %d (%s): path %s is not in the allow list", i, op.Kind, p)
	}
	if op.Kind == OpWriteFile && (matchAnyNameOrPath(in.TmpRecoveryGlobs, p) || matchAnyNameOrPath(tempNamePatterns, p)) {
		return invalid("op %d (%s): path %s has a temporary-file name", i, op.Kind, p)
	}
	return nil
}

// matchAny reports whether rel matches any glob (denylist.MatchGlob).
func matchAny(globs []string, rel string) bool {
	return slices.ContainsFunc(globs, func(g string) bool { return denylist.MatchGlob(g, rel) })
}

// matchAnyNameOrPath matches each glob against the whole path and, for
// globs without a slash, against the base name too.
func matchAnyNameOrPath(globs []string, rel string) bool {
	base := path.Base(rel)
	return slices.ContainsFunc(globs, func(g string) bool {
		return denylist.MatchGlob(g, rel) || (!strings.Contains(g, "/") && denylist.MatchGlob(g, base))
	})
}

// checkLSKey applies the LS deny floor and list, the allow list, the META
// key rule, and the encoded-content check.
func checkLSKey(in ValidateInput, i int, op Op) error {
	name := op.Key
	if name == "" {
		return invalid("op %d (%s): empty Local Storage key", i, op.Kind)
	}
	key, err := localstorage.Key(name)
	if err != nil {
		return invalid("op %d (%s): Local Storage key is not valid", i, op.Kind)
	}
	if bytes.Equal(key, localstorage.MetaKey) || strings.HasPrefix(strings.ToUpper(name), "META:") {
		return invalid("op %d (%s): the origin metadata key is never written", i, op.Kind)
	}
	if denylist.MatchLSKey(name) || slices.ContainsFunc(in.DenyLSKeys, func(d string) bool { return lsKeyMatch(d, name) }) {
		return invalid("op %d (%s): Local Storage key %s is never touched", i, op.Kind, name)
	}
	if len(in.AllowLSKeys) > 0 && !slices.Contains(in.AllowLSKeys, name) {
		return invalid("op %d (%s): Local Storage key %s is not in the allow list", i, op.Kind, name)
	}
	if op.Kind == OpLSPut {
		if _, err := localstorage.DecodeText(op.Content); err != nil {
			return invalid("op %d (%s): content of %s is not a valid encoded Local Storage value", i, op.Kind, name)
		}
	}
	return nil
}

// lsKeyMatch matches a manifest deny entry against an item name: a glob
// (path.Match syntax, case-insensitive) or, for a malformed glob, always
// (fail closed).
func lsKeyMatch(pattern, name string) bool {
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(name))
	return ok || err != nil
}

// isHash reports whether s is a lowercase hex sha256.
func isHash(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// checkHashes checks the form of PreHash and PostHash per kind, and that
// PostHash is the hash of Content for writes.
func checkHashes(_ ValidateInput, i int, op Op) error {
	if op.PreHash != "" && !isHash(op.PreHash) {
		return invalid("op %d (%s): malformed pre-hash", i, op.Kind)
	}
	switch op.Kind {
	case OpWriteFile, OpLSPut:
		if !isHash(op.PostHash) || op.PostHash != model.HashBytes(op.Content) {
			return invalid("op %d (%s): post-hash does not match the content", i, op.Kind)
		}
	case OpQuarantine, OpLSDelete:
		if op.PreHash == "" {
			return invalid("op %d (%s): a removal needs the pre-hash of what it removes", i, op.Kind)
		}
		if op.PostHash != "" || len(op.Content) > 0 {
			return invalid("op %d (%s): a removal has no content or post-hash", i, op.Kind)
		}
	}
	return nil
}

// checkSecret refuses secret ops across a managed edge unless every item
// is allowed explicitly. The message names the row only.
func checkSecret(in ValidateInput, i int, op Op) error {
	if !op.Secret || in.SecretsMayCross {
		return nil
	}
	allowed := len(op.Items) > 0
	for _, it := range op.Items {
		allowed = allowed && in.SecretsAllowed[it]
	}
	if !allowed {
		return invalid("op %d (row %s): secret values may not cross to this account", i, op.Row)
	}
	return nil
}

// changeKey identifies a change by row and item key.
func changeKey(row model.RowID, key string) string { return string(row) + "\x00" + key }

// deletedChanges indexes the plan's ChangeDeleted entries.
func deletedChanges(cs []model.Change) map[string]bool {
	out := map[string]bool{}
	for _, c := range cs {
		if c.Kind == model.ChangeDeleted {
			out[changeKey(c.Row, c.Key)] = true
		}
	}
	return out
}

// checkDestructive maps every destructive op to a deleted change of one
// of its rows and items, or to a non-user-data reason.
func checkDestructive(deleted map[string]bool, i int, op Op) error {
	if op.Reason != "" && !slices.Contains(NonUserDataReasons, op.Reason) {
		return invalid("op %d (row %s): unknown reason %q", i, op.Row, op.Reason)
	}
	if !op.Destructive && op.Kind != OpQuarantine && op.Kind != OpLSDelete {
		return nil
	}
	if op.Reason != "" {
		return nil
	}
	rows := append([]model.RowID{op.Row}, op.Rows...)
	for _, r := range rows {
		for _, it := range op.Items {
			if deleted[changeKey(r, it)] {
				return nil
			}
		}
	}
	return invalid("op %d (%s, row %s): destructive op maps to no deleted change", i, op.Kind, op.Row)
}

// checkBreaker trips the deletion breaker per category: more deletions
// than max(MaxDeleteMin, ceil(MaxDeleteFraction*Before)), or deletions
// that leave a non-empty category empty (items the same plan adds,
// revives or restores in that category count against emptying), unless
// --allow-deletions covers the count.
func checkBreaker(in ValidateInput) error {
	deletions := map[model.Category]int{}
	additions := map[model.Category]int{}
	for _, c := range in.Plan.Changes {
		switch c.Kind {
		case model.ChangeDeleted:
			deletions[model.CategoryOf(c.Row)]++
		case model.ChangeAdded, model.ChangeRevived, model.ChangeRestored:
			additions[model.CategoryOf(c.Row)]++
		}
	}
	for _, cat := range sortedCategories(deletions) {
		deleted, before := deletions[cat], in.Before[cat]
		if in.Limits.AllowDeletions >= deleted {
			continue
		}
		limit := max(in.Limits.MaxDeleteMin, int(math.Ceil(in.Limits.MaxDeleteFraction*float64(before))))
		switch {
		case deleted > limit:
			return pderr.New(pderr.DeletionBreaker, validateOp,
				fmt.Sprintf("%d deletions in %s exceed the limit (%d of %d items)", deleted, cat, limit, before))
		case before > 0 && before-deleted+additions[cat] <= 0:
			return pderr.New(pderr.DeletionBreaker, validateOp,
				fmt.Sprintf("%d deletions would leave %s empty (%d items before)", deleted, cat, before))
		}
	}
	return nil
}

// sortedCategories returns the categories of counts in AllCategories
// order, then any others sorted, so the reported category is stable.
func sortedCategories(counts map[model.Category]int) []model.Category {
	var out []model.Category
	for _, c := range model.AllCategories {
		if counts[c] > 0 {
			out = append(out, c)
		}
	}
	var rest []model.Category
	for c := range counts {
		if !slices.Contains(model.AllCategories, c) {
			rest = append(rest, c)
		}
	}
	slices.Sort(rest)
	return append(out, rest...)
}

// checkWorktrees refuses dirty worktrees that would lose their
// registration unless the user accepted the risk.
func checkWorktrees(in ValidateInput) error {
	if in.AcceptWorktreeRisk {
		return nil
	}
	n := 0
	for _, w := range in.Worktrees {
		if w.Dirty {
			n++
		}
	}
	if n > 0 {
		return pderr.New(pderr.WorktreeRisk, validateOp,
			fmt.Sprintf("%d worktrees with uncommitted changes would lose their registration", n))
	}
	return nil
}
