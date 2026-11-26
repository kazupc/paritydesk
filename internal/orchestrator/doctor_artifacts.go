package orchestrator

import (
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
)

// artifactsDoctor is the doctor view of artifact sync (artifacts.Service).
type artifactsDoctor interface {
	// Doctor returns the number of pairs and the publish state word, or
	// the archive problem.
	Doctor() (pairs int, publishing string, err error)
}

// artifactsChecks adds the `artifacts` check (last of Doctor): off (config
// or manifest), fail when the archive cannot be read, else ok with the
// number of artifacts and the publish state (counts only, never ids).
func (dr *doctor) artifactsChecks(m *compat.Manifest) {
	o := dr.o
	a, ok := o.d.Artifacts.(artifactsDoctor)
	// The archive view is wired also while artifact sync is off (profile
	// rename and remove use it); the config decides here too when no
	// manifest is known.
	cfgOn := o.d.Config != nil && o.d.Config.Sync.Artifacts && o.d.Config.Online.Reconcile != config.OnlineReconcileOff
	if o.d.Artifacts == nil || !ok || !cfgOn || (m != nil && !o.artifactsActive(m)) {
		dr.add("artifacts", "ok", i18n.T(i18n.DoctorArtifactsOff))
		return
	}
	n, word, err := a.Doctor()
	if err != nil {
		dr.add("artifacts", "fail", i18n.T(i18n.DoctorArtifactsBad, err.Error()))
		return
	}
	dr.add("artifacts", "ok", i18n.T(i18n.DoctorArtifactsOK, n, word))
}
