package profile

import (
	"slices"
	"sort"
	"time"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/state"
)

// LoggedChange is an account change inside a data folder, from main.log.
type LoggedChange struct {
	From, To ids.AccountID
	At       time.Time
}

// AttributedChange is a LoggedChange with the profiles that hold the
// accounts and whether it happened after the last committed run.
type AttributedChange struct {
	LoggedChange
	FromAlias, ToAlias ids.Alias // "" when no profile has the account
	SinceCommit        bool      // At after root.LastCommitAt (false when either is zero)
}

// AttributeChanges maps accounts to aliases: the profile whose
// Replica.Account is the account, else the first (sorted) profile whose
// Replicas or AccountHistory list it.
func AttributeChanges(root *state.Root, changes []LoggedChange) []AttributedChange {
	if len(changes) == 0 {
		return nil
	}
	out := make([]AttributedChange, len(changes))
	var mark time.Time
	if root != nil {
		mark = root.CommitMark()
	}
	for i, c := range changes {
		out[i] = AttributedChange{
			LoggedChange: c,
			FromAlias:    accountAlias(root, c.From),
			ToAlias:      accountAlias(root, c.To),
			SinceCommit:  !mark.IsZero() && !c.At.IsZero() && c.At.After(mark),
		}
	}
	return out
}

// accountAlias returns the profile that holds account a.
func accountAlias(root *state.Root, a ids.AccountID) ids.Alias {
	if root == nil || a == "" {
		return ""
	}
	aliases := make([]ids.Alias, 0, len(root.Profiles))
	for alias, ps := range root.Profiles {
		if ps == nil {
			continue
		}
		if ps.Replica.Account == a {
			return alias
		}
		aliases = append(aliases, alias)
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i] < aliases[j] })
	for _, alias := range aliases {
		ps := root.Profiles[alias]
		if slices.Contains(ps.AccountHistory, a) {
			return alias
		}
		for _, r := range ps.Replicas {
			if r.Account == a {
				return alias
			}
		}
	}
	return ""
}

// NativeMultiFromLog reports Claude's own multi-account mode from the
// log: a chain of at least two SinceCommit changes (the To of one is the
// From of the next, in time order), that is, one folder changed accounts
// twice since the last committed run. It returns the folder's alias
// (FromAlias of the first change of the longest chain, "" when no
// profile has that account) and the chain length; n == 0 when none.
// A single change is not native mode: when the folder still holds the
// other account, InFolderSignIn handles it; when it holds its own account
// again, it was changed back (that needs two lines, so it is a chain).
func NativeMultiFromLog(changes []AttributedChange) (alias ids.Alias, n int) {
	first, n := NativeMultiChain(changes)
	return first.FromAlias, n
}

// NativeMultiChain is NativeMultiFromLog returning the first change of
// the longest chain itself (its From names the folder's account when no
// profile has it); n == 0 when there is no chain of two.
func NativeMultiChain(changes []AttributedChange) (first AttributedChange, n int) {
	var since []AttributedChange
	for _, c := range changes {
		if c.SinceCommit {
			since = append(since, c)
		}
	}
	sort.SliceStable(since, func(i, j int) bool { return since[i].At.Before(since[j].At) })
	// length[i] is the longest chain ending at i; first[i] its first
	// change.
	length := make([]int, len(since))
	head := make([]int, len(since))
	best, bestEnd := 0, -1
	for i, c := range since {
		length[i], head[i] = 1, i
		for j := 0; j < i; j++ {
			if since[j].To == c.From && length[j]+1 > length[i] {
				length[i], head[i] = length[j]+1, head[j]
			}
		}
		if length[i] > best {
			best, bestEnd = length[i], i
		}
	}
	if best < 2 {
		return AttributedChange{}, 0
	}
	return since[head[bestEnd]], best
}

// InFolderChange says that Claude/ is the folder of Folder, signed in to
// Holder's account inside Claude.
type InFolderChange struct {
	Folder, Holder ids.Alias
	FromLog        bool // a logged change since the last commit confirms it
}

// InFolderSignIn detects that state. It is false when root is nil, a
// profile add is pending (root.PendingAdd != nil; the add flow has its
// own identity checks), res.Active is nil, or Claude/ has no identity.
// Holder is res.Active.Profile.Alias. It is true when either rule holds:
//
//	M (marker): Claude/'s marker names a configured profile F != Holder,
//	  and the marker's replica account differs from the folder's
//	  identity. Folder = F.
//	L (log): res.OutsideSwitch, root.Active != Holder, Claude/ carries no
//	  marker naming Holder (a holder's own marker moved with its folder:
//	  a real outside switch), and the SinceCommit changes, followed in
//	  time order from root.Profiles[root.Active].Replica.Account (each
//	  change whose From is the account reached so far moves it to its
//	  To), end at the folder's identity. Folder = root.Active, FromLog.
//	  A folder signed in to another account and back again ends at its
//	  own account, so rule L does not fire for it.
//
// When both hold, Folder is the marker's alias and FromLog is true.
func InFolderSignIn(cfg *config.Config, root *state.Root, res *Resolution, changes []AttributedChange) (InFolderChange, bool) {
	if root == nil || root.PendingAdd != nil || res == nil || res.Active == nil ||
		res.Active.Folder == nil || res.Active.Folder.Identity == nil {
		return InFolderChange{}, false
	}
	holder := res.Active.Profile.Alias
	ident := res.Active.Folder.Identity.Account
	mk := res.Active.Folder.Marker
	out := InFolderChange{Holder: holder}
	found := false
	if mk != nil && mk.Alias != holder && cfg != nil {
		if _, ok := cfg.Profile(mk.Alias); ok && mk.Replica.Account != ident {
			out.Folder, found = mk.Alias, true
		}
	}
	holderMarked := mk != nil && mk.Alias == holder
	if res.OutsideSwitch && !holderMarked && root.Active != "" && root.Active != holder {
		if ps := root.Profiles[root.Active]; ps != nil && ps.Replica.Account != "" &&
			ps.Replica.Account != ident && loggedAccount(ps.Replica.Account, changes) == ident {
			if !found {
				out.Folder = root.Active
			}
			out.FromLog, found = true, true
		}
	}
	return out, found
}

// loggedAccount follows the changes since the last commit, in time
// order, from account start: a change whose From is the account reached
// so far moves it to the change's To. It returns the account the folder
// that held start holds by the log.
func loggedAccount(start ids.AccountID, changes []AttributedChange) ids.AccountID {
	var since []AttributedChange
	for _, c := range changes {
		if c.SinceCommit {
			since = append(since, c)
		}
	}
	sort.SliceStable(since, func(i, j int) bool { return since[i].At.Before(since[j].At) })
	cur := start
	for _, c := range since {
		if c.From == cur {
			cur = c.To
		}
	}
	return cur
}
