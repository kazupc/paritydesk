//go:build darwin && !pd_nofdscan

package app

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/kazupc/paritydesk/internal/fdscan"
	"github.com/kazupc/paritydesk/internal/fdscan/fdscantest"
)

// TestNativeMatchesLsof is the meaning-parity test on a real kernel: the
// holder child (running from dir/bin/holder) is read by the native scan
// and by /usr/sbin/lsof, both filtered with the same matcher, and the two
// (PID, Path, Access) sets must be equal.
func TestNativeMatchesLsof(t *testing.T) {
	const lsof = "/usr/sbin/lsof"
	if _, err := os.Stat(lsof); err != nil {
		t.Skip("no lsof")
	}
	dir := t.TempDir()
	sockDir, err := os.MkdirTemp("", "pd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	pid, _ := fdscantest.StartHolderChild(t, dir, sockDir, execStart)
	prefixes := []string{dir, sockDir}

	// Native only: a failure is an error, never a silent lsof call.
	noRun := &fakeRunner{t: t, reply: func(Template, []string) ([]byte, error) {
		t.Error("the lsof fallback ran")
		return nil, nil
	}}
	h := newScanHolders(ScanNative, fdscan.System(), "", lsofHolders{run: noRun}, nil, withScanPIDs(pid))
	native, err := h.Holders(t.Context(), prefixes)
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(lsof, "-nP", "-w", "-F", "pfan", "-p", strconv.Itoa(pid)).Output()
	if err != nil && len(out) == 0 {
		t.Fatalf("lsof: %v", err)
	}
	fromLsof := parseLsof(out, newPathMatcher(prefixes).Match)

	nt, lt := tuples(native), tuples(fromLsof)
	if len(nt) == 0 || len(lt) == 0 || !maps.Equal(nt, lt) {
		t.Fatalf("native\n%v\nlsof\n%v", native, fromLsof)
	}
	rd, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	exe := tuple{pid, filepath.Join(rd, fdscantest.Exe), "txt"}
	if !nt[exe] || !lt[exe] {
		t.Fatalf("the executable inside the folder is missing: %+v", exe)
	}
}
