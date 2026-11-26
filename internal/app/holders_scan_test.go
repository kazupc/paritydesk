package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/fdscan"
	"github.com/kazupc/paritydesk/internal/pderr"
)

func TestPathMatcher(t *testing.T) {
	m := newPathMatcher([]string{"/data/Claude-a", "/data/Claude/", ""})
	for path, want := range map[string]bool{
		"/data/Claude":          true,
		"/data/Claude/x":        true,
		"/data/Claude/x/../y":   true,
		"/data/Claude-a/config": true,
		"/data/Claude-ab/other": false, // a sibling is not under the prefix
		"/data/Claud":           false,
		"/data":                 false,
		"/data/claude/x":        true, // case fold
		"/DATA/CLAUDE-A":        true,
		"":                      false,
	} {
		if got := m.Match(path); got != want {
			t.Errorf("Match(%q) = %v, want %v", path, got, want)
		}
	}
	// NFC versus NFD.
	nfc := newPathMatcher([]string{"/data/Caf\u00e9"})
	if !nfc.Match("/data/Cafe\u0301/x") || !newPathMatcher([]string{"/data/Cafe\u0301"}).Match("/data/caf\u00e9") {
		t.Fatal("normalization forms do not match")
	}
	// A symlinked prefix matches the resolved path the kernel reports.
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	sm := newPathMatcher([]string{link})
	if !sm.Match(filepath.Join(resolved, "f")) || !sm.Match(filepath.Join(link, "f")) || sm.Match(filepath.Join(dir, "other")) {
		t.Fatal("symlinked prefix")
	}
}

func TestHolderFromRecord(t *testing.T) {
	started := time.Unix(5, 0)
	tests := []struct {
		kind   fdscan.Kind
		access fdscan.Access
		want   string
	}{
		{fdscan.KindFD, fdscan.AccessRead, "r"},
		{fdscan.KindFD, fdscan.AccessWrite, "w"},
		{fdscan.KindFD, fdscan.AccessReadWrite, "u"},
		{fdscan.KindFD, fdscan.AccessNone, "u"},
		{fdscan.KindSocket, fdscan.AccessReadWrite, "u"},
		{fdscan.KindSocket, fdscan.AccessRead, "r"},
		{fdscan.KindFileport, fdscan.AccessWrite, "w"},
		{fdscan.KindCwd, fdscan.AccessNone, "cwd"},
		{fdscan.KindRoot, fdscan.AccessNone, "cwd"},
		{fdscan.KindThreadCwd, fdscan.AccessNone, "cwd"},
		{fdscan.KindMapped, fdscan.AccessNone, "txt"},
	}
	for _, tc := range tests {
		h := holderFromRecord(fdscan.Record{PID: 7, Started: started, Kind: tc.kind, Access: tc.access, Path: "/p"})
		if h.Access != tc.want || h.PID != 7 || h.Path != "/p" || !h.Started.Equal(started) || h.Command != "" {
			t.Errorf("%s/%d: %+v, want access %q", tc.kind, tc.access, h, tc.want)
		}
	}
}

// fixtureRecords are the native records equivalent to lsofFixture.
func fixtureRecords() []fdscan.Record {
	return []fdscan.Record{
		{PID: 101, Kind: fdscan.KindCwd, FD: -1, Path: "/data/Claude"},
		{PID: 101, Kind: fdscan.KindMapped, FD: -1, Path: "/Apps/Example.app/Contents/MacOS/Example"},
		{PID: 101, Kind: fdscan.KindFD, FD: 12, Access: fdscan.AccessRead, Path: "/data/Claude/Local Storage/leveldb/000005.ldb"},
		{PID: 101, Kind: fdscan.KindFD, FD: 13, Access: fdscan.AccessReadWrite, Path: "/data/Claude/Local Storage/leveldb/LOCK"},
		{PID: 202, Kind: fdscan.KindFD, FD: 4, Access: fdscan.AccessWrite, Path: "/data/Claude-a/config.json"},
		{PID: 202, Kind: fdscan.KindFD, FD: 5, Access: fdscan.AccessRead, Path: "/data/Claude-ab/other"},
		{PID: 202, Kind: fdscan.KindFD, FD: 6, Access: fdscan.AccessNone, Path: "/data/Claude/x"},
		{PID: 202, Kind: fdscan.KindThreadCwd, FD: -1, Path: "/data/Claude/twd"},
		{PID: 202, Kind: fdscan.KindFileport, FD: -1, Access: fdscan.AccessWrite, Path: "/data/Claude/port.txt"},
		{PID: 303, Kind: fdscan.KindMapped, FD: -1, Path: "/data/Claude/mapped"},
	}
}

