package artifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// ManifestVersion is the archive manifest's format version.
const ManifestVersion = 3

// MaxIgnored bounds Manifest.Ignored (oldest dropped).
const MaxIgnored = 5000

// Size limits of archive files (4.1).
const (
	MaxManifestBytes  = 16 << 20
	MaxInventoryBytes = 8 << 20
	MaxChainBytes     = 1 << 20
	MaxDueBytes       = 4 << 10
	MaxPartialBytes   = 8 << 20
)

var (
	shaRE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pairIDRE = regexp.MustCompile(`^[0-9a-f]{16}$`)
	iconRE   = regexp.MustCompile(`^[a-z]{1,24}$`)
	// urlPathRE is the app's artifact page path (B5): an optional vanity
	// prefix and a uuid or a base58 id.
	urlRE = regexp.MustCompile(`^https://([a-z0-9.-]{1,253})(/(?:code/(?:artifact|frame)|artifact)/(?:[A-Za-z0-9_-]*-)?([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[1-9A-HJ-NP-Za-km-z]{22}))$`)
	// defaultVersionRE is artifacts.version_pattern of the embedded
	// manifest; the knobs carry the manifest's own.
	defaultVersionRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// allowedHosts are the hosts an artifact URL may name.
var allowedHosts = []string{"claude.ai"}

// ErrManifestInvalid marks a manifest that fails Validate.
var ErrManifestInvalid = errors.New("artifacts: manifest is not valid")

// ValidSHA reports a lower-case hex sha256.
func ValidSHA(s string) bool { return shaRE.MatchString(s) }

// ValidPairID reports a pair id.
func ValidPairID(s string) bool { return pairIDRE.MatchString(s) }

// ValidSlug reports a lower-case UUID.
func ValidSlug(s string) bool { return ids.IsUUID(s) }

// ValidIcon reports an icon name.
func ValidIcon(s string) bool { return iconRE.MatchString(s) }

// URLID returns the id in an artifact URL (B5): a lower-cased UUID, or a
// base58 id; ok is false for any other URL.
func URLID(raw string) (id string, isUUID bool, ok bool) {
	if len(raw) > 2048 {
		return "", false, false
	}
	m := urlRE.FindStringSubmatch(raw)
	if m == nil || !slices.Contains(allowedHosts, m[1]) {
		return "", false, false
	}
	id = m[3]
	if len(id) == 36 {
		return strings.ToLower(id), true, true
	}
	return id, false, true
}

// ValidURL reports a B5-valid artifact URL.
func ValidURL(s string) bool {
	_, _, ok := URLID(s)
	return ok
}

// PairIDFor is the pair id of a pair first recorded from origin.
func PairIDFor(origin Slug) PairID {
	h := sha256.Sum256([]byte("paritydesk-artifact-pair\x00" + string(origin)))
	return PairID(hex.EncodeToString(h[:])[:16])
}

// NewManifest returns an empty v3 manifest.
func NewManifest() *Manifest {
	return &Manifest{V: ManifestVersion, Gate: Gate{Roundtrip: GateUnverified}, Profiles: map[ids.Alias]*ProfileRecord{},
		Pairs: map[PairID]*Pair{}}
}

// ensureMaps makes the manifest's maps non-nil.
func (m *Manifest) ensureMaps() {
	if m.Profiles == nil {
		m.Profiles = map[ids.Alias]*ProfileRecord{}
	}
	if m.Pairs == nil {
		m.Pairs = map[PairID]*Pair{}
	}
	for _, p := range m.Pairs {
		if p.Copies == nil {
			p.Copies = map[ids.Alias]*Copy{}
		}
	}
}

// Clone returns a deep copy.
func (m *Manifest) Clone() *Manifest {
	if m == nil {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(fmt.Sprintf("artifacts: clone manifest: %v", err))
	}
	var c Manifest
	if err := json.Unmarshal(b, &c); err != nil {
		panic(fmt.Sprintf("artifacts: clone manifest: %v", err))
	}
	c.ensureMaps()
	return &c
}

// Decode parses manifest.json (no validation).
func Decode(b []byte) (*Manifest, error) {
	if len(b) > MaxManifestBytes {
		return nil, fmt.Errorf("%w: too large", ErrManifestInvalid)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifestInvalid, err)
	}
	m.ensureMaps()
	return &m, nil
}

