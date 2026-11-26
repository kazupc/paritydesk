package policy

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// busyTextIDs are the ids the log-signal item's orchestrator texts moved
// to (spec-busy 12.2, spec-i18n-ja 7.4 item 2).
var busyTextIDs = []string{
	"confirm.busy_line",
	"confirm.busy_line_no_time",
	"confirm.busy_untitled",
	"confirm.busy_waited",
	"progress.p3.busy_wait",
	"warn.busy_stale",
	"warn.busy_preview",
	"warn.busy_truncated",
	"warn.busy_log_unreadable",
	"warn.account_window",
	"warn.account_change",
	"warn.in_folder_sign_in",
	"warn.native_multi",
	"warn.swap_only_native_log",
	"warn.swap_only_native_history",
	"err.sync.swap_only_native_log",
	"err.sync.swap_only_native_history",
	"err.resolve.in_folder_sign_in",
	"doctor.busy_detection.ok",
	"doctor.busy_detection.record",
	"doctor.busy_detection.partial",
	"doctor.busy_detection.unavailable",
	"doctor.busy_now.app_stopped",
	"doctor.busy_now.none",
	"doctor.busy_now.some",
	"doctor.busy_now.no_signals",
	"doctor.busy_now.unreadable",
	"doctor.busy_now.records",
	"doctor.busy_now.stale",
	"doctor.busy_now.truncated",
	"doctor.account_change.unavailable",
	"doctor.account_change.none",
	"doctor.account_change.one",
	"doctor.account_change.unknown",
	"doctor.account_change.in_folder",
	"doctor.account_change.native_log",
	"doctor.account_change.native_history",
	"doctor.account_change.unknown_time",
	"doctor.chrome_pairing_org.known",
	"doctor.chrome_pairing_org.unknown",
	"doctor.chrome_pairing_org.state",
	"doctor.chrome_bridge.pairing_disabled",
	"doctor.version.deprecated_key",
}

// TestBusyTextMigrated: the log-signal item's orchestrator texts live in
// the catalog under their ids, and busytext.go is gone.
func TestBusyTextMigrated(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "internal", "orchestrator", "busytext.go")); err == nil {
		t.Error("internal/orchestrator/busytext.go still exists")
	}
	catalog, err := os.ReadFile(filepath.Join(root, "internal", "i18n", "catalog.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range busyTextIDs {
		if !strings.Contains(string(catalog), strconv.Quote(id)) {
			t.Errorf("%s is not a catalog id", id)
		}
	}
}

// TestSignalPatternsInManifests: every main.log pattern of the embedded
// manifests starts at the message (no "^" or ".*" in front); the loader
// anchors it. Their full validation runs when the manifests load.
func TestSignalPatternsInManifests(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"6.x.toml", "7.x.toml"} {
		data, err := os.ReadFile(filepath.Join(root, "internal", "compat", "manifests", name))
		if err != nil {
			t.Fatal(err)
		}
		in := false
		for i, line := range strings.Split(string(data), "\n") {
			switch {
			case strings.HasPrefix(line, "[signals]"):
				in = true
				continue
			case strings.HasPrefix(line, "["), strings.HasPrefix(line, "# ----"):
				in = false
			}
			if !in || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, bad := range []string{"'^", "'.*", "'.+"} {
				if strings.Contains(line, bad) {
					t.Errorf("%s:%d: a signal pattern must start at the message", name, i+1)
				}
			}
		}
	}
}