func TestRecordsMatchLsofFixture(t *testing.T) {
	m := newPathMatcher([]string{"/data/Claude", "/data/Claude-a/"})
	var got []Holder
	for _, r := range fixtureRecords() {
		if m.Match(r.Path) {
			got = append(got, holderFromRecord(r))
		}
	}
	if !slices.Equal(got, lsofFixtureWant) {
		t.Fatalf("got\n%v\nwant\n%v", got, lsofFixtureWant)
	}
}

// renderLsof renders records as `lsof -nP -w -F pfan` text: one 'p' line
// per process, then per record its 'f', 'a' and 'n' lines; noise adds
// NOFD records that the parser must skip.
func renderLsof(records []fdscan.Record, noise []bool) string {
	var b strings.Builder
	pid := 0
	for i, r := range records {
		if r.PID != pid {
			pid = r.PID
			fmt.Fprintf(&b, "p%d\n", pid)
		}
		var label string
		switch r.Kind {
		case fdscan.KindFD, fdscan.KindSocket:
			label = fmt.Sprint(r.FD)
		case fdscan.KindFileport:
			label = "fp."
		case fdscan.KindCwd:
			label = "cwd"
		case fdscan.KindRoot:
			label = "rtd"
		case fdscan.KindThreadCwd:
			label = "twd"
		case fdscan.KindMapped:
			label = "txt"
		}
		access := " "
		switch r.Access {
		case fdscan.AccessRead:
			access = "r"
		case fdscan.AccessWrite:
			access = "w"
		case fdscan.AccessReadWrite:
			access = "u"
		}
		fmt.Fprintf(&b, "f%s\na%s\nn%s\n", label, access, r.Path)
		if i < len(noise) && noise[i] {
			fmt.Fprintf(&b, "fNOFD\nn%s (opendir: Permission denied)\n", r.Path)
		}
	}
	return b.String()
}

// tuple is what both backends must agree on.
type tuple struct {
	PID    int
	Path   string
	Access string
}

func tuples(hs []Holder) map[tuple]bool {
	out := map[tuple]bool{}
	for _, h := range hs {
		out[tuple{h.PID, h.Path, h.Access}] = true
	}
	return out
}

func writes(hs []Holder) int {
	n := 0
	for _, h := range hs {
		if h.Writes() {
			n++
		}
	}
	return n
}

