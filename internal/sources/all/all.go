package all

import (
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/pins"
	"github.com/kazupc/paritydesk/internal/sources/prefs"
	"github.com/kazupc/paritydesk/internal/sources/presets"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
	"github.com/kazupc/paritydesk/internal/sources/sessions"
	"github.com/kazupc/paritydesk/internal/sources/sidebar"
	"github.com/kazupc/paritydesk/internal/sources/windowstate"
	"github.com/kazupc/paritydesk/internal/sources/worktrees"
	"github.com/kazupc/paritydesk/internal/store/idb"
)

// Options configure adapters that need more than the defaults.
type Options struct {
	// IDB reads the pins row's IndexedDB value; nil reads in place
	// (idb.ReadString, tests). The wiring passes idb.Cloner.Read.
	IDB idb.Reader
}

func (o Options) pinsOptions() pins.Options { return pins.Options{Read: o.IDB} }

// Registry returns an adapter for every row that has one, with default
// options.
func Registry() sources.Registry { return RegistryWith(Options{}) }

// RegistryWith returns an adapter for every row that has one.
func RegistryWith(o Options) sources.Registry {
	r := sources.Registry{}
	for _, a := range []sources.Adapter{
		sessions.NewSessions(), sessions.NewTasks(), sessions.NewWaitingInput(),
		presets.New(),
		sidebar.NewGroupOrder(), sidebar.NewSidebarView(), sidebar.NewPinnedOrder(),
		prefs.NewAppPrefs(), prefs.NewMCPServers(),
		worktrees.New(),
		windowstate.New(),
		// v0.2 anchor: adapters scratch
		scratch.New(scratch.DefaultLimits(), nil, nil, nil),

		// v0.2 anchor: adapters sidebar-offline
		sidebar.NewCollapse(), sidebar.NewSeed(),

		// v0.2 anchor: adapters pins
		pins.New(o.pinsOptions()),
	} {
		r[a.Row()] = a
	}
	return r
}
