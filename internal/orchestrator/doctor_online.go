package orchestrator

import (
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/state"
)

// onlineChecks adds the `online` check: off (config or manifest), warn
// when the support directory is not the default one (the plugin's hooks
// cannot see a queue there), else ok with the queue's state.
func (dr *doctor) onlineChecks(root *state.Root, m *compat.Manifest) {
	o := dr.o
	switch {
	case o.d.Online == nil || (m != nil && !o.onlineActive(m)):
		dr.add("online", "ok", i18n.T(i18n.DoctorOnlineOff))
	case !o.defaultSupportDir():
		dr.add("online", "warn", i18n.T(i18n.DoctorOnlineSupportDir))
	default:
		dr.add("online", "ok", i18n.T(i18n.DoctorOnlineQueue, onlineStateWord(root, o.d.Online)))
	}
}

// onlineStateWord is the queue's state: root.Online's, or the head of
// queue.json when it is ahead (done committed the queue but not the root).
func onlineStateWord(root *state.Root, ol Online) string {
	rec := root.Online
	if rec == nil {
		return online.StateNone
	}
	word := rec.State
	if h, err := ol.ReadHead(); err == nil && h != nil && h.ID == rec.Queue {
		if (h.State == online.QueueComplete || h.State == online.QueueCancelled) && word != string(h.State) {
			word = string(h.State)
		}
	}
	return word
}