func TestNativeAndLsofAgree(t *testing.T) {
	prefixes := []string{"/data/Claude", "/data/Claude-a"}
	m := newPathMatcher(prefixes)
	bases := []string{"/data/Claude", "/data/Claude-a", "/data/Claude-ab", "/data/Claud", "/data/claude", "/other"}
	suffixes := []string{"", "/x", "/y/z", "/X", "/Local Storage/LOCK"}
	kinds := []fdscan.Kind{fdscan.KindFD, fdscan.KindSocket, fdscan.KindFileport, fdscan.KindCwd, fdscan.KindRoot, fdscan.KindThreadCwd, fdscan.KindMapped}
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 30).Draw(rt, "n")
		var records []fdscan.Record
		seenTxt := map[string]bool{}
		for i := range n {
			r := fdscan.Record{
				PID:  rapid.IntRange(1, 4).Draw(rt, fmt.Sprintf("pid%d", i)),
				Kind: rapid.SampledFrom(kinds).Draw(rt, fmt.Sprintf("kind%d", i)),
				FD:   -1,
				Path: rapid.SampledFrom(bases).Draw(rt, fmt.Sprintf("base%d", i)) + rapid.SampledFrom(suffixes).Draw(rt, fmt.Sprintf("suffix%d", i)),
				Dev:  1,
				Ino:  uint64(rapid.IntRange(1, 5).Draw(rt, fmt.Sprintf("ino%d", i))),
			}
			switch r.Kind {
			case fdscan.KindFD, fdscan.KindSocket:
				r.FD = int32(rapid.IntRange(0, 50).Draw(rt, fmt.Sprintf("fd%d", i)))
				fallthrough
			case fdscan.KindFileport:
				r.Access = fdscan.Access(rapid.IntRange(0, 3).Draw(rt, fmt.Sprintf("access%d", i)))
			case fdscan.KindMapped:
				// One txt per (pid, dev, ino), like both backends.
				key := fmt.Sprint(r.PID, r.Dev, r.Ino)
				if seenTxt[key] {
					continue
				}
				seenTxt[key] = true
			}
			records = append(records, r)
		}
		slices.SortStableFunc(records, func(a, b fdscan.Record) int { return a.PID - b.PID })
		noise := rapid.SliceOfN(rapid.Bool(), len(records), len(records)).Draw(rt, "noise")

		var native []Holder
		for _, r := range records {
			if m.Match(r.Path) {
				native = append(native, holderFromRecord(r))
			}
		}
		lsof := parseLsof([]byte(renderLsof(records, noise)), m.Match)
		if !maps.Equal(tuples(native), tuples(lsof)) {
			rt.Fatalf("native %v\nlsof %v", native, lsof)
		}
		if len(native) != len(lsof) || writes(native) != writes(lsof) {
			rt.Fatalf("counts differ: %d/%d records, %d/%d writers", len(native), len(lsof), writes(native), writes(lsof))
		}
	})
}

// fakeScanner is an fdscan.Scanner with scripted answers.
type fakeScanner struct {
	mu       sync.Mutex
	checkErr []error // per SelfCheck call; the last repeats
	scanErr  []error // per Scan call; the last repeats
	records  []fdscan.Record
	stats    fdscan.Stats
	checks   int
	scans    int
	opts     []fdscan.Options
}

func pick(errs []error, n int) error {
	if len(errs) == 0 {
		return nil
	}
	return errs[min(n, len(errs)-1)]
}

func (f *fakeScanner) SelfCheck(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	err := pick(f.checkErr, f.checks)
	f.checks++
	if err != nil {
		return 0, err
	}
	return 22, nil
}

func (f *fakeScanner) Scan(ctx context.Context, opt fdscan.Options) (fdscan.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	err := pick(f.scanErr, f.scans)
	f.scans++
	f.opts = append(f.opts, opt)
	if err != nil {
		return fdscan.Result{Stats: f.stats}, err
	}
	var out []fdscan.Record
	for _, r := range f.records {
		if opt.Match(r.Path) {
			out = append(out, r)
		}
	}
	return fdscan.Result{Records: out, Stats: f.stats}, nil
}

func (f *fakeScanner) counts() (checks, scans int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.scans
}

// lsofRunner answers TmplLsof with lsofFixture.
func lsofRunner(t *testing.T) *fakeRunner {
	return &fakeRunner{t: t, reply: func(Template, []string) ([]byte, error) { return []byte(lsofFixture), nil }}
}

func lsofCalls(r *fakeRunner) int {
	n := 0
	for _, tm := range r.templates() {
		if tm == TmplLsof {
			n++
		}
	}
	return n
}

var watchedFolders = []string{"/data/Claude"}

