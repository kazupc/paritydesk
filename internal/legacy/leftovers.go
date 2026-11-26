package legacy

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
)

// LeftoverKind classifies a directory of another account inside a folder.
type LeftoverKind string

// Leftover kinds.
const (
	// KindOtherAccount: a directory of the account another scanned folder
	// holds.
	KindOtherAccount LeftoverKind = "other_account"
	// KindUnrecognized: an account directory no scanned folder holds (the
	// folder's install id, a 3P id, or an 8-hex short name). Only reported.
	KindUnrecognized LeftoverKind = "unrecognized"
	// KindMixedOrgStub: an empty directory of this folder's account under
	// another account's organization.
	KindMixedOrgStub LeftoverKind = "mixed_org_stub"
	// KindMixedOrg: the same, with data. Only reported.
	KindMixedOrg LeftoverKind = "mixed_org"
)

// Why ids: why a leftover is not eligible to be moved.
const (
	WhyUnrecognized   = "unrecognized"
	WhyShortID        = "short_id"
	WhyHoldsData      = "holds_data"
	WhyUniqueSessions = "unique_sessions"
	WhySymlink        = "symlink"
	WhyTooLarge       = "too_large"
	WhyScratch        = "scratch"
	WhyOrgUnknown     = "org_unknown"
	WhyOtherVolume    = "other_volume"
	WhyAppRunning     = "app_running"
	WhyInUse          = "in_use"
	WhyFolderChanged  = "folder_changed"
	WhyDenied         = "denied" // deny floor, a protected path inside, or a synced path: reported only
	// WhyMoveFailed: an earlier move out of this folder failed in this run,
	// so the later ones were not tried.
	WhyMoveFailed = "move_failed"
)

// DefaultMaxEntries caps the entries counted below one leftover.
const DefaultMaxEntries = 1_000_000

const builtinsSuffix = "-builtins"

var shortIDRE = regexp.MustCompile(`^[0-9a-f]{8}$`)

// OtherFolder is another scanned folder that holds an account.
type OtherFolder struct {
	Name  string    // validated folder name (for the unique-name check)
	Path  string    // absolute
	Owner ids.Alias // alias it has or will have; "" when skipped or excluded
}

// LeftoverInput is what FindLeftovers needs for one folder.
type LeftoverInput struct {
	Folder  string        // absolute data folder
	Account ids.AccountID // the folder's own account
	// OwnOrgs are the folder's own organizations (non-stub ones, plus the
	// active org hint for Claude).
	OwnOrgs []ids.OrgID
	// OrgKnown reports whether the folder's own organization is known (a
	// mixed_org_stub may be moved only then).
	OrgKnown bool
	// Others holds, for every account an OTHER scanned folder holds, the
	// folder that is that account's home: Claude when it holds the account,
	// else a folder this run keeps (adopt, finish, registered), never a
	// skipped duplicate when another holder exists. Unique names are
	// counted against it.
	Others map[ids.AccountID]OtherFolder
	// OtherOrgs are the own orgs of those folders, with the folder that
	// owns each (chosen the same way).
	OtherOrgs map[ids.OrgID]OtherFolder
	// Protected reports a data-folder-relative path paritydesk never moves
	// (the deny floor, never_touched, never_until_classified). A leftover
	// holding such a path anywhere below it is not eligible (why denied).
	// nil protects nothing.
	Protected func(rel string) bool
	// KnownIDs maps every known account and org id to an alias, to name
	// the likely owner of an 8-hex short directory.
	KnownIDs map[string]ids.Alias
	Dirs     []compat.LegacyDir
	Stub     StubRules
	// MaxEntries caps the entries counted below one leftover (0: default).
	MaxEntries int
}

// Leftover is one directory reported for a folder.
type Leftover struct {
	Rel         string // slash path relative to the folder
	Root        string // LegacyDir.ID
	Kind        LeftoverKind
	Owner       ids.Alias // "" when unknown
	OwnerFolder string    // folder holding that account, "" when none
	Files       int
	Bytes       int64
	Unique      int // names not present in the owner folder (-1: not checked)
	Eligible    bool
	Why         string    // constant id when not eligible
	Possibly    ids.Alias // short_id: the alias whose id starts with the name
}

