package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/redact"
	"github.com/kazupc/paritydesk/internal/render"
)

// maxText bounds every free-text value in the bundle, in runes.
const maxText = 300

// Placeholders that replace removed values.
const (
	unknownAlias  = "profile-?"
	unknownFolder = "Claude-<folder>"
	labelMark     = "<label>"
	emailMark     = "<email>"
	idMark        = "<id>"
	textMark      = `"<text>"`
)

var (
	emailRE  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	quotedRE = regexp.MustCompile("\"[^\"\n]*\"|`[^`\n]*`")
	// longHexRE matches hashes, device ids and UUIDs without hyphens.
	longHexRE = regexp.MustCompile(`\b[0-9A-Fa-f]{16,}\b`)
	// pathRE matches a token that contains a slash.
	pathRE = regexp.MustCompile(`[^\s"'<>|(),;\[\]{}` + "`" + `]*/[^\s"'<>|(),;\[\]{}` + "`" + `]*`)
	// hashedHomeRE matches what redact.Redactor leaves for a home path.
	hashedHomeRE = regexp.MustCompile(`^~(/[0-9a-f]{8})?$`)
	// wordRE matches the tokens that can be an alias or a folder name.
	wordRE = regexp.MustCompile(`[A-Za-z0-9._-]+`)
	// prefRE matches an app preference key name.
	prefRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	// runIDRE matches a run id (runner.ValidRunID).
	runIDRE = regexp.MustCompile(`^[0-9a-z-]{8,64}$`)
	// enumRE matches short fixed vocabulary: phases, statuses, modes.
	enumRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
)

// systemPrefixes are locations that never name a person or a project, so
// paths under them are kept as they are.
var systemPrefixes = []string{"/opt/homebrew/", "/usr/", "/bin/", "/sbin/", "/System/", "/Library/"}

// defaultDataRoot is config.Default's data root, which is kept as is.
const defaultDataRoot = "~/Library/Application Support"

// scrubber redacts text for the bundle.
type scrubber struct {
	red     redact.Redactor
	aliases map[string]string // alias -> profile-N
	folders map[string]string // parked folder name -> Claude-profile-N
	unknown map[string]bool   // aliases seen in run logs but not in the config
}

// newScrubber maps the config's aliases and parked folders to
// pseudonyms, in config order.
func newScrubber(home string, cfg *config.Config) *scrubber {
	s := &scrubber{red: redact.Redactor{Home: home}, aliases: map[string]string{}, folders: map[string]string{}, unknown: map[string]bool{}}
	if cfg == nil {
		return s
	}
	for i, p := range cfg.Profiles {
		name := "profile-" + strconv.Itoa(i+1)
		if p.Alias != "" {
			if _, dup := s.aliases[string(p.Alias)]; !dup {
				s.aliases[string(p.Alias)] = name
			}
		}
		if f := p.ParkedFolder(); f != "" {
			if _, dup := s.folders[f]; !dup {
				s.folders[f] = "Claude-" + name
			}
		}
	}
	return s
}

// alias returns the pseudonym of an alias: profile-N for a profile in the
// config, profile-? otherwise, and "" for "".
func (s *scrubber) alias(a string) string {
	if a == "" {
		return ""
	}
	if p, ok := s.aliases[a]; ok {
		return p
	}
	return unknownAlias
}

// text redacts free text. The order matters: emails and quoted text go
// first, before their content is split into tokens; redact.Redactor then
// hashes home paths (which may contain spaces) and masks UUIDs; any other
// token with a slash is hashed; aliases and folder names go last.
func (s *scrubber) text(in string) string {
	t := render.Sanitize(in, 0)
	t = emailRE.ReplaceAllString(t, emailMark)
	t = quotedRE.ReplaceAllString(t, textMark)
	t = s.red.String(t)
	t = longHexRE.ReplaceAllString(t, idMark)
	t = pathRE.ReplaceAllStringFunc(t, scrubPath)
	t = wordRE.ReplaceAllStringFunc(t, s.word)
	return render.Sanitize(t, maxText)
}

