package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/kazupc/paritydesk/internal/fdscan"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// probeScanTimeout bounds doctor's probe scan.
const probeScanTimeout = 5 * time.Second

// opOpenFiles names open-file scan errors.
const opOpenFiles = "app/open-files"

// blindMsg is the message of a blind scan, in every mode: lsof, started
// as a child, inherits the same restriction and would report nothing.
const blindMsg = i18n.ErrOpenFilesBlind

// scanHolders finds holders with the native scan (internal/fdscan) and
// uses the fixed-argv lsof as the fallback in auto mode. Both backends
// filter with the same pathMatcher, so their results mean the same.
type scanHolders struct {
	mode   ScanMode
	native fdscan.Scanner // nil: lsof only
	reason string         // fixed reason when native is nil ("test", "config", "unsupported")
	lsof   lsofHolders
	procs  ProcessTable
	pids   []int // test seam only: passed to Options.PIDs; set only by withScanPIDs
}

// scanHolderOption is unexported, so only code in package app (tests) can
// pass one; NewDarwin passes none.
type scanHolderOption func(*scanHolders)

// withScanPIDs restricts the native scan to pids (TestNativeMatchesLsof).
func withScanPIDs(pids ...int) scanHolderOption {
	return func(h *scanHolders) { h.pids = append([]int(nil), pids...) }
}

// newScanHolders is the only constructor; NewDarwin calls it without
// options.
func newScanHolders(mode ScanMode, native fdscan.Scanner, reason string, lsof lsofHolders, procs ProcessTable, opts ...scanHolderOption) *scanHolders {
	if mode == "" {
		mode = ScanAuto
	}
	h := &scanHolders{mode: mode, native: native, reason: reason, lsof: lsof, procs: procs}
	for _, o := range opts {
		o(h)
	}
	return h
}

// callerContextErr reports whether err is the caller's own context error:
// ctx is done and err is a context error that does not wrap
// fdscan.ErrLayout (the self-check's time guard). A context-typed error
// while ctx is live is a scanner failure and follows the auto/native rules.
func callerContextErr(ctx context.Context, err error) bool {
	return ctx.Err() != nil &&
		(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) &&
		!errors.Is(err, fdscan.ErrLayout)
}

// blindErr is the error of a blind scan.
func blindErr(err error) error {
	return pderr.WrapT(pderr.Internal, opOpenFiles, blindMsg, err)
}

// Holders lists processes holding a path at or under one of prefixes. The
// caller's own process is reported like any other (CheckQuiescence
// filters it, holderCheck counts it), exactly as lsof reports it.
func (h *scanHolders) Holders(ctx context.Context, prefixes []string) ([]Holder, error) {
	if h.mode == ScanLsof || h.native == nil {
		if h.mode == ScanNative {
			return nil, pderr.NewT(pderr.Internal, opOpenFiles, i18n.ErrOpenFilesNativeUnavailable)
		}
		return h.lsof.Holders(ctx, prefixes)
	}
	if _, err := h.native.SelfCheck(ctx); err != nil {
		switch {
		case callerContextErr(ctx, err):
			return nil, err
		case errors.Is(err, fdscan.ErrBlind):
			return nil, blindErr(err)
		case h.mode == ScanNative:
			return nil, pderr.WrapT(pderr.Internal, opOpenFiles, i18n.ErrOpenFilesSelfCheck, err)
		}
		return h.lsof.Holders(ctx, prefixes)
	}
	m := newPathMatcher(prefixes)
	res, err := h.native.Scan(ctx, fdscan.Options{Match: m.Match, PIDs: h.pids})
	if err != nil {
		switch {
		case callerContextErr(ctx, err):
			return nil, err
		case errors.Is(err, fdscan.ErrBlind):
			return nil, blindErr(err)
		case h.mode == ScanNative:
			msg := i18n.ErrOpenFilesNativeFailed
			if errors.Is(err, fdscan.ErrPartial) {
				msg = i18n.ErrOpenFilesPartial
			}
			return nil, pderr.WrapT(pderr.Internal, opOpenFiles, msg, err)
		}
		// ErrPartial, ErrLayout, ErrListPIDs, ErrProcInfo, ErrTooLarge:
		// lsof for this call (v0.1's answer).
		return h.lsof.Holders(ctx, prefixes)
	}
	holders := make([]Holder, 0, len(res.Records))
	for _, r := range res.Records {
		holders = append(holders, holderFromRecord(r))
	}
	h.nameCommands(ctx, holders)
	return holders, nil
}

