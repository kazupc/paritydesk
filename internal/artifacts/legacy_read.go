package artifacts

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrLegacyPath: the folder to import is not an absolute path to a real
// folder outside paritydesk's and Claude's own folders.
var ErrLegacyPath = errors.New("artifacts: unusable legacy folder")

// legacyDir reads files below a legacy folder: every path component is
// Lstat'ed (no symlink), files are opened O_NOFOLLOW|O_NONBLOCK (a FIFO
// must not block) and must be regular; nothing is ever written there.
type legacyDir struct{ root string }

// openLegacyDir checks the folder (10.1): absolute, a real directory,
// not inside supportDir and not inside any <dataRoot>/Claude* folder.
// The checks run on the path as given and on the path with every symlink
// resolved (a symlinked parent must not lead into Claude's folders), and
// compare without regard to case (the default macOS volume ignores it).
// Reads then go below the resolved path.
func openLegacyDir(dir, supportDir, dataRoot string) (*legacyDir, error) {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, ErrLegacyPath
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return nil, ErrLegacyPath
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, ErrLegacyPath
	}
	if fi, err := os.Lstat(real); err != nil || !fi.IsDir() {
		return nil, ErrLegacyPath
	}
	dirs := []string{dir, real}
	for _, d := range dirs {
		for _, root := range []string{supportDir, resolvePath(supportDir)} {
			if within(d, root) {
				return nil, ErrLegacyPath
			}
		}
		for _, root := range []string{dataRoot, resolvePath(dataRoot)} {
			if inClaudeFolder(d, root) {
				return nil, ErrLegacyPath
			}
		}
	}
	return &legacyDir{root: real}, nil
}

// within reports whether dir is root or below it, ignoring case.
func within(dir, root string) bool {
	if root == "" {
		return false
	}
	d, r := strings.ToLower(filepath.Clean(dir)), strings.ToLower(filepath.Clean(root))
	return d == r || strings.HasPrefix(d, r+string(filepath.Separator))
}

// inClaudeFolder reports whether dir is inside a <dataRoot>/Claude* folder,
// ignoring case.
func inClaudeFolder(dir, dataRoot string) bool {
	if dataRoot == "" {
		return false
	}
	rel, err := filepath.Rel(strings.ToLower(filepath.Clean(dataRoot)), strings.ToLower(filepath.Clean(dir)))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	first := strings.Split(rel, string(filepath.Separator))[0]
	return strings.HasPrefix(first, "claude")
}

// resolvePath resolves the symlinks of the longest existing prefix of p
// (the rest may not exist yet); "" stays "".
func resolvePath(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; cur = filepath.Dir(cur) {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		if parent := filepath.Dir(cur); parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
	}
}

// Read reads root/rel (at most max bytes); nil when missing.
func (d *legacyDir) Read(rel string, max int64) ([]byte, error) {
	if rel == "" || filepath.IsAbs(rel) || filepath.Clean(rel) != rel || strings.Contains(rel, "..") {
		return nil, ErrLegacyPath
	}
	p := d.root
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, nil
		}
		if !fi.IsDir() {
			return nil, ErrSymlink
		}
	}
	p = filepath.Join(p, parts[len(parts)-1])
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrSymlink
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, ErrSymlink
	}
	if fi.Size() > max {
		return nil, ErrTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrTooLarge
	}
	return b, nil
}

// History lists the regular files of <page>/history.
func (d *legacyDir) History(page string) []string {
	if page == "" || filepath.IsAbs(page) || strings.Contains(page, "..") {
		return nil
	}
	p := d.root
	for _, part := range append(strings.Split(page, "/"), "history") {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if err != nil || !fi.IsDir() {
			return nil
		}
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() {
			out = append(out, e.Name())
		}
	}
	return out
}
