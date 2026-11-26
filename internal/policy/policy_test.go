package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// TestNoNetworkInDependencies enforces zero network (D22): no package in
// the module's dependency graph, with or without its tests, is net,
// net/http or crypto/tls.
func TestNoNetworkInDependencies(t *testing.T) {
	root := repoRoot(t)
	banned := map[string]bool{"net": true, "net/http": true, "crypto/tls": true}
	for _, args := range [][]string{{"list", "-e", "-deps", "./..."}, {"list", "-e", "-deps", "-test", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go %s: %v", strings.Join(args, " "), err)
		}
		for _, pkg := range strings.Fields(string(out)) {
			if banned[pkg] {
				t.Errorf("%s is in the dependency graph (go %s)", pkg, strings.Join(args, " "))
			}
		}
	}
}

// goFile is a parsed Go file.
type goFile struct {
	rel  string
	file *ast.File
	fset *token.FileSet
}

func goFiles(t *testing.T, includeTests bool) []goFile {
	t.Helper()
	root := repoRoot(t)
	var out []goFile
	for _, rel := range trackedFiles(t) {
		if !strings.HasSuffix(rel, ".go") || strings.HasPrefix(rel, policyDir) || strings.Contains(rel, "/testdata/") {
			continue
		}
		if !includeTests && strings.HasSuffix(rel, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		out = append(out, goFile{rel: rel, file: f, fset: fset})
	}
	return out
}

// importName returns the local name under which f imports path, or "".
func importName(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return path[strings.LastIndex(path, "/")+1:]
	}
	return ""
}

// TestExecOnlyInExecGo: os/exec (and process spawning in general) is used
// only by internal/app/exec.go; test files are exempt.
func TestExecOnlyInExecGo(t *testing.T) {
	spawn := map[string][]string{"os": {"StartProcess"}, "syscall": {"Exec", "ForkExec", "StartProcess"}}
	for _, g := range goFiles(t, false) {
		if g.rel == "internal/app/exec.go" {
			continue
		}
		if importName(g.file, "os/exec") != "" {
			t.Errorf("%s imports os/exec; external commands run only through internal/app/exec.go", g.rel)
		}
		names := map[string]string{}
		for pkg := range spawn {
			if n := importName(g.file, pkg); n != "" {
				names[n] = pkg
			}
		}
		ast.Inspect(g.file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok {
				for _, fn := range spawn[names[x.Name]] {
					if sel.Sel.Name == fn {
						t.Errorf("%s:%d: %s.%s spawns a process", g.rel, g.fset.Position(sel.Pos()).Line, x.Name, fn)
					}
				}
			}
			return true
		})
	}
}

// TestNoLevelDBRecover: goleveldb's Recover and RecoverFile rewrite a
// database and are never used (D5, M15).
func TestNoLevelDBRecover(t *testing.T) {
	const pkg = "github.com/syndtr/goleveldb/leveldb"
	for _, g := range goFiles(t, true) {
		name := importName(g.file, pkg)
		ast.Inspect(g.file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.SelectorExpr:
				if x, ok := v.X.(*ast.Ident); ok && name != "" && x.Name == name && (v.Sel.Name == "Recover" || v.Sel.Name == "RecoverFile") {
					t.Errorf("%s:%d: %s.%s is banned", g.rel, g.fset.Position(v.Pos()).Line, x.Name, v.Sel.Name)
				}
			case *ast.Ident:
				if v.Name == "RecoverFile" {
					t.Errorf("%s:%d: RecoverFile is banned", g.rel, g.fset.Position(v.Pos()).Line)
				}
			}
			return true
		})
	}
}

// signOutRE matches advice to end an account's session.
var signOutRE = regexp.MustCompile(`(?i)\b(sign|log)[\s-]*out\b`)

// signOutAllowlist lists exact phrases allowed to match signOutRE. It is
// empty: the rule's own text lives only in this package.
var signOutAllowlist = map[string]bool{}