// nameCommands fills Command from the process table, only where the
// table's entry has the holder's start time (a reused pid stays unnamed).
// A failing process list is ignored: names are cosmetic.
func (h *scanHolders) nameCommands(ctx context.Context, holders []Holder) {
	if len(holders) == 0 || h.procs == nil {
		return
	}
	procs, err := h.procs.Processes(ctx)
	if err != nil {
		return
	}
	byPID := make(map[int]Process, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	for i := range holders {
		if p, ok := byPID[holders[i].PID]; ok && p.Started.Equal(holders[i].Started) {
			holders[i].Command = filepath.Base(p.Exe)
		}
	}
}

// holderFromRecord maps a native record to a Holder with the lsof path's
// access labels.
func holderFromRecord(r fdscan.Record) Holder {
	h := Holder{PID: r.PID, Path: r.Path, Started: r.Started}
	switch r.Kind {
	case fdscan.KindCwd, fdscan.KindRoot, fdscan.KindThreadCwd:
		h.Access = "cwd"
	case fdscan.KindMapped:
		h.Access = "txt"
	default: // KindFD, KindSocket, KindFileport
		switch r.Access {
		case fdscan.AccessRead:
			h.Access = "r"
		case fdscan.AccessWrite:
			h.Access = "w"
		default:
			h.Access = "u"
		}
	}
	return h
}

// lsofUsable reports whether /usr/sbin/lsof exists and is executable (no
// exec).
func lsofUsable() bool {
	fi, err := os.Stat(binLsof)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// Info describes the scan in use (doctor only). In lsof mode no native
// code runs; otherwise it runs the self-check and one probe scan whose
// matcher accepts nothing, so it keeps no path and reports counts only.
func (h *scanHolders) Info(ctx context.Context) HolderScanInfo {
	info := HolderScanInfo{Mode: h.mode, LsofOK: lsofUsable()}
	fallback := "lsof"
	if h.mode == ScanNative {
		fallback = "none"
	}
	if h.mode == ScanLsof {
		info.Backend, info.Reason = "lsof", "config"
		return info
	}
	if h.native == nil {
		info.Backend, info.Reason = fallback, h.reason
		return info
	}
	flavor, err := h.native.SelfCheck(ctx)
	if err != nil {
		switch {
		case errors.Is(err, fdscan.ErrUnsupported):
			info.Backend, info.Reason = fallback, "unsupported"
		case errors.Is(err, fdscan.ErrBlind):
			info.Backend, info.Reason = "none", "blind"
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			info.Backend, info.Reason = fallback, "scan-error"
		default:
			info.Backend, info.Reason = fallback, "self-check"
		}
		return info
	}
	pctx, cancel := context.WithTimeout(ctx, probeScanTimeout)
	defer cancel()
	res, err := h.native.Scan(pctx, fdscan.Options{Match: func(string) bool { return false }})
	info.Unreadable, info.Partial = res.Stats.Unreadable, res.Stats.Partial
	info.SocketCheck = res.Stats.SocketCheck
	info.RegionFlavor = flavor
	if res.Stats.RegionFlavor != 0 {
		info.RegionFlavor = res.Stats.RegionFlavor
	}
	switch {
	case err == nil:
		info.Backend = "native"
	case errors.Is(err, fdscan.ErrBlind):
		info.Backend, info.Reason = "none", "blind"
	case errors.Is(err, fdscan.ErrPartial):
		info.Backend, info.Reason = fallback, "partial"
	default:
		info.Backend, info.Reason = fallback, "scan-error"
	}
	return info
}
