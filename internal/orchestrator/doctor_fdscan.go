package orchestrator

import (
	"context"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/i18n"
)

// fdscanChecks adds the open-file-scan check: which backend finds the
// processes that hold the data folders, and whether a switch can rely on
// it. It runs the native self-check and one probe scan that keeps no
// path (counts only).
func (dr *doctor) fdscanChecks(ctx context.Context) {
	status, detail := openFileScanCheck(dr.o.d.App.HolderScan(ctx))
	dr.add("open-file-scan", status, detail)
}

// openFileScanCheck maps the scan's description to a status and detail;
// the first matching row wins.
func openFileScanCheck(info app.HolderScanInfo) (status string, detail i18n.Text) {
	switch {
	case info.Reason == "blind":
		return "fail", i18n.Tx(i18n.DoctorFDScanBlind)
	case info.Mode == app.ScanNative && info.Backend != "native":
		return "fail", i18n.Tx(i18n.DoctorFDScanNativeFailed)
	case info.Backend != "native" && !info.LsofOK:
		return "fail", i18n.Tx(i18n.DoctorFDScanNone)
	case info.Backend == "native":
		return "ok", i18n.Tx(i18n.DoctorFDScanNative)
	case info.Reason == "config":
		return "ok", i18n.Tx(i18n.DoctorFDScanLsofConfig)
	case info.Reason == "test":
		return "ok", i18n.Tx(i18n.DoctorFDScanLsofTest)
	case info.Reason == "partial":
		return "warn", i18n.Tx(i18n.DoctorFDScanPartial, info.Unreadable, info.Partial)
	}
	// "self-check", "unsupported", "scan-error" (and anything unforeseen).
	return "warn", i18n.Tx(i18n.DoctorFDScanFallback)
}