// TestNoSignOutAdvice (D2, K24): no Go string literal outside tests, no
// i18n catalog entry, no plugin file and no document says "sign out",
// "log out" or "logout".
func TestNoSignOutAdvice(t *testing.T) {
	check := func(where, s string) {
		for _, m := range signOutRE.FindAllString(s, -1) {
			if !signOutAllowlist[strings.ToLower(m)] {
				t.Errorf("%s: %q advises ending an account's session", where, m)
			}
		}
	}
	for _, g := range goFiles(t, false) {
		ast.Inspect(g.file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					check(g.rel+":"+strconv.Itoa(g.fset.Position(lit.Pos()).Line), s)
				}
			}
			return true
		})
	}
	docs := textFiles(t, func(rel string) bool {
		return !strings.HasPrefix(rel, policyDir) &&
			(strings.HasSuffix(rel, ".md") || underAny(rel, "plugin/", ".claude-plugin/"))
	})
	if len(docs) == 0 {
		t.Fatal("no documents found")
	}
	for _, f := range docs {
		for _, loc := range signOutRE.FindAllIndex(f.data, -1) {
			check(f.rel+":"+strconv.Itoa(lineOf(f.data, loc[0])), string(f.data[loc[0]:loc[1]]))
		}
	}
}

// credentialPatterns name credential stores paritydesk never reads.
var credentialPatterns = []string{"Cookies", "Login Data", "find-generic-password", "CLAUDE_CODE_OAUTH_TOKEN"}

// credentialTestAllowlist lists test files that name a credential store
// only to assert that it is refused (deny floor and manifest checks).
// Each entry is a file owned by another work item; see the WI-8 report.
var credentialTestAllowlist = map[string]string{
	"internal/compat/compat_test.go": "asserts that never_touched lists the stores",
	"internal/plan/validate_test.go": "asserts that the deny floor refuses the stores",
}

// TestNoCredentialStoreReferences (D22): credential stores are named only
// by the compat manifests, the fixed deny floor and this package.
func TestNoCredentialStoreReferences(t *testing.T) {
	files := textFiles(t, func(rel string) bool {
		return !underAny(rel, "internal/compat/manifests/", "internal/denylist/", policyDir)
	})
	for _, f := range files {
		for _, p := range credentialPatterns {
			i := strings.Index(string(f.data), p)
			if i < 0 {
				continue
			}
			if reason, ok := credentialTestAllowlist[f.rel]; ok {
				t.Logf("%s:%d names %q (allow-listed: %s)", f.rel, lineOf(f.data, i), p, reason)
				continue
			}
			t.Errorf("%s:%d names the credential store %q", f.rel, lineOf(f.data, i), p)
		}
	}
}

var (
	uuidRE        = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	fakeUUIDRE    = regexp.MustCompile(`^00000000-0000-4000-8000-[0-9a-f]{12}$`)
	emailRE       = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	exampleDomain = regexp.MustCompile(`(?i)@(example\.(com|org|net)|users\.noreply\.github\.com)$`)
)

// TestFixturesUseFakeIDs (PII guard): every UUID in the repository is an
// allow-listed fake (00000000-0000-4000-8000-...), fixtures hold no
// email-like strings, and other files only example.com-style addresses.
func TestFixturesUseFakeIDs(t *testing.T) {
	for _, f := range textFiles(t, func(rel string) bool { return !strings.HasPrefix(rel, policyDir) && rel != "go.sum" }) {
		for _, loc := range uuidRE.FindAllIndex(f.data, -1) {
			if !fakeUUIDRE.Match(f.data[loc[0]:loc[1]]) {
				t.Errorf("%s:%d holds a UUID that is not an allow-listed fake", f.rel, lineOf(f.data, loc[0]))
			}
		}
		for _, loc := range emailRE.FindAllIndex(f.data, -1) {
			m := f.data[loc[0]:loc[1]]
			if isFixture(f.rel) || !exampleDomain.Match(m) {
				t.Errorf("%s:%d holds an email-like string", f.rel, lineOf(f.data, loc[0]))
			}
		}
	}
}

// japaneseAllowed reports the files that may hold Japanese letters: the
// Japanese catalog, README.ja.md, README.md (its language-switch line)
// and the Japanese goldens. Tests write Japanese as \u escapes;
// docs/dev/i18n.md is English only.
func japaneseAllowed(rel string) bool {
	return rel == "internal/i18n/catalog_ja.go" || rel == "README.ja.md" || rel == "README.md" ||
		underAny(rel, "internal/cli/testdata/ja/", "internal/render/testdata/ja/")
}

// japaneseLetter reports Han, Hiragana, Katakana and the prolonged sound
// mark (U+30FC).
func japaneseLetter(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) || r == 0x30fc
}

