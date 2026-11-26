package profile

import (
	"errors"
	"strings"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

// Constant BaseInvalidWhy texts (catalog ids profile.why.*).
const (
	WhyNoState         = i18n.ProfileWhyNoState
	WhyFolderMissing   = i18n.ProfileWhyFolderMissing
	WhyIdentityUnknown = i18n.ProfileWhyIdentityUnknown
	WhyNoMarker        = i18n.ProfileWhyNoMarker
	WhyMarkerAlias     = i18n.ProfileWhyMarkerAlias
	WhyMarkerReplica   = i18n.ProfileWhyMarkerReplica
	WhyMarkerBase      = i18n.ProfileWhyMarkerBase
	WhyFolderID        = i18n.ProfileWhyFolderID
	WhyReplicaChanged  = i18n.ProfileWhyReplicaChanged
)

// resolver carries one Resolve call.
type resolver struct {
	cfg     *config.Config
	root    *state.Root
	hints   Hints
	byName  map[string]*Folder // by ids.FoldName
	active  *Folder
	owner   map[ids.AccountID]ids.Alias // recorded account -> profile
	res     *Resolution
	used    map[*Folder]bool
	boundTo map[ids.AccountID]ids.Alias // accounts found in bound folders
}

// Resolve binds every configured profile to a folder by identity and
// checks markers against state. An alias whose folder holds a different
// account gives *pderr.Error IdentityMismatch (exit 20); the message never
// suggests signing out. With root.PendingAdd set, an empty or unidentified
// Claude/ is not an error (the documented I1 exception for profile add).
//
// A profile is bound to Claude/ when Claude/ holds its recorded account;
// otherwise to its parked folder by name. A profile without a recorded
// account (a fresh state) is bound to Claude/ when it is state's active
// profile or the only profile without a parked folder.
func Resolve(cfg *config.Config, root *state.Root, folders []Folder, hints Hints) (*Resolution, error) {
	if cfg == nil {
		return nil, errors.New("profile: resolve: no config")
	}
	r := &resolver{
		cfg: cfg, root: root, hints: hints,
		byName:  map[string]*Folder{},
		owner:   map[ids.AccountID]ids.Alias{},
		res:     &Resolution{Bindings: map[ids.Alias]*Binding{}},
		used:    map[*Folder]bool{},
		boundTo: map[ids.AccountID]ids.Alias{},
	}
	for i := range folders {
		f := &folders[i]
		r.byName[ids.FoldName(f.Name)] = f
		if f.Name == ids.DefaultFolder {
			r.active = f
		}
	}
	for _, p := range cfg.Profiles {
		if a := r.recorded(p.Alias); a != "" {
			r.owner[a] = p.Alias
		}
	}
	activeAlias, err := r.bindActive()
	if err != nil {
		return nil, err
	}
	for _, p := range cfg.Profiles {
		b, err := r.bind(p, p.Alias == activeAlias)
		if err != nil {
			return nil, err
		}
		r.res.Bindings[p.Alias] = b
		if p.Alias == activeAlias {
			r.res.Active = b
		}
	}
	for i := range folders {
		f := &folders[i]
		if !r.used[f] && f != r.res.PendingAdd {
			r.res.Unregistered = append(r.res.Unregistered, *f)
		}
	}
	r.res.OutsideSwitch = r.outsideSwitch()
	return r.res, nil
}

// state returns the recorded state of a profile, or nil.
func (r *resolver) state(a ids.Alias) *state.ProfileState {
	if r.root == nil {
		return nil
	}
	return r.root.Profiles[a]
}

// recorded returns the profile's recorded account, or "".
func (r *resolver) recorded(a ids.Alias) ids.AccountID {
	if ps := r.state(a); ps != nil {
		return ps.Replica.Account
	}
	return ""
}

func (r *resolver) configured(a ids.Alias) bool {
	_, ok := r.cfg.Profile(a)
	return a != "" && ok
}

func (r *resolver) parked(p config.Profile) *Folder {
	return r.byName[ids.FoldName(p.ParkedFolder())]
}

// stateActive returns state's active profile when it is configured.
func (r *resolver) stateActive() ids.Alias {
	if r.root == nil || !r.configured(r.root.Active) {
		return ""
	}
	return r.root.Active
}

// bindActive decides which profile Claude/ belongs to ("" for none).
func (r *resolver) bindActive() (ids.Alias, error) {
	if r.active == nil {
		return "", nil
	}
	pending := r.root != nil && r.root.PendingAdd != nil
	if r.active.Identity == nil {
		if pending {
			r.res.PendingAdd = r.active
			return "", nil
		}
		if a := r.stateActive(); a != "" {
			return a, nil
		}
		return r.unrecordedWithoutFolder(), nil
	}
	acct := r.active.Identity.Account
	if a, ok := r.owner[acct]; ok {
		return a, nil
	}
	if pending {
		r.res.PendingAdd = r.active
		return "", nil
	}
	if a := r.stateActive(); a != "" {
		if r.recorded(a) == "" {
			return a, nil
		}
		if p, _ := r.cfg.Profile(a); r.parked(p) == nil {
			// State's active profile has no parked folder, so Claude/ is
			// its folder, and it holds another account.
			return "", mismatch(r.active.Name, a)
		}
		return "", nil // an outside switch into an unregistered account
	}
	return r.unrecordedWithoutFolder(), nil
}

// unrecordedWithoutFolder returns the only configured profile that has no
// recorded account and no parked folder, or "".
func (r *resolver) unrecordedWithoutFolder() ids.Alias {
	var found ids.Alias
	for _, p := range r.cfg.Profiles {
		if r.recorded(p.Alias) != "" || r.parked(p) != nil {
			continue
		}
		if found != "" {
			return ""
		}
		found = p.Alias
	}
	return found
}

func mismatch(folder string, alias ids.Alias) error {
	return pderr.NewT(pderr.IdentityMismatch, "profile/resolve", i18n.ErrResolveMismatch, folder, alias)
}

// bind resolves one profile.
func (r *resolver) bind(p config.Profile, active bool) (*Binding, error) {
	b := &Binding{Profile: p}
	if active {
		b.Folder = r.active
	} else {
		b.Folder = r.parked(p)
	}
	if b.Folder != nil {
		if err := r.checkIdentity(p.Alias, b.Folder); err != nil {
			return nil, err
		}
		r.used[b.Folder] = true
	}
	b.Replica = r.replica(p.Alias, b.Folder, active)
	b.BaseValid, b.BaseInvalidWhy = r.baseValid(p.Alias, b)
	return b, nil
}

// checkIdentity refuses a folder that holds another account than the
// profile's recorded one, another profile's account, or an account
// already bound to another profile.
func (r *resolver) checkIdentity(alias ids.Alias, f *Folder) error {
	if f.Identity == nil {
		return nil
	}
	acct := f.Identity.Account
	if rec := r.recorded(alias); rec != "" && rec != acct {
		return mismatch(f.Name, alias)
	}
	if other, ok := r.owner[acct]; ok && other != alias {
		return mismatch(f.Name, alias)
	}
	if other, ok := r.boundTo[acct]; ok && other != alias {
		return mismatch(f.Name, alias)
	}
	r.boundTo[acct] = alias
	return nil
}

// replica returns the profile's (account, org).
func (r *resolver) replica(alias ids.Alias, f *Folder, active bool) model.Replica {
	acct := r.recorded(alias)
	if f != nil && f.Identity != nil {
		acct = f.Identity.Account
	}
	if f == nil {
		if ps := r.state(alias); ps != nil {
			return ps.Replica
		}
		return model.Replica{Account: acct}
	}
	var orgs []ids.OrgID
	for _, rep := range f.Replicas {
		if rep.Account == acct && !containsOrg(orgs, rep.Org) {
			orgs = append(orgs, rep.Org)
		}
	}
	rep := model.Replica{Account: acct}
	switch {
	case len(orgs) == 1:
		rep.Org = orgs[0]
	case len(orgs) > 1 && active && containsOrg(orgs, r.hints.ActiveOrg):
		rep.Org = r.hints.ActiveOrg
	case len(orgs) > 1 && (!active || r.hints.ActiveOrg == ""):
		// An account in several orgs, in a parked folder (or active with
		// no log hint; a hint naming another org means that org is new
		// here, so it stays unknown): the org its last run used. That is the recorded
		// replica, or, for state written before replicas of a leaving
		// profile were recorded, the one org this alias has bases for.
		rep.Org = r.lastOrg(alias, acct, orgs)
	}
	return rep
}

// lastOrg returns the org of alias's last run among orgs, or "".
func (r *resolver) lastOrg(alias ids.Alias, acct ids.AccountID, orgs []ids.OrgID) ids.OrgID {
	ps := r.state(alias)
	if ps != nil && ps.Replica.Account == acct && containsOrg(orgs, ps.Replica.Org) {
		return ps.Replica.Org
	}
	if r.root == nil {
		return ""
	}
	var found ids.OrgID
	for _, o := range orgs {
		prefix := string(alias) + "/" + model.Replica{Account: acct, Org: o}.Key() + "/"
		for k := range r.root.Bases {
			if strings.HasPrefix(k, prefix) {
				if found != "" && found != o {
					return ""
				}
				found = o
				break
			}
		}
	}
	return found
}

func containsOrg(orgs []ids.OrgID, o ids.OrgID) bool {
	if o == "" {
		return false
	}
	for _, x := range orgs {
		if x == o {
			return true
		}
	}
	return false
}

// baseValid checks the folder against the recorded state: marker alias,
// replica and base hash, folder id, and the replica itself.
func (r *resolver) baseValid(alias ids.Alias, b *Binding) (bool, i18n.Msg) {
	ps := r.state(alias)
	f := b.Folder
	switch {
	case ps == nil:
		return false, WhyNoState
	case f == nil:
		return false, WhyFolderMissing
	case f.Identity == nil:
		return false, WhyIdentityUnknown
	case f.Marker == nil:
		return false, WhyNoMarker
	case f.Marker.Alias != alias:
		return false, WhyMarkerAlias
	case f.Marker.Replica != b.Replica:
		return false, WhyMarkerReplica
	case f.Marker.BaseHash != ps.MarkerBase:
		return false, WhyMarkerBase
	case f.ID.Dev != ps.FolderID.Dev || f.ID.Ino != ps.FolderID.Ino || !f.ID.Birth.Equal(ps.FolderID.Birth):
		return false, WhyFolderID
	case b.Replica != ps.Replica:
		return false, WhyReplicaChanged
	}
	return true, ""
}

// outsideSwitch reports whether Claude/ holds another account than
// state's recorded active profile.
func (r *resolver) outsideSwitch() bool {
	if r.root == nil || r.root.Active == "" || r.active == nil || r.active.Identity == nil {
		return false
	}
	rec := r.recorded(r.root.Active)
	return rec != "" && rec != r.active.Identity.Account
}