func TestScanHolders(t *testing.T) {
	t1, t2 := time.Unix(100, 0), time.Unix(200, 0)
	procs := procFunc(func() []Process {
		return []Process{{PID: 101, Exe: "/usr/bin/editor", Started: t1}, {PID: 202, Exe: "/usr/bin/other", Started: t1}}
	})
	native := func() *fakeScanner {
		return &fakeScanner{records: []fdscan.Record{
			{PID: 101, Started: t1, Kind: fdscan.KindFD, FD: 3, Access: fdscan.AccessWrite, Path: "/data/Claude/a"},
			{PID: 202, Started: t2, Kind: fdscan.KindCwd, FD: -1, Path: "/data/Claude"}, // pid reused: unnamed
			{PID: os.Getpid(), Started: t1, Kind: fdscan.KindFD, FD: 9, Access: fdscan.AccessRead, Path: "/data/Claude/self"},
			{PID: 303, Started: t1, Kind: fdscan.KindFD, FD: 1, Access: fdscan.AccessRead, Path: "/elsewhere"},
		}}
	}

	t.Run("auto, native ok", func(t *testing.T) {
		run, f := lsofRunner(t), native()
		h := newScanHolders(ScanAuto, f, "", lsofHolders{run: run}, procs)
		hs, err := h.Holders(t.Context(), watchedFolders)
		if err != nil {
			t.Fatal(err)
		}
		want := []Holder{
			{PID: 101, Command: "editor", Path: "/data/Claude/a", Access: "w", Started: t1},
			{PID: 202, Path: "/data/Claude", Access: "cwd", Started: t2},
			{PID: os.Getpid(), Path: "/data/Claude/self", Access: "r", Started: t1},
		}
		if !slices.Equal(hs, want) {
			t.Fatalf("holders\n%+v\nwant\n%+v", hs, want)
		}
		if lsofCalls(run) != 0 {
			t.Fatal("lsof ran although the native scan worked")
		}
		if f.opts[0].SkipPID != 0 || f.opts[0].PIDs != nil {
			t.Fatalf("options %+v: the own pid must not be skipped", f.opts[0])
		}
	})

	t.Run("auto, self-check error", func(t *testing.T) {
		run, f := lsofRunner(t), native()
		f.checkErr = []error{fdscan.ErrLayout}
		h := newScanHolders(ScanAuto, f, "", lsofHolders{run: run}, nil)
		hs, err := h.Holders(t.Context(), watchedFolders)
		if err != nil || lsofCalls(run) != 1 || len(hs) == 0 {
			t.Fatalf("%+v %v", hs, err)
		}
		if _, scans := f.counts(); scans != 0 {
			t.Fatal("scanned after a failed self-check")
		}
		if info := h.Info(t.Context()); info.Reason != "self-check" || info.Backend != "lsof" {
			t.Fatalf("info %+v", info)
		}
	})

	for _, e := range []error{fdscan.ErrPartial, fdscan.ErrLayout, fdscan.ErrListPIDs, fdscan.ErrProcInfo, fdscan.ErrTooLarge} {
		t.Run("auto, scan "+e.Error(), func(t *testing.T) {
			run, f := lsofRunner(t), native()
			f.scanErr = []error{e, nil}
			h := newScanHolders(ScanAuto, f, "", lsofHolders{run: run}, nil)
			hs, err := h.Holders(t.Context(), watchedFolders)
			if err != nil || lsofCalls(run) != 1 || !slices.ContainsFunc(hs, func(h Holder) bool { return h.PID == 303 }) {
				t.Fatalf("fallback: %+v %v", hs, err)
			}
			// lsof for that call only.
			if _, err := h.Holders(t.Context(), watchedFolders); err != nil || lsofCalls(run) != 1 {
				t.Fatalf("second call: %v, %d lsof calls", err, lsofCalls(run))
			}
		})
	}

	for _, mode := range []ScanMode{ScanAuto, ScanNative} {
		for _, where := range []string{"self-check", "scan"} {
			t.Run(fmt.Sprintf("%s, blind %s", mode, where), func(t *testing.T) {
				run, f := lsofRunner(t), native()
				if where == "self-check" {
					f.checkErr = []error{fdscan.ErrBlind}
				} else {
					f.scanErr = []error{fdscan.ErrBlind}
				}
				h := newScanHolders(mode, f, "", lsofHolders{run: run}, nil)
				_, err := h.Holders(t.Context(), watchedFolders)
				var pe *pderr.Error
				if !errors.As(err, &pe) || pe.Code != pderr.Internal || !strings.Contains(pe.Msg, "sandbox") || !errors.Is(err, fdscan.ErrBlind) {
					t.Fatalf("err %v", err)
				}
				if lsofCalls(run) != 0 {
					t.Fatal("lsof ran for a blind scan")
				}
			})
		}
	}

	for _, e := range []error{fdscan.ErrPartial, fdscan.ErrLayout, fdscan.ErrListPIDs, fdscan.ErrUnsupported} {
		t.Run("native, "+e.Error(), func(t *testing.T) {
			for _, where := range []string{"self-check", "scan"} {
				run, f := lsofRunner(t), native()
				if where == "self-check" {
					f.checkErr = []error{e}
				} else {
					f.scanErr = []error{e}
				}
				h := newScanHolders(ScanNative, f, "", lsofHolders{run: run}, nil)
				_, err := h.Holders(t.Context(), watchedFolders)
				var pe *pderr.Error
				if !errors.As(err, &pe) || pe.Code != pderr.Internal || !errors.Is(err, e) || lsofCalls(run) != 0 {
					t.Fatalf("%s: %v", where, err)
				}
			}
		})
	}

	t.Run("lsof mode", func(t *testing.T) {
		run, f := lsofRunner(t), native()
		h := newScanHolders(ScanLsof, f, "config", lsofHolders{run: run}, nil)
		if _, err := h.Holders(t.Context(), watchedFolders); err != nil || lsofCalls(run) != 1 {
			t.Fatal(err)
		}
		if checks, scans := f.counts(); checks+scans != 0 {
			t.Fatal("native code ran in lsof mode")
		}
	})

	t.Run("context errors pass through", func(t *testing.T) {
		for _, where := range []string{"self-check", "scan"} {
			run, f := lsofRunner(t), native()
			if where == "self-check" {
				f.checkErr = []error{context.Canceled, nil}
			} else {
				f.scanErr = []error{context.Canceled, nil}
			}
			h := newScanHolders(ScanAuto, f, "", lsofHolders{run: run}, nil)
			ctx, cancel := context.WithCancel(t.Context())
			cancel() // the caller's own context is done
			_, err := h.Holders(ctx, watchedFolders)
			if !errors.Is(err, context.Canceled) || lsofCalls(run) != 0 {
				t.Fatalf("%s: %v, %d lsof calls", where, err, lsofCalls(run))
			}
			var pe *pderr.Error
			if errors.As(err, &pe) {
				t.Fatalf("%s: context error wrapped: %v", where, err)
			}
			// The next call with a live context uses native.
			hs, err := h.Holders(t.Context(), watchedFolders)
			if err != nil || len(hs) == 0 || lsofCalls(run) != 0 {
				t.Fatalf("%s: next call: %v, %d lsof calls", where, err, lsofCalls(run))
			}
		}
	})

	t.Run("a context error while the caller's ctx is live is a scan failure", func(t *testing.T) {
		// For example a worker's view of the scanner's own cancellation:
		// auto asks lsof, native reports the scan failure, never a bare
		// context error the caller would read as its own cancellation.
		for _, where := range []string{"self-check", "scan"} {
			for _, cerr := range []error{context.Canceled, context.DeadlineExceeded} {
				run, f := lsofRunner(t), native()
				if where == "self-check" {
					f.checkErr = []error{cerr}
				} else {
					f.scanErr = []error{cerr}
				}
				h := newScanHolders(ScanAuto, f, "", lsofHolders{run: run}, nil)
				if hs, err := h.Holders(t.Context(), watchedFolders); err != nil || len(hs) == 0 || lsofCalls(run) != 1 {
					t.Fatalf("auto %s %v: %v, %d lsof calls", where, cerr, err, lsofCalls(run))
				}
				run, f = lsofRunner(t), native()
				if where == "self-check" {
					f.checkErr = []error{cerr}
				} else {
					f.scanErr = []error{cerr}
				}
				h = newScanHolders(ScanNative, f, "", lsofHolders{run: run}, nil)
				_, err := h.Holders(t.Context(), watchedFolders)
				var pe *pderr.Error
				if !errors.As(err, &pe) || pe.Code != pderr.Internal || !strings.HasPrefix(pe.Msg, "the native open-file scan failed") || lsofCalls(run) != 0 {
					t.Fatalf("native %s %v: %v, %d lsof calls", where, cerr, err, lsofCalls(run))
				}
			}
		}
	})

	t.Run("self-check time guard falls back in auto", func(t *testing.T) {
		run, f := lsofRunner(t), native()
		f.checkErr = []error{fmt.Errorf("%w: %w", fdscan.ErrLayout, context.DeadlineExceeded), nil}
		h := newScanHolders(ScanAuto, f, "", lsofHolders{run: run}, nil)
		if _, err := h.Holders(t.Context(), watchedFolders); err != nil || lsofCalls(run) != 1 {
			t.Fatalf("%v, %d lsof calls", err, lsofCalls(run))
		}
	})
}

