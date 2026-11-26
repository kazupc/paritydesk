package artifacts

import (
	"bytes"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// The legacy import reads the artifact archive of an earlier folder-swap
// script (formats 1 and 2, section 10). Every byte of it is untrusted;
// its labels (personal names) are never decoded.

// ErrLegacyFormat: the manifest is not a format this version reads.
var ErrLegacyFormat = errors.New("artifacts: not a legacy archive manifest of format 1 or 2")

// MaxLegacyManifest bounds the legacy manifest.json.
const MaxLegacyManifest = 16 << 20

// LegacyEntry is one normalised legacy artifact.
type LegacyEntry struct {
	Title         string
	SHA256        string
	Path          string
	SavedAt       time.Time
	SourceAccount string
	SourceVersion string
	ChangedAt     time.Time
	TimesUnknown  bool
	Skipped       bool
	OtherFiles    bool
	Copies        []LegacyCopy
}

// LegacyCopy is one account's copy of a legacy artifact.
type LegacyCopy struct {
	Account       string
	Slug          string
	URL           string
	SyncedVersion string
	SyncedSHA     string
}

// legacy JSON shapes: only the keys paritydesk uses (no labels).
type legacyV2 struct {
	Schema    json.Number                `json:"schema"`
	Artifacts map[string]json.RawMessage `json:"artifacts"`
}

type legacyV2Entry struct {
	Title   string `json:"title"`
	Content struct {
		SHA256        string `json:"sha256"`
		Path          string `json:"path"`
		SavedAt       string `json:"saved_at"`
		SourceAccount string `json:"source_account"`
		SourceVersion string `json:"source_version"`
		ChangedAt     string `json:"changed_at"`
		OtherFiles    bool   `json:"other_files_not_saved"`
	} `json:"content"`
	Copies map[string]struct {
		Slug          string `json:"slug"`
		URL           string `json:"url"`
		SyncedVersion string `json:"synced_version"`
		SyncedSHA     string `json:"synced_sha"`
	} `json:"copies"`
	Skipped json.RawMessage `json:"skipped"`
}

type legacyV1Entry struct {
	Owner      string `json:"owner"`
	Title      string `json:"title"`
	SHA256     string `json:"sha256"`
	Path       string `json:"path"`
	SavedAt    string `json:"saved_at"`
	Version    string `json:"version"`
	URL        string `json:"url"`
	Slug       string `json:"slug"`
	OtherFiles bool   `json:"other_files_not_saved"`
	Mirrors    map[string]struct {
		Slug        string `json:"slug"`
		URL         string `json:"url"`
		FromVersion string `json:"from_version"`
	} `json:"mirrors"`
	Skipped json.RawMessage `json:"skipped"`
}

// legacyTime parses a time with a zone; a time without one is unknown.
func legacyTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700", "2006-01-02T15:04:05-0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// skippedSet reports a present, non-empty skipped value.
func skippedSet(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` || string(raw) == "false" {
		return false
	}
	return true
}

// DecodeLegacy parses a legacy manifest (pure).
func DecodeLegacy(b []byte) (LegacyManifest, error) {
	if len(b) > MaxLegacyManifest || !depthOK(b) {
		return LegacyManifest{}, ErrLegacyFormat
	}
	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil || top == nil {
		return LegacyManifest{}, ErrLegacyFormat
	}
	schema := 1
	if raw, ok := top["schema"]; ok {
		var n json.Number
		if json.Unmarshal(raw, &n) != nil {
			return LegacyManifest{}, ErrLegacyFormat
		}
		switch n.String() {
		case "1":
		case "2":
			schema = 2
		default:
			return LegacyManifest{}, ErrLegacyFormat
		}
	}
	entries := map[string]json.RawMessage{}
	if raw, ok := top["artifacts"]; ok {
		if json.Unmarshal(raw, &entries) != nil {
			return LegacyManifest{}, ErrLegacyFormat
		}
	} else if schema == 1 {
		for k, v := range top {
			if k != "schema" && len(bytes.TrimSpace(v)) > 0 && bytes.TrimSpace(v)[0] == '{' {
				entries[k] = v
			}
		}
	} else {
		return LegacyManifest{}, ErrLegacyFormat
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lm := LegacyManifest{Format: schema}
	for _, k := range keys {
		var e LegacyEntry
		var ok bool
		if schema == 2 {
			e, ok = decodeV2(entries[k])
		} else {
			e, ok = decodeV1(k, entries[k])
		}
		if ok {
			lm.Entries = append(lm.Entries, e)
		}
	}
	return lm, nil
}

func decodeV2(raw json.RawMessage) (LegacyEntry, bool) {
	var v legacyV2Entry
	if json.Unmarshal(raw, &v) != nil {
		return LegacyEntry{}, false
	}
	e := LegacyEntry{Title: v.Title, SHA256: v.Content.SHA256, Path: v.Content.Path, SourceAccount: v.Content.SourceAccount,
		SourceVersion: v.Content.SourceVersion, OtherFiles: v.Content.OtherFiles, Skipped: skippedSet(v.Skipped)}
	var known bool
	if e.SavedAt, known = legacyTime(v.Content.SavedAt); !known && v.Content.SavedAt != "" {
		e.TimesUnknown = true
	}
	if e.ChangedAt, known = legacyTime(v.Content.ChangedAt); !known {
		e.TimesUnknown = true
	}
	accts := make([]string, 0, len(v.Copies))
	for a := range v.Copies {
		accts = append(accts, a)
	}
	sort.Strings(accts)
	for _, a := range accts {
		c := v.Copies[a]
		e.Copies = append(e.Copies, LegacyCopy{Account: a, Slug: c.Slug, URL: c.URL, SyncedVersion: c.SyncedVersion, SyncedSHA: c.SyncedSHA})
	}
	return e, true
}

// v1 timestamps carry the version's time (L3).
var legacyVersionTime = regexp.MustCompile(`^([0-9]{9,11})-`)

func decodeV1(key string, raw json.RawMessage) (LegacyEntry, bool) {
	var v legacyV1Entry
	if json.Unmarshal(raw, &v) != nil {
		return LegacyEntry{}, false
	}
	e := LegacyEntry{Title: v.Title, SHA256: v.SHA256, Path: v.Path, SourceAccount: v.Owner, SourceVersion: v.Version,
		OtherFiles: v.OtherFiles, Skipped: skippedSet(v.Skipped)}
	var known bool
	if e.SavedAt, known = legacyTime(v.SavedAt); !known && v.SavedAt != "" {
		e.TimesUnknown = true
	}
	e.ChangedAt = versionTime(VersionID(v.Version), compat.ArtifactsSpec{VersionTimePrefix: legacyVersionTime.String()})
	if e.ChangedAt.IsZero() {
		e.TimesUnknown = true
	}
	slug := v.Slug
	if slug == "" && ids.IsUUID(key) {
		slug = key
	}
	if v.Owner != "" {
		e.Copies = append(e.Copies, LegacyCopy{Account: v.Owner, Slug: slug, URL: v.URL, SyncedVersion: v.Version, SyncedSHA: v.SHA256})
	}
	accts := make([]string, 0, len(v.Mirrors))
	for a := range v.Mirrors {
		accts = append(accts, a)
	}
	sort.Strings(accts)
	for _, a := range accts {
		mr := v.Mirrors[a]
		c := LegacyCopy{Account: a, Slug: mr.Slug, URL: mr.URL}
		if mr.FromVersion != "" && mr.FromVersion == v.Version {
			c.SyncedSHA = v.SHA256
		}
		e.Copies = append(e.Copies, c)
	}
	return e, true
}

// ImportReport is the JSON of `artifacts import-legacy` (10.4).
type ImportReport struct {
	V            int          `json:"-"`
	State        string       `json:"state,omitempty"`
	Applied      bool         `json:"applied"`
	Format       int          `json:"format"`
	LegacySHA256 string       `json:"legacy_sha256"`
	Pairs        ImportPairs  `json:"pairs"`
	Copies       ImportCopies `json:"copies"`
	Unmapped     []string     `json:"unmapped_accounts"`
	History      int          `json:"history"`
	Lines        []i18n.Text  `json:"lines"`
}

// ImportPairs counts the legacy artifacts.
type ImportPairs struct {
	Found          int `json:"found"`
	Importable     int `json:"importable"`
	Exists         int `json:"exists"`
	WithoutContent int `json:"without_content"`
	LegacyWrapped  int `json:"legacy_wrapped"`
	TimesUnknown   int `json:"times_unknown"`
}

// ImportCopies counts the legacy copies.
type ImportCopies struct {
	Mapped                int `json:"mapped"`
	DroppedUnmapped       int `json:"dropped_unmapped"`
	DroppedReplicaUnknown int `json:"dropped_replica_unknown"`
	DroppedSlugUnknown    int `json:"dropped_slug_unknown"`
}

// LegacyFiles reads below the legacy folder (legacy_read.go).
type LegacyFiles interface {
	Read(rel string, max int64) ([]byte, error)
	// History lists the history page names of pages/<name>/history.
	History(page string) []string
}

var (
	v2PathRE    = regexp.MustCompile(`^pages/[A-Za-z0-9._-]{1,128}/index\.html$`)
	v1PathRE    = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,256}\.html$`)
	wrappedRE   = regexp.MustCompile(`^\s*<(untrusted-document-[0-9a-f]{8,64})>[\s\S]*</([a-z0-9-]+)>\s*$`)
	historyName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}\.html$`)
)

// legacyWrapped reports a page the earlier tool may have saved wrapped
// (M8, L6).
func legacyWrapped(b []byte) bool {
	s := strings.TrimPrefix(string(b), "\xef\xbb\xbf")
	if strings.HasPrefix(strings.TrimLeft(s, " \t\r\n"), "<untrusted-document-") {
		return true
	}
	m := wrappedRE.FindStringSubmatch(s)
	return m != nil && m[1] == m[2]
}

// legacyPathOK checks a recorded page path.
func legacyPathOK(p string, format int) bool {
	if p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.Contains(p, "..") || strings.ContainsRune(p, 0) {
		return false
	}
	if format == 2 {
		return v2PathRE.MatchString(p)
	}
	return v1PathRE.MatchString(p)
}

// importPlan is what an import would write. blobs maps each page's sha
// to its path below the legacy folder: the bytes are not kept (a large
// old archive would otherwise be held in memory, also by a dry run);
// --apply reads and hashes each file again.
type importPlan struct {
	report ImportReport
	pairs  map[PairID]*Pair
	blobs  map[string]string
}

// planImport maps and validates a legacy manifest against the archive
// and the profiles' current replicas (10.3). It is pure but for reading
// the legacy files through files.
func planImport(lm LegacyManifest, files LegacyFiles, m *Manifest, current map[ids.Alias]model.Replica,
	mapping map[ids.AccountID]ids.Alias, historyKeep int, spec compat.ArtifactsSpec, now time.Time) importPlan {
	pl := importPlan{pairs: map[PairID]*Pair{}, blobs: map[string]string{}}
	r := &pl.report
	r.V, r.Format, r.Unmapped, r.Lines = 1, lm.Format, []string{}, []i18n.Text{}
	claimed := m.claimed()
	unmapped := map[string]bool{}
	byAccount := map[ids.AccountID][]ids.Alias{}
	for a, rep := range current {
		byAccount[rep.Account] = append(byAccount[rep.Account], a)
	}
	maxContent := spec.MaxContentBytes
	if maxContent <= 0 {
		maxContent = 16 << 20
	}
	for _, e := range lm.Entries {
		r.Pairs.Found++
		if e.TimesUnknown {
			r.Pairs.TimesUnknown++
		}
		copies := map[ids.Alias]*Copy{}
		for _, lc := range e.Copies {
			acct := ids.AccountID(lc.Account)
			if !ids.IsUUID(lc.Account) {
				r.Copies.DroppedUnmapped++
				continue
			}
			alias, ok := mapping[acct]
			if !ok {
				if as := byAccount[acct]; len(as) == 1 {
					alias, ok = as[0], true
				}
			}
			if !ok {
				r.Copies.DroppedUnmapped++
				unmapped[ids.Mask(lc.Account)] = true
				continue
			}
			rep, has := current[alias]
			if !has || rep.Account != acct {
				r.Copies.DroppedReplicaUnknown++
				continue
			}
			slug := strings.ToLower(lc.Slug)
			if !ValidSlug(slug) {
				slug = ""
				if id, isUUID, ok := URLID(lc.URL); ok && isUUID {
					slug = id
				}
			}
			if slug == "" {
				r.Copies.DroppedSlugUnknown++
				continue
			}
			c := &Copy{Replica: rep, Slug: Slug(slug), State: StateUnverified, Origin: OriginImported}
			if ValidURL(lc.URL) {
				c.URL = lc.URL
			}
			if versionValid(VersionID(lc.SyncedVersion), spec) {
				c.SyncedVersion = VersionID(lc.SyncedVersion)
			}
			if ValidSHA(lc.SyncedSHA) {
				c.SyncedSHA = lc.SyncedSHA
			}
			if _, dup := copies[alias]; dup {
				r.Copies.DroppedReplicaUnknown++
				continue
			}
			copies[alias] = c
			r.Copies.Mapped++
		}
		if len(copies) == 0 {
			continue
		}
		var slugs []string
		exists := false
		for _, c := range copies {
			slugs = append(slugs, string(c.Slug))
			if _, ok := claimed[c.Slug]; ok {
				exists = true
			}
		}
		if exists {
			r.Pairs.Exists++
			continue
		}
		sort.Strings(slugs)
		id := PairIDFor(Slug(slugs[0]))
		if _, taken := m.Pairs[id]; taken {
			r.Pairs.Exists++
			continue
		}
		if _, taken := pl.pairs[id]; taken {
			r.Pairs.Exists++
			continue
		}
		title := ""
		if ValidTitle(e.Title) {
			title = e.Title
		}
		p := &Pair{Title: title, TitleSHA: TitleKey(e.Title, ""), CreatedAt: now, Copies: copies,
			Flags: Flags{Capabilities: CapFlags{State: CapNone}, AllowCreate: []ids.Alias{}, Restore: []ids.Alias{}}}
		if e.Skipped {
			p.Flags.Skipped = "imported"
		}
		if e.OtherFiles {
			p.Flags.MultiFile = true
		}
		withContent := false
		if ValidSHA(e.SHA256) && legacyPathOK(e.Path, lm.Format) {
			b, err := files.Read(e.Path, maxContent)
			switch {
			case err != nil || b == nil || shaHex(b) != e.SHA256:
			case legacyWrapped(b):
				r.Pairs.LegacyWrapped++
			default:
				withContent = true
				pl.blobs[e.SHA256] = e.Path
				src := ids.Alias("")
				for a, c := range copies {
					if string(c.Replica.Account) == e.SourceAccount {
						src = a
					}
				}
				if src == "" {
					src = sortedAliases(copies)[0]
				}
				p.Content = &Content{SHA256: e.SHA256, Bytes: int64(len(b)), Lines: lineCount(string(b)), SavedAt: e.SavedAt,
					SourceProfile: src, ChangedAt: e.ChangedAt}
				if versionValid(VersionID(e.SourceVersion), spec) {
					p.Content.SourceVersion = VersionID(e.SourceVersion)
				}
				if p.Content.SavedAt.IsZero() {
					p.Content.SavedAt = now
				}
			}
		}
		if !withContent {
			r.Pairs.WithoutContent++
		}
		if withContent && lm.Format == 2 {
			page := path.Dir(e.Path)
			names := files.History(page)
			sort.Sort(sort.Reverse(sort.StringSlice(names)))
			for _, n := range names {
				if len(p.History) >= historyKeep {
					break
				}
				if !historyName.MatchString(n) {
					continue
				}
				b, err := files.Read(page+"/history/"+n, maxContent)
				if err != nil || b == nil || legacyWrapped(b) {
					continue
				}
				sha := shaHex(b)
				pl.blobs[sha] = page + "/history/" + n
				p.History = append([]HistoryEntry{{SHA256: sha, SavedAt: now, SourceProfile: p.Content.SourceProfile,
					Reason: HistImported}}, p.History...)
				r.History++
			}
		}
		pl.pairs[id] = p
		r.Pairs.Importable++
	}
	for u := range unmapped {
		r.Unmapped = append(r.Unmapped, u)
	}
	sort.Strings(r.Unmapped)
	return pl
}