// TestNoNonASCIILetters (PII guard): no file holds letters outside ASCII,
// so names in other scripts cannot slip in. Punctuation such as the
// ellipsis is allowed. The Japanese files (japaneseAllowed) may hold
// Japanese letters only (Han, Hiragana, Katakana, U+30FC); Latin with
// diacritics, Cyrillic or full-width Latin still fail there.
func TestNoNonASCIILetters(t *testing.T) {
	for _, f := range textFiles(t, nil) {
		ja := japaneseAllowed(f.rel)
		for i, r := range string(f.data) {
			if r > unicode.MaxASCII && unicode.IsLetter(r) && !(ja && japaneseLetter(r)) {
				t.Errorf("%s:%d holds a non-ASCII letter (U+%04X)", f.rel, lineOf(f.data, i), r)
				break
			}
		}
	}
}

// realPathTestAllowlist lists test files that name the real home or the
// system Applications folder only to prove that the CheckTestPath guards
// refuse them (or as inert strings in parsing tests). Each entry is a
// file owned by another work item; see the WI-8 report.
var realPathTestAllowlist = map[string]string{
	"internal/fsx/fsx_test.go":                  "CheckTestPath refusal cases",
	"internal/fsx/default_darwin_test.go":       "CheckTestPath refusal cases",
	"internal/app/platform_test.go":             "NewDarwin refusal cases",
	"internal/app/procs_test.go":                "bundle path strings in process matching",
	"internal/store/kv/leveldb/leveldb_test.go": "leveldb.New refusal cases",
}

// TestTestsAvoidRealHome: test files never call os.UserHomeDir or name
// /Applications, so no test can reach the real app or its data.
func TestTestsAvoidRealHome(t *testing.T) {
	banned := []string{"os.UserHomeDir", "/Applications"}
	files := textFiles(t, func(rel string) bool {
		return strings.HasSuffix(rel, "_test.go") && !strings.HasPrefix(rel, policyDir)
	})
	for _, f := range files {
		for _, b := range banned {
			if i := strings.Index(string(f.data), b); i >= 0 {
				if reason, ok := realPathTestAllowlist[f.rel]; ok {
					t.Logf("%s:%d names %s (allow-listed: %s)", f.rel, lineOf(f.data, i), b, reason)
					continue
				}
				t.Errorf("%s:%d names %s", f.rel, lineOf(f.data, i), b)
			}
		}
	}
}

// denyListFile returns the personal-label deny-list: the file named by
// PARITYDESK_PII_DENYLIST_FILE, else the git-ignored
// testdata/private/pii.txt. It is never part of the repository.
func denyListFile(t *testing.T) string {
	if p := os.Getenv("PARITYDESK_PII_DENYLIST_FILE"); p != "" {
		return p
	}
	return filepath.Join(repoRoot(t), "testdata", "private", "pii.txt")
}