// isUUID reports whether a directory name is a lower-case UUID.
func isUUID(s string) bool { return ids.IsUUID(s) }

// OwnOrgs splits the organization directories of acct under
// folder/root/<acct>/ into stubs and the rest. Symlinks and non-UUID names
// are ignored. A missing directory gives none.
func OwnOrgs(folder, root string, acct ids.AccountID, stub StubRules) (own, stubs []ids.OrgID, err error) {
	dir := filepath.Join(folder, filepath.FromSlash(root), string(acct))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 || !isUUID(e.Name()) {
			continue
		}
		ok, err := IsStub(filepath.Join(dir, e.Name()), stub)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			stubs = append(stubs, ids.OrgID(e.Name()))
		} else {
			own = append(own, ids.OrgID(e.Name()))
		}
	}
	return own, stubs, nil
}

// FindLeftovers walks only the directories the manifest names
// (legacy.scoped_dir), with lstat semantics, never following a symlink and
// never reading a file except stub candidates. It returns the leftovers
// sorted by path.
func FindLeftovers(in LeftoverInput) ([]Leftover, error) {
	if in.MaxEntries <= 0 {
		in.MaxEntries = DefaultMaxEntries
	}
	f := finder{in: in}
	for _, d := range in.Dirs {
		var err error
		switch d.Layout {
		case compat.LayoutAcctOrg:
			err = f.acctOrg(d)
		case compat.LayoutOrgAcct:
			err = f.orgAcct(d)
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(f.out, func(i, j int) bool { return f.out[i].Rel < f.out[j].Rel })
	return f.out, nil
}

type finder struct {
	in  LeftoverInput
	out []Leftover
}

func (f *finder) abs(rel string) string { return filepath.Join(f.in.Folder, filepath.FromSlash(rel)) }

// skipped reports a first-level name the walk ignores.
func (f *finder) skipped(d compat.LegacyDir, name string) bool {
	for _, n := range d.SkipNames {
		if n == name {
			return true
		}
	}
	return f.in.Stub.ignored(name)
}

// readDir lists a directory; a missing one, or anything that is not a
// directory (a symlink included), is empty.
func readDir(p string) ([]os.DirEntry, error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, nil
	}
	return os.ReadDir(p)
}

func (f *finder) ownOrg(o ids.OrgID) bool {
	for _, x := range f.in.OwnOrgs {
		if x == o {
			return true
		}
	}
	return false
}

// acctOrg handles a "{acct}/{org}" root.
func (f *finder) acctOrg(d compat.LegacyDir) error {
	entries, err := readDir(f.abs(d.Root))
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if f.skipped(d, name) {
			continue
		}
		rel := path.Join(d.Root, name)
		switch {
		case isUUID(name) && ids.AccountID(name) == f.in.Account:
			if err := f.mixedOrgs(d, rel, e); err != nil {
				return err
			}
		case isUUID(name):
			if err := f.account(d, rel, e, ids.AccountID(name)); err != nil {
				return err
			}
		case shortIDRE.MatchString(name):
			l := Leftover{Rel: rel, Root: d.ID, Kind: KindUnrecognized, Why: WhyShortID, Unique: -1, Possibly: f.shortOwner(name)}
			if err := f.add(l, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// account reports a first-level account directory other than the
// folder's own.
func (f *finder) account(d compat.LegacyDir, rel string, e os.DirEntry, acct ids.AccountID) error {
	other, held := f.in.Others[acct]
	l := Leftover{Rel: rel, Root: d.ID, Unique: -1}
	if !held || notDir(e) {
		// Not another folder's account, or a file (not a directory) with
		// an account's name: only reported (a symlink gets why symlink).
		l.Kind, l.Why = KindUnrecognized, WhyUnrecognized
		return f.add(l, e)
	}
	l.Kind, l.Owner, l.OwnerFolder = KindOtherAccount, other.Owner, other.Name
	l.Eligible = true
	if d.UniqueCheck && e.IsDir() {
		n, err := uniqueNames(f.abs(rel), filepath.Join(other.Path, filepath.FromSlash(rel)), f.in.Stub)
		if err != nil {
			return err
		}
		l.Unique = n
		if n > 0 {
			l.Eligible, l.Why = false, WhyUniqueSessions
		}
	}
	if !d.Quarantine && l.Why == "" {
		l.Eligible, l.Why = false, WhyScratch
	}
	return f.add(l, e)
}

// mixedOrgs reports this account's directories under orgs of other
// folders. Orgs the folder owns, and orgs nobody known owns, are not
// reported.
func (f *finder) mixedOrgs(d compat.LegacyDir, rel string, e os.DirEntry) error {
	if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
		return nil
	}
	orgs, err := readDir(f.abs(rel))
	if err != nil {
		return err
	}
	for _, o := range orgs {
		org := ids.OrgID(o.Name())
		owner, other := f.in.OtherOrgs[org]
		if !isUUID(o.Name()) || f.ownOrg(org) || !other {
			continue
		}
		orel := path.Join(rel, o.Name())
		l := Leftover{Rel: orel, Root: d.ID, Unique: -1, Owner: owner.Owner, OwnerFolder: owner.Name}
		stub := false
		if o.IsDir() && o.Type()&fs.ModeSymlink == 0 {
			if stub, err = IsStub(f.abs(orel), f.in.Stub); err != nil {
				return err
			}
		}
		if stub {
			l.Kind = KindMixedOrgStub
			l.Eligible = d.Quarantine && f.in.OrgKnown
			switch {
			case !d.Quarantine:
				l.Why = WhyScratch
			case !f.in.OrgKnown:
				l.Why = WhyOrgUnknown
			}
		} else {
			l.Kind, l.Why = KindMixedOrg, WhyHoldsData
		}
		if err := f.add(l, o); err != nil {
			return err
		}
	}
	return nil
}

// orgAcct handles a "{org}/{acct}" root: decided per second-level entry,
// never for a whole org directory.
func (f *finder) orgAcct(d compat.LegacyDir) error {
	entries, err := readDir(f.abs(d.Root))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if f.skipped(d, e.Name()) || !e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		orel := path.Join(d.Root, e.Name())
		subs, err := readDir(f.abs(orel))
		if err != nil {
			return err
		}
		for _, s := range subs {
			name := strings.TrimSuffix(s.Name(), builtinsSuffix)
			if !isUUID(name) || ids.AccountID(name) == f.in.Account || f.in.Stub.ignored(s.Name()) {
				continue
			}
			l := Leftover{Rel: path.Join(orel, s.Name()), Root: d.ID, Unique: -1}
			if other, held := f.in.Others[ids.AccountID(name)]; held && !notDir(s) {
				l.Kind, l.Owner, l.OwnerFolder = KindOtherAccount, other.Owner, other.Name
				l.Eligible = d.Quarantine
				if !d.Quarantine {
					l.Why = WhyScratch
				}
			} else {
				l.Kind, l.Why = KindUnrecognized, WhyUnrecognized
			}
			if err := f.add(l, s); err != nil {
				return err
			}
		}
	}
	return nil
}

// add counts a leftover's files and appends it. A symlink is never
// eligible and never walked; a tree past MaxEntries is not eligible.
func (f *finder) add(l Leftover, e os.DirEntry) error {
	if e.Type()&fs.ModeSymlink != 0 {
		l.Eligible, l.Why = false, WhySymlink
		f.out = append(f.out, l)
		return nil
	}
	files, bytes, over, protected, err := countTree(f.in.Folder, l.Rel, f.in.MaxEntries, f.in.Protected)
	if err != nil {
		return err
	}
	l.Files, l.Bytes = files, bytes
	switch {
	case over:
		l.Eligible, l.Why = false, WhyTooLarge
	case protected && l.Eligible:
		l.Eligible, l.Why = false, WhyDenied
	}
	if !l.Eligible && l.Why == "" {
		l.Why = WhyUnrecognized
	}
	f.out = append(f.out, l)
	return nil
}

// shortOwner returns the alias whose known id starts with an 8-hex name,
// when exactly one does.
func (f *finder) shortOwner(name string) ids.Alias {
	var found ids.Alias
	n := 0
	seen := map[ids.Alias]bool{}
	for id, a := range f.in.KnownIDs {
		if strings.HasPrefix(id, name) && a != "" && !seen[a] {
			seen[a] = true
			found = a
			n++
		}
	}
	if n == 1 {
		return found
	}
	return ""
}

// notDir reports an entry that is neither a directory nor a symlink (a
// symlink is reported as such by add).
func notDir(e fs.DirEntry) bool { return !e.IsDir() && e.Type()&fs.ModeSymlink == 0 }

// countTree counts regular files and their bytes below folder/rel (lstat
// walk, symlinks not followed). over is set when more than max entries
// exist; protected when protect reports rel or any path below it.
func countTree(folder, rel string, max int, protect func(string) bool) (files int, bytes int64, over, protected bool, err error) {
	entries := 0
	p := filepath.Join(folder, filepath.FromSlash(rel))
	err = filepath.WalkDir(p, func(cur string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		entries++
		if entries > max+1 {
			over = true
			return filepath.SkipAll
		}
		if protect != nil && !protected {
			sub, rerr := filepath.Rel(p, cur)
			if rerr != nil {
				return rerr
			}
			if protect(path.Join(rel, filepath.ToSlash(sub))) {
				protected = true
			}
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			files++
			bytes += fi.Size()
		}
		return nil
	})
	return files, bytes, over, protected, err
}

// stubElsewhere reports whether the stub-named file mine needs no other
// copy: it is empty by its rule, or the owner holds the same bytes.
func stubElsewhere(mine, owner string, rule StubFile) bool {
	b, err := readSmall(mine, maxStubBytes)
	if err != nil {
		return false
	}
	if isStubJSON(b, rule) {
		return true
	}
	theirs, err := readSmall(owner, maxStubBytes)
	return err == nil && string(theirs) == string(b)
}

// uniqueNames counts the names directly inside each <org>/ of mine that
// are absent from the same directory of the owner folder (all of them when
// that directory is absent). Ignored names do not count. A stub-named file
// (its name is fixed, so its name says nothing) counts unless it is empty
// by its rule or the owner holds the same bytes (stubElsewhere): real
// scheduled tasks are never hidden. Other entries are compared by name
// only; no other file is opened.
func uniqueNames(mine, owner string, stub StubRules) (int, error) {
	orgs, err := os.ReadDir(mine)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range orgs {
		if stub.ignored(o.Name()) {
			continue
		}
		if !o.IsDir() || o.Type()&fs.ModeSymlink != 0 {
			// A file directly below the account directory is compared by
			// name at that level.
			if _, err := os.Lstat(filepath.Join(owner, o.Name())); errors.Is(err, fs.ErrNotExist) {
				n++
			}
			continue
		}
		names, err := os.ReadDir(filepath.Join(mine, o.Name()))
		if err != nil {
			return 0, err
		}
		have := map[string]bool{}
		if theirs, err := os.ReadDir(filepath.Join(owner, o.Name())); err == nil {
			for _, t := range theirs {
				have[t.Name()] = true
			}
		}
		for _, x := range names {
			if stub.ignored(x.Name()) {
				continue
			}
			if rule, isStub := stub.Files[x.Name()]; isStub {
				// A file of a fixed name: an empty stub never counts; one
				// with data counts unless the owner has the same bytes.
				if !stubElsewhere(filepath.Join(mine, o.Name(), x.Name()), filepath.Join(owner, o.Name(), x.Name()), rule) {
					n++
				}
				continue
			}
			if !have[x.Name()] {
				n++
			}
		}
	}
	return n, nil
}
