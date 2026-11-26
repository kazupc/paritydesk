package testkit

import (
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
)

// Helpers for the sidebar-offline rows (collapsed groups and fork
// families in dframe-store, legacy per-account seed keys).

// storeMembers reports whether any dframe-store member of the collapse
// row is set, so mirror() writes the store.
func (s FolderSpec) storeMembers() bool {
	return s.CollapsedGroups != nil || s.CollapsedFamilies != nil || s.OpenedGroups != nil ||
		len(s.OmitStore) > 0 || s.StoreShared != 0
}

// sidebarOffline reports whether any field of the sidebar-offline rows is
// set, so mirror() writes the config mirror and Local Storage.
func (s FolderSpec) sidebarOffline() bool {
	return s.storeMembers() || s.SeedPrefs != nil
}

// collapseStore sets the collapse members of dframe-store's state and
// removes the ones listed in OmitStore.
func collapseStore(spec FolderSpec, state map[string]any) {
	for name, v := range map[string][]string{
		"collapsedGroups": spec.CollapsedGroups, "collapsedFamilies": spec.CollapsedFamilies, "openedGroups": spec.OpenedGroups,
	} {
		if v != nil {
			state[name] = v
		}
	}
	for _, name := range spec.OmitStore {
		delete(state, name)
	}
}

// storeShared is dframe-store's top-level "shared" member.
func storeShared(spec FolderSpec) int {
	if spec.StoreShared != 0 {
		return spec.StoreShared
	}
	return 1
}

// seedPrefs writes the legacy keys as "<name>.<acct>" into the config
// mirror's epitaxyPrefs and as LSS envelopes.
func (w *writer) seedPrefs(spec FolderSpec, ep map[string]any, ls map[string]string) {
	acct := string(spec.Replica.Account)
	for name, v := range spec.SeedPrefs {
		ep[name+"."+acct] = v
		ls["LSS-persisted."+name+"."+acct] = envelope(w.t, v)
	}
}

// TestedVersion7 is the 7.x app version the embedded 7.x manifest lists
// as tested.
const TestedVersion7 = "7.37095.0"

// Manifest7 loads the embedded manifests and selects the 7.x one, for
// rows the 6.x manifest does not carry (sidebar-seed).
func Manifest7(t testing.TB) *compat.Manifest {
	t.Helper()
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := compat.Select(ms, TestedVersion7)
	if err != nil || m == nil {
		t.Fatalf("testkit: no manifest for %s: %v", TestedVersion7, err)
	}
	return m
}
