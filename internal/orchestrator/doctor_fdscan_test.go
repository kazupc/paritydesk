package orchestrator

import (
	"context"
	"slices"
	"testing"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/i18n"
)

func TestDoctorOpenFileScan(t *testing.T) {
	native := app.HolderScanInfo{Mode: app.ScanAuto, Backend: "native", RegionFlavor: 22, LsofOK: true}
	with := func(f func(*app.HolderScanInfo)) app.HolderScanInfo {
		i := native
		f(&i)
		return i
	}
	tests := []struct {
		name   string
		info   app.HolderScanInfo
		status string
		detail string
	}{
		{"blind wins over everything", with(func(i *app.HolderScanInfo) { i.Backend, i.Reason, i.Mode = "none", "blind", app.ScanNative }), "fail", i18n.T(i18n.DoctorFDScanBlind)},
		{"blind in auto with lsof", with(func(i *app.HolderScanInfo) { i.Backend, i.Reason = "none", "blind" }), "fail", i18n.T(i18n.DoctorFDScanBlind)},
		{"native mode without native", with(func(i *app.HolderScanInfo) { i.Mode, i.Backend, i.Reason = app.ScanNative, "none", "self-check" }), "fail", i18n.T(i18n.DoctorFDScanNativeFailed)},
		{"no backend", with(func(i *app.HolderScanInfo) { i.Backend, i.Reason, i.LsofOK = "lsof", "unsupported", false }), "fail", i18n.T(i18n.DoctorFDScanNone)},
		{"native", native, "ok", i18n.T(i18n.DoctorFDScanNative)},
		{"native mode, native", with(func(i *app.HolderScanInfo) { i.Mode = app.ScanNative }), "ok", i18n.T(i18n.DoctorFDScanNative)},
		{"native without lsof", with(func(i *app.HolderScanInfo) { i.LsofOK = false }), "ok", i18n.T(i18n.DoctorFDScanNative)},
		{"lsof by config", app.HolderScanInfo{Mode: app.ScanLsof, Backend: "lsof", Reason: "config", LsofOK: true}, "ok", i18n.T(i18n.DoctorFDScanLsofConfig)},
		{"test build", with(func(i *app.HolderScanInfo) { i.Backend, i.Reason = "lsof", "test" }), "ok", i18n.T(i18n.DoctorFDScanLsofTest)},
		{"self-check", with(func(i *app.HolderScanInfo) { i.Backend, i.Reason = "lsof", "self-check" }), "warn", i18n.T(i18n.DoctorFDScanFallback)},
		{"unsupported", with(func(i *app.HolderScanInfo) { i.Backend, i.Reason = "lsof", "unsupported" }), "warn", i18n.T(i18n.DoctorFDScanFallback)},
		{"scan error", with(func(i *app.HolderScanInfo) { i.Backend, i.Reason = "lsof", "scan-error" }), "warn", i18n.T(i18n.DoctorFDScanFallback)},
		{"partial", with(func(i *app.HolderScanInfo) { i.Backend, i.Reason, i.Unreadable, i.Partial = "lsof", "partial", 3, 1 }), "warn", i18n.T(i18n.DoctorFDScanPartial, 3, 1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			info := tc.info
			e.app.scan = &info
			checks, err := e.orch().Doctor(context.Background(), DoctorOptions{})
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(checks, func(c Check) bool { return c.ID == "open-file-scan" })
			if i < 0 {
				t.Fatalf("no open-file-scan check in %+v", checks)
			}
			if c := checks[i]; c.Status != tc.status || c.Detail != tc.detail {
				t.Fatalf("got %s %q, want %s %q", c.Status, c.Detail, tc.status, tc.detail)
			}
			// Right after update.
			if i == 0 || checks[i-1].ID != "update" {
				t.Fatalf("open-file-scan is not right after update: %+v", checks)
			}
		})
	}
	if _, detail := openFileScanCheck(native); detail == "" {
		t.Fatal("empty detail")
	}
}