// Encode returns the manifest's JSON (field order fixed by the types).
func Encode(m *Manifest) ([]byte, error) {
	b, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// enum sets of the manifest.
var (
	copyStates  = []string{StateLive, StateDeleted, StateCreatePending, StateUnverified}
	copyOrigins = []string{OriginCreated, OriginRecorded, OriginImported}
	readModes   = []string{"", ReadOwner, ReadUsers, ReadOrg, ReadPublic}
	gateValues  = []string{GateUnverified, GateExact, GateMismatch}
	histReasons = []string{HistReplaced, HistConflict, HistReadback, HistImported}
	skipValues  = []string{"", "user", "imported"}
	capStates   = []string{"", CapNone, CapDeclared, CapUnknown}
	roundtrips  = []string{"", ClassExact, ClassTitleOnly, "mismatch:" + ClassWrapped, "mismatch:" + ClassOther}
	ignoredWhys = []string{"multi_file", "type_made", "live_doc", "capabilities", "co_authored", "app_refused", "unrecognized", "too_large", "not_owned"}
	capNameRE   = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
)

// versionValid checks a version id against the manifest pattern (the
// default one when the spec has none).
func versionValid(v VersionID, spec compat.ArtifactsSpec) bool {
	re := defaultVersionRE
	if spec.VersionPattern != "" {
		if r, err := regexp.Compile(spec.VersionPattern); err == nil {
			re = r
		}
	}
	return len(v) <= 128 && re.MatchString(string(v))
}

// Validate checks a manifest (4.2). historyKeep is config
// artifacts.history_keep.
func Validate(m *Manifest, spec compat.ArtifactsSpec, historyKeep int) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrManifestInvalid}, args...)...)
	}
	if m == nil || m.V != ManifestVersion {
		return bad("version")
	}
	if !slices.Contains(gateValues, m.Gate.Roundtrip) {
		return bad("gate")
	}
	if !utcOrZero(m.UpdatedAt) || !utcOrZero(m.Gate.At) {
		return bad("times")
	}
	for a, pr := range m.Profiles {
		if _, err := ids.ParseAlias(string(a)); err != nil || pr == nil || !validReplica(pr.Replica) {
			return bad("profile %q", a)
		}
	}
	slugOwner := map[Slug]PairID{}
	check := func(id PairID, p *Pair, forgotten bool) error {
		if !ValidPairID(string(id)) || p == nil {
			return bad("pair id")
		}
		if p.Title != "" && !ValidTitle(p.Title) {
			return bad("pair %s: title", id)
		}
		if p.TitleSHA != "" && !ValidSHA(p.TitleSHA) {
			return bad("pair %s: title key", id)
		}
		if !utcOrZero(p.CreatedAt) || !utcOrZero(p.TitleChangedAt) {
			return bad("pair %s: times", id)
		}
		if c := p.Content; c != nil {
			if !ValidSHA(c.SHA256) || c.Bytes < 0 || c.Lines < 0 || (c.SourceVersion != "" && !versionValid(c.SourceVersion, spec)) ||
				(c.Icon != "" && !ValidIcon(c.Icon)) || !utcOrZero(c.SavedAt) || !utcOrZero(c.ChangedAt) {
				return bad("pair %s: content", id)
			}
			if c.SourceProfile != "" {
				if _, err := ids.ParseAlias(string(c.SourceProfile)); err != nil {
					return bad("pair %s: content source", id)
				}
			}
		}
		if len(p.History) > historyKeep+1 {
			return bad("pair %s: history overflow", id)
		}
		for _, h := range p.History {
			if !ValidSHA(h.SHA256) || !slices.Contains(histReasons, h.Reason) || (h.Title != "" && !ValidTitle(h.Title)) ||
				(h.SourceVersion != "" && !versionValid(h.SourceVersion, spec)) || !utcOrZero(h.SavedAt) {
				return bad("pair %s: history", id)
			}
		}
		f := p.Flags
		if !slices.Contains(skipValues, f.Skipped) || !slices.Contains(capStates, f.Capabilities.State) {
			return bad("pair %s: flags", id)
		}
		for _, d := range []string{f.Capabilities.Digest, f.Capabilities.AllowedDigest} {
			if d != "" && !ValidSHA(d) {
				return bad("pair %s: capability digest", id)
			}
		}
		for _, n := range f.Capabilities.Names {
			if !capNameRE.MatchString(n) {
				return bad("pair %s: capability name", id)
			}
		}
		for _, a := range append(append(append([]ids.Alias(nil), f.AllowCreate...), f.Restore...), f.TitleTaken...) {
			if _, err := ids.ParseAlias(string(a)); err != nil {
				return bad("pair %s: alias list", id)
			}
		}
		for a, c := range p.Copies {
			if _, err := ids.ParseAlias(string(a)); err != nil || c == nil {
				return bad("pair %s: copy alias", id)
			}
			// A pending create has no slug until adoption finds it.
			slugOK := ValidSlug(string(c.Slug)) || (c.Slug == "" && c.State == StateCreatePending)
			if !slugOK || !validReplica(c.Replica) || !slices.Contains(copyStates, c.State) ||
				!slices.Contains(copyOrigins, c.Origin) || !slices.Contains(readModes, c.Read) ||
				(c.URL != "" && !ValidURL(c.URL)) || (c.SyncedSHA != "" && !ValidSHA(c.SyncedSHA)) ||
				(c.ServedSHA != "" && !ValidSHA(c.ServedSHA)) || (c.SeenTitleSHA != "" && !ValidSHA(c.SeenTitleSHA)) ||
				(c.SyncedVersion != "" && !versionValid(c.SyncedVersion, spec)) ||
				(c.SeenVersion != "" && !versionValid(c.SeenVersion, spec)) || !slices.Contains(roundtrips, c.Roundtrip) ||
				!utcOrZero(c.SeenAt) || !utcOrZero(c.DeletedAt) {
				return bad("pair %s: copy %s", id, a)
			}
			if ca := c.CreateAttempt; ca != nil && ((ca.TitleSHA != "" && !ValidSHA(ca.TitleSHA)) || !ValidSHA(ca.ContentSHA) || !utcOrZero(ca.At)) {
				return bad("pair %s: create attempt", id)
			}
			if forgotten || c.Slug == "" {
				continue
			}
			if other, ok := slugOwner[c.Slug]; ok {
				return bad("slug in pairs %s and %s", other, id)
			}
			slugOwner[c.Slug] = id
		}
		return nil
	}
	for _, id := range sortedPairIDs(m.Pairs) {
		if err := check(id, m.Pairs[id], false); err != nil {
			return err
		}
	}
	for _, id := range sortedPairIDs(m.Forgotten) {
		if err := check(id, m.Forgotten[id], true); err != nil {
			return err
		}
	}
	if m.PublishRefused != "" && !stickyRefusal(m.PublishRefused) {
		return bad("publish_refused")
	}
	if len(m.Ignored) > MaxIgnored {
		return bad("ignored overflow")
	}
	for s, it := range m.Ignored {
		if !ValidSlug(string(s)) || it == nil || !slices.Contains(ignoredWhys, it.Reason) || !utcOrZero(it.At) ||
			(it.Version != "" && !versionValid(it.Version, spec)) {
			return bad("ignored %s", s)
		}
		if _, err := ids.ParseAlias(string(it.Alias)); err != nil {
			return bad("ignored %s: alias", s)
		}
	}
	for _, im := range m.Imports {
		if !ValidSHA(im.LegacySHA256) || (im.Format != 1 && im.Format != 2) || im.Pairs < 0 {
			return bad("imports")
		}
	}
	return nil
}