// tokens splits lower-cased text into letter and digit runs, and splits
// a run where it changes between a Japanese letter (japaneseLetter) and
// any other letter or digit: Japanese prose attaches Latin words to
// particles without a space (the label directly followed or preceded
// by kana), and the label must still be a token of its own. Text without Japanese letters
// splits exactly as before.
func tokens(s string) []string {
	var out []string
	var cur []rune
	curJA := false
	for _, r := range strings.ToLower(s) {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			if len(cur) > 0 {
				out, cur = append(out, string(cur)), cur[:0]
			}
			continue
		}
		ja := japaneseLetter(r)
		if len(cur) > 0 && ja != curJA {
			out, cur = append(out, string(cur)), cur[:0]
		}
		cur, curJA = append(cur, r), ja
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

func hashTokens(ts []string) string {
	h := sha256.Sum256([]byte(strings.Join(ts, " ")))
	return hex.EncodeToString(h[:])
}

// labelMatcher holds deny-listed labels as sha256 hashes of their
// lower-cased token runs, keyed by token count.
type labelMatcher map[int]map[string]bool

// newLabelMatcher hashes the deny-list lines (blank lines and # comments
// are skipped).
func newLabelMatcher(lines []string) labelMatcher {
	m := labelMatcher{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ts := tokens(line)
		if len(ts) == 0 {
			continue
		}
		if m[len(ts)] == nil {
			m[len(ts)] = map[string]bool{}
		}
		m[len(ts)][hashTokens(ts)] = true
	}
	return m
}

// matches reports whether text holds a deny-listed label as a token run.
func (m labelMatcher) matches(text string) bool {
	ts := tokens(text)
	for k, hashes := range m {
		for i := 0; i+k <= len(ts); i++ {
			if hashes[hashTokens(ts[i:i+k])] {
				return true
			}
		}
	}
	return false
}

// canonicalJA canonicalizes a line for the Japanese check: NFKC, lower
// case, every space (U+3000 included) removed.
func canonicalJA(s string) string {
	s = strings.ToLower(norm.NFKC.String(s))
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// jaMatcher holds the deny-list lines with a non-ASCII letter as sha256
// hashes of their canonical form, keyed by rune length, so a name inside
// Japanese prose (one token run, no spaces) is found as a substring.
type jaMatcher map[int]map[string]bool

func newJAMatcher(lines []string) jaMatcher {
	m := jaMatcher{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.ContainsFunc(line, func(r rune) bool {
			return r > unicode.MaxASCII && unicode.IsLetter(r)
		}) {
			continue
		}
		c := []rune(canonicalJA(line))
		if len(c) == 0 {
			continue
		}
		if m[len(c)] == nil {
			m[len(c)] = map[string]bool{}
		}
		h := sha256.Sum256([]byte(string(c)))
		m[len(c)][hex.EncodeToString(h[:])] = true
	}
	return m
}

// matches reports whether any window of text equals a deny-listed line.
func (m jaMatcher) matches(text string) bool {
	c := []rune(canonicalJA(text))
	for n, hashes := range m {
		for i := 0; i+n <= len(c); i++ {
			h := sha256.Sum256([]byte(string(c[i : i+n])))
			if hashes[hex.EncodeToString(h[:])] {
				return true
			}
		}
	}
	return false
}

func TestLabelMatcher(t *testing.T) {
	// A synthetic Japanese entry (not a name of anyone): "sample label".
	jm := newJAMatcher([]string{"\u898b\u672c\u30e9\u30d9\u30eb"})
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"\u3053\u308c\u306f\u898b\u672c\u30e9\u30d9\u30eb\u3067\u3059", true},
		{"\u898b\u672c\u3000\u30e9\u30d9\u30eb", true},
		{"\u898b\u672c\u30e9\u30a4\u30f3", false},
	} {
		if got := jm.matches(tc.text); got != tc.want {
			t.Errorf("Japanese matches(%q) = %v", tc.text, got)
		}
	}
	m := newLabelMatcher([]string{"# comment", "", "Alpha", "bravo charlie"})
	tests := []struct {
		text string
		want bool
	}{
		{"profile alpha here", true},
		{"ALPHA", true},
		{"path/to/alpha-x/file", true},
		{"alphabet", false},
		{"bravo charlie", true},
		{"Bravo_Charlie", true},
		{"bravo delta charlie", false},
		{"charlie bravo", false},
		// An ASCII label glued to Japanese text on either side or both
		// ("<label>no account", "kore wa <label> desu", "<label> to
		// <label>" without spaces), as escapes.
		{"alpha\u306e\u30a2\u30ab\u30a6\u30f3\u30c8", true},
		{"\u3053\u308c\u306falpha\u3067\u3059", true},
		{"\u3053\u308c\u306fAlpha\u3067\u3059", true},
		{"\u3053\u308c\u306fbravo charlie\u3067\u3059", true},
		{"bravo\u3068charlie", false},
		{"\u3053\u308c\u306falphabet\u3067\u3059", false},
		{"\u6f22\u5b57alpha2\u3067\u3059", false},
	}
	for _, tc := range tests {
		if got := m.matches(tc.text); got != tc.want {
			t.Errorf("matches(%q) = %v", tc.text, got)
		}
	}
}

// TestNoPersonalLabels compares every token run of every file (and file
// path) with the deny-list as sha256 hashes, so neither the list nor a
// match is ever printed. Skipped when no deny-list is present.
func TestNoPersonalLabels(t *testing.T) {
	b, err := os.ReadFile(denyListFile(t))
	if os.IsNotExist(err) {
		t.Skip("no personal deny-list (set PARITYDESK_PII_DENYLIST_FILE)")
	}
	if err != nil {
		t.Fatalf("read deny-list: %v", err)
	}
	m := newLabelMatcher(strings.Split(string(b), "\n"))
	if len(m) == 0 && len(newJAMatcher(strings.Split(string(b), "\n"))) == 0 {
		t.Skip("empty deny-list")
	}
	for _, rel := range trackedFiles(t) {
		if m.matches(rel) {
			t.Errorf("the path %s holds a deny-listed personal label", rel)
		}
	}
	jm := newJAMatcher(strings.Split(string(b), "\n"))
	for _, tf := range textFiles(t, nil) {
		ja := japaneseAllowed(tf.rel)
		for i, line := range strings.Split(string(tf.data), "\n") {
			if m.matches(line) || (ja && jm.matches(line)) {
				t.Errorf("%s:%d holds a deny-listed personal label", tf.rel, i+1)
			}
		}
	}
}