func TestScanHoldersInfo(t *testing.T) {
	t.Run("lsof mode", func(t *testing.T) {
		f := &fakeScanner{}
		info := newScanHolders(ScanLsof, f, "config", lsofHolders{}, nil).Info(t.Context())
		if checks, scans := f.counts(); checks+scans != 0 {
			t.Fatal("native code ran")
		}
		if info.Backend != "lsof" || info.Reason != "config" || info.RegionFlavor != 0 || info.Mode != ScanLsof {
			t.Fatalf("info %+v", info)
		}
	})
	t.Run("no scanner", func(t *testing.T) {
		if info := newScanHolders(ScanAuto, nil, "test", lsofHolders{}, nil).Info(t.Context()); info.Backend != "lsof" || info.Reason != "test" {
			t.Fatalf("info %+v", info)
		}
		if info := newScanHolders(ScanNative, nil, "test", lsofHolders{}, nil).Info(t.Context()); info.Backend != "none" {
			t.Fatalf("info %+v", info)
		}
	})
	t.Run("self-check blind", func(t *testing.T) {
		f := &fakeScanner{checkErr: []error{fdscan.ErrBlind}}
		info := newScanHolders(ScanAuto, f, "", lsofHolders{}, nil).Info(t.Context())
		if _, scans := f.counts(); scans != 0 || info.Reason != "blind" || info.Backend != "none" {
			t.Fatalf("info %+v, %d scans", info, scans)
		}
	})
	t.Run("self-check unsupported", func(t *testing.T) {
		f := &fakeScanner{checkErr: []error{fdscan.ErrUnsupported}}
		if info := newScanHolders(ScanAuto, f, "", lsofHolders{}, nil).Info(t.Context()); info.Reason != "unsupported" || info.Backend != "lsof" {
			t.Fatalf("info %+v", info)
		}
	})
	for _, tc := range []struct {
		err             error
		reason, backend string
	}{
		{nil, "", "native"},
		{fdscan.ErrBlind, "blind", "none"},
		{fdscan.ErrPartial, "partial", "lsof"},
		{context.DeadlineExceeded, "scan-error", "lsof"},
		{fdscan.ErrListPIDs, "scan-error", "lsof"},
	} {
		t.Run(fmt.Sprintf("probe %v", tc.err), func(t *testing.T) {
			f := &fakeScanner{
				scanErr: []error{tc.err},
				stats:   fdscan.Stats{Unreadable: 2, Partial: 1, RegionFlavor: 22, SocketCheck: true},
				records: []fdscan.Record{{PID: 1, Kind: fdscan.KindCwd, Path: "/data/Claude"}},
			}
			info := newScanHolders(ScanAuto, f, "", lsofHolders{}, nil).Info(t.Context())
			if info.Reason != tc.reason || info.Backend != tc.backend {
				t.Fatalf("info %+v", info)
			}
			if info.Unreadable != 2 || info.Partial != 1 || info.RegionFlavor != 22 || !info.SocketCheck {
				t.Fatalf("counts not copied: %+v", info)
			}
			for _, p := range []string{"/", "/data/Claude", "/x"} {
				if f.opts[0].Match(p) {
					t.Fatalf("the probe's matcher accepted %q", p)
				}
			}
		})
	}
	t.Run("native mode, failing probe", func(t *testing.T) {
		f := &fakeScanner{scanErr: []error{fdscan.ErrPartial}}
		if info := newScanHolders(ScanNative, f, "", lsofHolders{}, nil).Info(t.Context()); info.Backend != "none" || info.Reason != "partial" {
			t.Fatalf("info %+v", info)
		}
	})
}

