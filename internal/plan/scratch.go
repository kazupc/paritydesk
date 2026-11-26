package plan

import (
	"io/fs"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/kazupc/paritydesk/internal/model"
)

// ScratchNotes carries what row scratch's plan notices need beyond the
// plan's changes: the configured [scratch] limits, and the numbers of
// the free-space check when it left the row out. Memory only: it is in
// neither the plan's JSON nor its hash.
type ScratchNotes struct {
	MaxFilesPerWorkspace int
	MaxWorkspaceBytes    int64
	MaxRunBytes          int64
	MaxRunFiles          int
	// NeedBytes and FreeBytes are set when the row was left out for
	// lack of free disk space.
	NeedBytes, FreeBytes int64
}

// DirHash is the post-hash of an OpMkdir: a directory with perm, domain
// separated from file sha256 values.
func DirHash(mode fs.FileMode) string {
	return model.HashBytes([]byte("paritydesk-dir\x00" + strconv.FormatUint(uint64(mode.Perm()), 8)))
}

// LinkHash is the post-hash of an OpSymlink: a symlink to target.
func LinkHash(target string) string {
	return model.HashBytes([]byte("paritydesk-link\x00" + target))
}

// CleanRel reports whether rel is a clean, slash-separated relative path
// without "..", "." or empty segments, NUL, a backslash or a leading
// slash.
func CleanRel(rel string) bool { return cleanRel(rel) }

// scratchWorkspaceDepth is the number of segments of
// "<root>/<acct>/<org>/<leaf>".
const scratchWorkspaceDepth = 4

// SoftCopy reports whether a copy may be skipped at apply when its source
// changed since the plan: a row-scratch copy from the leaving folder whose
// path is not inside a git unit (spec-scratch 3.3). It is a pure function
// of the op, so plan, apply and the orchestrator agree.
func SoftCopy(op Op) bool {
	return op.Row == model.RowScratch && op.Kind == OpCopyFile && op.Copy != nil &&
		op.Copy.Side == SideLeaving && !GitMember(op.Path)
}

// GitMember reports whether a data-folder-relative scratch path lies
// inside a git unit: a directory segment below the workspace folds to
// ".git".
func GitMember(p string) bool {
	segs := strings.Split(p, "/")
	if len(segs) <= scratchWorkspaceDepth {
		return false
	}
	for _, s := range segs[scratchWorkspaceDepth : len(segs)-1] {
		if foldName(s) == gitFolded {
			return true
		}
	}
	return false
}

// gitFolded is ".git" folded.
var gitFolded = foldName(".git")

// foldName is NFC plus the smallest rune of each simple folding orbit
// (the same rule as sources/scratch.FoldName).
func foldName(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	for _, r := range s {
		m := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < m {
				m = f
			}
		}
		b.WriteRune(m)
	}
	return b.String()
}

// isScratchKind reports whether k is one of row scratch's own kinds.
func isScratchKind(k Kind) bool { return k == OpCopyFile || k == OpMkdir || k == OpSymlink }

// isScratchEdit reports whether an edit is composed without reading its
// location: row scratch's own kinds and its removals.
func isScratchEdit(e Edit) bool {
	switch e.Kind {
	case EditCopy, EditMkdir, EditSymlink:
		return true
	case EditRemove:
		return e.Row == model.RowScratch
	}
	return false
}
