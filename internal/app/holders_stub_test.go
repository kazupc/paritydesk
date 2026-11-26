//go:build !darwin || pd_nofdscan

package app

import (
	"errors"
	"testing"

	"github.com/kazupc/paritydesk/internal/fdscan"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// TestScanHoldersStub: where the native scan is not built, auto mode uses
// lsof and native mode is an error.
func TestScanHoldersStub(t *testing.T) {
	run := lsofRunner(t)
	h := newScanHolders(ScanAuto, fdscan.System(), "", lsofHolders{run: run}, nil)
	if _, err := h.Holders(t.Context(), watchedFolders); err != nil || lsofCalls(run) != 1 {
		t.Fatalf("auto: %v, %d lsof calls", err, lsofCalls(run))
	}
	if info := h.Info(t.Context()); info.Reason != "unsupported" || info.Backend != "lsof" {
		t.Fatalf("info %+v", info)
	}
	n := newScanHolders(ScanNative, fdscan.System(), "", lsofHolders{run: run}, nil)
	var pe *pderr.Error
	if _, err := n.Holders(t.Context(), watchedFolders); !errors.As(err, &pe) || pe.Code != pderr.Internal || lsofCalls(run) != 1 {
		t.Fatalf("native: %v", err)
	}
}
