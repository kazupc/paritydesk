package all

import (
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/prefs"
	"github.com/kazupc/paritydesk/internal/sources/presets"
	"github.com/kazupc/paritydesk/internal/sources/sessions"
	"github.com/kazupc/paritydesk/internal/sources/sidebar"
	"github.com/kazupc/paritydesk/internal/sources/windowstate"
	"github.com/kazupc/paritydesk/internal/sources/worktrees"
)

// Registry returns an adapter for every v0.1 row.
func Registry() sources.Registry {
	r := sources.Registry{}
	for _, a := range []sources.Adapter{
		sessions.NewSessions(), sessions.NewTasks(), sessions.NewWaitingInput(),
		presets.New(),
		sidebar.NewGroupOrder(), sidebar.NewSidebarView(), sidebar.NewPinnedOrder(),
		prefs.NewAppPrefs(), prefs.NewMCPServers(),
		worktrees.New(),
		windowstate.New(),
	} {
		r[a.Row()] = a
	}
	return r
}