func TestCheckQuiescencePidReuse(t *testing.T) {
	listed := t0.Add(-time.Hour)
	newer := t0.Add(time.Minute)
	p := &Platform{
		Procs:   procFunc(func() []Process { return []Process{{PID: 70, Exe: "/usr/bin/editor", Started: listed}} }),
		Locks:   fakeLocks{},
		Holders: fakeHolders{{PID: 70, Path: "/data/Claude/b", Access: "w", Started: newer}, {PID: 71, Path: "/data/Claude/c", Access: "w", Started: newer}},
	}
	in := QuiescenceInput{BundlePath: testBundlePath, BundleID: testBundle, Folders: []string{"/data/Claude"}, After: t0}
	rep, err := p.CheckQuiescence(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	want := []Process{{PID: 70, Started: newer}, {PID: 71, Started: newer}}
	same := slices.EqualFunc(rep.StartedAfter, want, func(a, b Process) bool { return a.PID == b.PID && a.Started.Equal(b.Started) })
	if !same || rep.Quiet {
		t.Fatalf("started after %+v", rep.StartedAfter)
	}
	// Zero Started (lsof): v0.1 behaviour, the list entry decides.
	p.Holders = fakeHolders{{PID: 70, Path: "/data/Claude/b", Access: "w"}}
	rep, err = p.CheckQuiescence(t.Context(), in)
	if err != nil || len(rep.StartedAfter) != 0 || len(rep.WriteHolders) != 1 {
		t.Fatalf("lsof holder: %+v %v", rep, err)
	}
	// The same start as the list entry: the entry decides too.
	p.Procs = procFunc(func() []Process { return []Process{{PID: 70, Exe: "/usr/bin/editor", Started: newer}} })
	p.Holders = fakeHolders{{PID: 70, Path: "/data/Claude/b", Access: "w", Started: newer}}
	rep, err = p.CheckQuiescence(t.Context(), in)
	if err != nil || len(rep.StartedAfter) != 1 || rep.StartedAfter[0].Exe != "/usr/bin/editor" {
		t.Fatalf("same start: %+v %v", rep.StartedAfter, err)
	}
}

func TestNewDarwinOpenFileScan(t *testing.T) {
	run := lsofRunner(t)
	p, err := NewDarwin(DarwinConfig{Home: t.TempDir(), Exec: run})
	if err != nil {
		t.Fatal(err)
	}
	if info := p.HolderScan(t.Context()); info.Backend != "lsof" || info.Reason != "test" || info.Mode != ScanAuto {
		t.Fatalf("info %+v", info)
	}
	if _, err := p.Holders.Holders(t.Context(), watchedFolders); err != nil || lsofCalls(run) != 1 {
		t.Fatalf("holders: %v, %d lsof calls", err, lsofCalls(run))
	}
	if _, err := NewDarwin(DarwinConfig{Home: t.TempDir(), Exec: run, OpenFileScan: ScanNative}); err == nil {
		t.Fatal("native mode without a scanner accepted under go test")
	}
	if _, err := NewDarwin(DarwinConfig{Home: t.TempDir(), Exec: run, OpenFileScan: "proc"}); err == nil {
		t.Fatal("bad mode accepted")
	}
	p, err = NewDarwin(DarwinConfig{Home: t.TempDir(), Exec: run, OpenFileScan: ScanLsof})
	if err != nil {
		t.Fatal(err)
	}
	if info := p.HolderScan(t.Context()); info.Reason != "config" || info.Mode != ScanLsof {
		t.Fatalf("lsof mode: %+v", info)
	}
	f := &fakeScanner{}
	p, err = NewDarwin(DarwinConfig{Home: t.TempDir(), Exec: run, OpenFileScan: ScanNative, FDScan: f})
	if err != nil {
		t.Fatal(err)
	}
	if info := p.HolderScan(t.Context()); info.Backend != "native" {
		t.Fatalf("native with a scanner: %+v", info)
	}
	// A platform whose holders have no Info (fakes) reports lsof.
	if info := (&Platform{Holders: fakeHolders{}}).HolderScan(t.Context()); info.Backend != "lsof" {
		t.Fatalf("fake holders: %+v", info)
	}
}