// utcOrZero reports a zero time or a time in UTC.
func utcOrZero(t time.Time) bool { return t.IsZero() || t.Location() == time.UTC }

// validReplica reports a replica of two UUIDs.
func validReplica(r model.Replica) bool {
	return ids.IsUUID(string(r.Account)) && ids.IsUUID(string(r.Org))
}

// sortedPairIDs returns the keys of a pair map in order.
func sortedPairIDs(m map[PairID]*Pair) []PairID {
	out := make([]PairID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// sortedAliases returns the keys of a copy map in order.
func sortedAliases[V any](m map[ids.Alias]V) []ids.Alias {
	out := make([]ids.Alias, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// copyOwner returns the pair and alias a slug is a copy of.
func (m *Manifest) copyOwner(s Slug) (PairID, ids.Alias, bool) {
	for _, id := range sortedPairIDs(m.Pairs) {
		for _, a := range sortedAliases(m.Pairs[id].Copies) {
			if s != "" && m.Pairs[id].Copies[a].Slug == s {
				return id, a, true
			}
		}
	}
	return "", "", false
}

// claimed returns every copy slug of the manifest's pairs.
func (m *Manifest) claimed() map[Slug]PairID {
	out := map[Slug]PairID{}
	for id, p := range m.Pairs {
		for _, c := range p.Copies {
			if c.Slug != "" {
				out[c.Slug] = id
			}
		}
	}
	return out
}