// scrubPath keeps hashed home paths and system locations and hashes any
// other token that contains a slash.
func scrubPath(tok string) string {
	core := strings.TrimRight(tok, ".:")
	tail := tok[len(core):]
	if core == "" || core == "/" || hashedHomeRE.MatchString(core) {
		return tok
	}
	if !strings.Contains(core, "..") {
		for _, p := range systemPrefixes {
			if strings.HasPrefix(core, p) {
				return tok
			}
		}
	}
	return "<path:" + shortHash(core) + ">" + tail
}

// word replaces a parked folder name or an alias.
func (s *scrubber) word(tok string) string {
	core := strings.TrimRight(tok, ".")
	tail := tok[len(core):]
	if strings.HasPrefix(core, "Claude-") {
		if p, ok := s.folders[core]; ok {
			return p + tail
		}
		return unknownFolder + tail
	}
	if p, ok := s.aliases[core]; ok {
		return p + tail
	}
	if s.unknown[core] {
		return unknownAlias + tail
	}
	return tok
}

// enum keeps a short fixed-vocabulary value and drops anything else.
func enum(v string) string {
	if enumRE.MatchString(v) {
		return v
	}
	return ""
}

// config returns a copy of cfg with everything personal replaced.
func (s *scrubber) config(cfg *config.Config) *config.Config {
	c := *cfg
	if c.App.DataRoot != defaultDataRoot {
		c.App.DataRoot = s.text(c.App.DataRoot)
	}
	c.App.BundleID = s.text(c.App.BundleID)
	c.Profiles = make([]config.Profile, len(cfg.Profiles))
	for i, p := range cfg.Profiles {
		q := config.Profile{Alias: ids.Alias(s.alias(string(p.Alias))), ManagedByOrg: p.ManagedByOrg}
		if p.Label != "" {
			q.Label = labelMark
		}
		if p.Folder != "" {
			q.Folder = s.folderName(p.Folder)
		}
		c.Profiles[i] = q
	}
	c.Sync.Tombstones = enum(cfg.Sync.Tombstones)
	c.Sync.Secrets = enum(cfg.Sync.Secrets)
	c.Sync.Exclude = numbered("<exclude-", len(cfg.Sync.Exclude))
	c.Sync.SecretsAllow = numbered("<secret-name-", len(cfg.Sync.SecretsAllow))
	c.Sync.AppPrefs = nil
	for _, k := range cfg.Sync.AppPrefs {
		if !prefRE.MatchString(k) {
			k = "<pref>"
		}
		c.Sync.AppPrefs = append(c.Sync.AppPrefs, k)
	}
	c.Sync.Export = s.aliasMap(cfg.Sync.Export)
	c.Sync.Import = s.aliasMap(cfg.Sync.Import)
	c.Backups.MaxSize = s.text(cfg.Backups.MaxSize)
	return &c
}

// folderName returns the pseudonym of a parked folder name.
func (s *scrubber) folderName(f string) string {
	if p, ok := s.folders[f]; ok {
		return p
	}
	return unknownFolder
}

// aliasMap re-keys an alias -> categories map by pseudonym. Category
// names are kept only when they are short fixed words.
func (s *scrubber) aliasMap(m map[string][]string) map[string][]string {
	if m == nil {
		return nil
	}
	out := map[string][]string{}
	for a, cats := range m {
		k := s.alias(a)
		for _, c := range cats {
			if c = enum(c); c != "" {
				out[k] = append(out[k], c)
			}
		}
		if _, ok := out[k]; !ok {
			out[k] = []string{}
		}
	}
	return out
}

// numbered returns n placeholders "<prefix1>" ... "<prefixn>".
func numbered(prefix string, n int) []string {
	if n == 0 {
		return nil
	}
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + strconv.Itoa(i+1) + ">"
	}
	return out
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}
