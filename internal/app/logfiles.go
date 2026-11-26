package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	maxRotated  = 4        // main1.log … main4.log
	windowBytes = 24 << 20 // byte budget of a window read
	catchUpMax  = 24 << 20 // byte budget of a cursor read across rotation
	// firstTimeProbe bounds the bytes read to find a file's first record
	// time; firstTimeStep is the size of one probe read.
	firstTimeProbe = 64 << 10
	firstTimeStep  = 4 << 10
)

// logNames are the app's log files, newest first.
var logNames = [maxRotated + 1]string{"main.log", "main1.log", "main2.log", "main3.log", "main4.log"}

// LogCursor is a position in main.log that survives rotation: the inode
// of the file the offset belongs to, and an offset in it. Ino 0 means
// unknown (an offset recorded by v0.1 or passed without --log-inode):
// the file is then main.log by assumption. Off may point inside a line
// (a size taken while the app writes); reading then starts at the next
// line.
type LogCursor struct {
	Ino uint64 `json:"ino,omitempty"`
	Off int64  `json:"off"`
}

// lineFunc receives one record line: its time (zero when unknown) and
// its message (the bytes after the header). Continuation lines are not
// passed. msg is only valid during the call.
type lineFunc func(t time.Time, msg []byte)

// openFile is one log file, open, with the identity of the descriptor.
type openFile struct {
	f    *os.File
	name string // "main.log", "main1.log", …
	idx  int    // 0 for main.log, k for main<k>.log
	ino  uint64
	size int64
}

func closeSet(set []openFile) {
	for _, o := range set {
		_ = o.f.Close()
	}
}

// location returns the time zone of the log's local timestamps.
func (l fileLog) location() *time.Location {
	if l.loc == nil {
		return time.Local
	}
	return l.loc
}

// openSet opens main.log and main1..4.log newest first and fstats each
// open descriptor. A symlink or non-regular file is skipped (Lstat before
// open, and the open file must be regular). The listing is checked
// against the open descriptors: when an Lstat inode differs from the
// fstat inode of the same name (a rotation happened in between), every
// file is closed and the set is opened once more; a second mismatch
// returns raced = true with the set from the second attempt. The caller
// closes the set.
func (l fileLog) openSet() (set []openFile, raced bool, err error) {
	for attempt := 0; ; attempt++ {
		set, mismatch, err := l.openOnce()
		if err != nil {
			return nil, false, err
		}
		if !mismatch {
			return set, false, nil
		}
		if attempt == 1 {
			return set, true, nil
		}
		closeSet(set)
	}
}

func (l fileLog) openOnce() ([]openFile, bool, error) {
	var listed [maxRotated + 1]uint64
	var present [maxRotated + 1]bool
	for k, n := range logNames {
		fi, err := os.Lstat(filepath.Join(l.dir, n))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("app: %s: %w", n, err)
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		listed[k], present[k] = fileIno(fi), true
	}
	if l.afterList != nil {
		l.afterList()
	}
	var set []openFile
	mismatch := false
	for k, n := range logNames {
		if !present[k] {
			continue
		}
		f, err := os.OpenFile(filepath.Join(l.dir, n), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP) {
				mismatch = true
				continue
			}
			closeSet(set)
			return nil, false, fmt.Errorf("app: %s: %w", n, err)
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			closeSet(set)
			return nil, false, fmt.Errorf("app: %s: %w", n, err)
		}
		if !fi.Mode().IsRegular() {
			_ = f.Close()
			mismatch = true
			continue
		}
		ino := fileIno(fi)
		if ino != listed[k] {
			mismatch = true
		}
		set = append(set, openFile{f: f, name: n, idx: k, ino: ino, size: fi.Size()})
	}
	return set, mismatch, nil
}

// fileAt returns the open file of main<idx>.log, or nil.
func fileAt(set []openFile, idx int) *openFile {
	for i := range set {
		if set[i].idx == idx {
			return &set[i]
		}
	}
	return nil
}

// segment is a byte range of one open file to read.
type segment struct {
	file       *openFile
	start, end int64
	// final: a rotated file no longer grows, so its last line counts
	// even without a trailing newline.
	final bool
}

func segLen(s []segment) int64 {
	var n int64
	for _, x := range s {
		n += x.end - x.start
	}
	return n
}

// trimSegments keeps the last budget bytes of segs; trimmed reports
// whether anything was dropped.
func trimSegments(segs []segment, budget int64) ([]segment, bool) {
	excess := segLen(segs) - budget
	if excess <= 0 {
		return segs, false
	}
	for len(segs) > 0 && excess >= segs[0].end-segs[0].start {
		excess -= segs[0].end - segs[0].start
		segs = segs[1:]
	}
	if len(segs) > 0 {
		segs[0].start += excess
	}
	return segs, true
}

// readHooks are optional callbacks of a segment read.
type readHooks struct {
	// onFile is called before the first line of each file.
	onFile func()
	// since drops record lines whose known time is before since-1s.
	since time.Time
}

// readSegments reads segs in order and calls fn for every record line. It
// returns the cursor after the last complete line of the last segment
// (the zero cursor when segs is empty).
func (l fileLog) readSegments(ctx context.Context, segs []segment, h readHooks, fn lineFunc) (LogCursor, error) {
	loc := l.location()
	cutoff := time.Time{}
	if !h.since.IsZero() {
		cutoff = h.since.Add(-time.Second)
	}
	emit := func(line []byte) {
		t, m, ok := parseHeader(line, loc)
		if !ok {
			return
		}
		if !cutoff.IsZero() && !t.IsZero() && t.Before(cutoff) {
			return
		}
		fn(t, line[m:])
	}
	var end LogCursor
	for _, s := range segs {
		if h.onFile != nil {
			h.onFile()
		}
		skipFirst := false
		if s.start > 0 {
			var b [1]byte
			if _, err := s.file.f.ReadAt(b[:], s.start-1); err != nil {
				return end, fmt.Errorf("app: %s: %w", s.file.name, err)
			}
			l.read(1)
			skipFirst = b[0] != '\n'
		}
		r := bufio.NewReaderSize(countingReader{r: io.NewSectionReader(s.file.f, s.start, s.end-s.start), l: l}, maxLineLen)
		next, err := readLines(ctx, r, s.start, skipFirst, s.final, emit)
		if err != nil {
			return end, err
		}
		end = LogCursor{Ino: s.file.ino, Off: next}
	}
	return end, nil
}

// read reports n bytes read to the test hook.
func (l fileLog) read(n int) {
	if l.onRead != nil && n > 0 {
		l.onRead(n)
	}
}

// countingReader reports every read to fileLog.onRead.
type countingReader struct {
	r io.Reader
	l fileLog
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.l.read(n)
	return n, err
}

// readFrom calls fn for every record line after cur, across rotation,
// and returns the cursor after the last complete line of main.log. gap
// is true when lines may have been missed: the file of cur was rotated
// out of the set or truncated, the catch-up budget was exceeded, or
// openSet raced twice.
//
// main.log can be missing for a moment (the app renamed it to main1.log
// and has not created the new one yet) or be skipped (a symlink). The
// cursor then stays where the reader can find it again: a cursor on a
// rotated file reads that file and the newer rotated ones and returns a
// cursor on the newest of them; any other cursor is returned unchanged.
// The next call then catches up through rule 3 instead of reading the
// new main.log from 0 and losing what the old one received.
func (l fileLog) readFrom(ctx context.Context, cur LogCursor, fn lineFunc) (next LogCursor, gap bool, err error) {
	set, raced, err := l.openSet()
	if err != nil {
		return cur, false, err
	}
	defer closeSet(set)
	gap = raced
	main := fileAt(set, 0)
	clamp := func(off, size int64) (int64, bool) {
		switch {
		case off < 0:
			return 0, false
		case off > size:
			return 0, true
		}
		return off, false
	}
	// rotatedIdx is k in 1..4 with ino(main<k>.log) == cur.Ino, or 0.
	rotatedIdx := 0
	if cur.Ino != 0 && (main == nil || cur.Ino != main.ino) {
		for i := range set {
			if set[i].idx > 0 && set[i].ino == cur.Ino {
				rotatedIdx = set[i].idx
			}
		}
	}
	var segs []segment
	switch {
	case main == nil && rotatedIdx == 0:
		// Nothing to read yet. A cursor on a file that is gone (not
		// main.log, not rotated) may have missed lines.
		return cur, gap || cur.Ino != 0, nil
	case rotatedIdx == 0 && cur.Ino == 0:
		off, _ := clamp(cur.Off, main.size)
		segs = []segment{{file: main, start: off, end: main.size}}
	case rotatedIdx == 0 && cur.Ino == main.ino:
		off, truncated := clamp(cur.Off, main.size)
		gap = gap || truncated
		segs = []segment{{file: main, start: off, end: main.size}}
	case rotatedIdx == 0:
		segs = []segment{{file: main, start: 0, end: main.size}}
		gap = true
	default: // main.log may be missing here
		fk := fileAt(set, rotatedIdx)
		off, truncated := clamp(cur.Off, fk.size)
		gap = gap || truncated
		segs = append(segs, segment{file: fk, start: off, end: fk.size, final: true})
		for j := rotatedIdx - 1; j >= 1; j-- {
			f := fileAt(set, j)
			if f == nil {
				gap = true
				continue
			}
			segs = append(segs, segment{file: f, start: 0, end: f.size, final: true})
		}
		if main != nil {
			segs = append(segs, segment{file: main, start: 0, end: main.size})
		}
	}
	if cur.Ino != 0 && (main == nil || cur.Ino != main.ino) {
		var trimmed bool
		segs, trimmed = trimSegments(segs, l.budgetOr(catchUpMax))
		gap = gap || trimmed
	}
	end, err := l.readSegments(ctx, segs, readHooks{}, fn)
	if err != nil {
		return cur, gap, err
	}
	if end.Ino == 0 {
		// Nothing was read (every segment trimmed away): stay on the
		// newest file of the set.
		if main != nil {
			return LogCursor{Ino: main.ino, Off: main.size}, gap, nil
		}
		return cur, gap, nil
	}
	return end, gap, nil
}

// windowOpts selects the lines of a window read.
type windowOpts struct {
	// Since: read back to the first file whose first record time is at
	// or before Since, and ignore record lines older than Since-1s.
	// Zero: read files newest first while the budget lasts.
	Since time.Time
	// OnFile, when set, is called before the first line of each file
	// (the out-of-order guard works per file).
	OnFile func()
}

// budgetOr returns the test budget when set, else def.
func (l fileLog) budgetOr(def int64) int64 {
	if l.budget > 0 {
		return l.budget
	}
	return def
}

// readWindow calls fn for the record lines of the window, oldest first.
// truncated is true when it stopped on the budget or ran out of files
// before reaching Since.
func (l fileLog) readWindow(ctx context.Context, o windowOpts, fn lineFunc) (end LogCursor, truncated bool, err error) {
	set, _, err := l.openSet()
	if err != nil {
		return LogCursor{}, false, err
	}
	defer closeSet(set)
	if len(set) == 0 {
		return LogCursor{}, false, nil
	}
	remaining := l.budgetOr(windowBytes)
	reached := o.Since.IsZero()
	var chosen []segment // newest first
	for i := range set {
		f := &set[i]
		if remaining <= 0 {
			truncated = true
			break
		}
		start := int64(0)
		if f.size > remaining {
			start = f.size - remaining
		}
		chosen = append(chosen, segment{file: f, start: start, end: f.size, final: f.idx > 0})
		remaining -= f.size - start
		if start > 0 {
			truncated = true
			break
		}
		if !o.Since.IsZero() {
			if ft, ok := l.firstTime(f); ok && !ft.After(o.Since) {
				reached = true
				break
			}
		}
	}
	// Running out of files means lines were rotated out only when the
	// oldest file the app keeps exists; with fewer files the whole log
	// is still there.
	if !reached && !truncated && fileAt(set, maxRotated) != nil {
		truncated = true
	}
	segs := make([]segment, 0, len(chosen))
	for i := len(chosen) - 1; i >= 0; i-- {
		segs = append(segs, chosen[i])
	}
	// end is after the newest file read: main.log, or the newest rotated
	// file while main.log is missing (readFrom then catches up from it).
	end, err = l.readSegments(ctx, segs, readHooks{onFile: o.OnFile, since: o.Since}, fn)
	if err != nil {
		return LogCursor{}, truncated, err
	}
	return end, truncated, nil
}

// firstTime returns the time of the first record line with a known time
// in the first firstTimeProbe bytes of f.
func (l fileLog) firstTime(f *openFile) (time.Time, bool) {
	limit := min(f.size, int64(firstTimeProbe))
	buf := make([]byte, 0, firstTimeStep)
	loc := l.location()
	var off int64
	for off < limit {
		n := min(int64(firstTimeStep), limit-off)
		chunk := make([]byte, n)
		got, _ := f.f.ReadAt(chunk, off)
		l.read(got)
		if got == 0 {
			return time.Time{}, false
		}
		buf = append(buf, chunk[:got]...)
		off += int64(got)
		for {
			nl := indexNL(buf)
			if nl < 0 {
				break
			}
			if t, _, ok := parseHeader(buf[:nl], loc); ok && !t.IsZero() {
				return t, true
			}
			buf = buf[nl+1:]
		}
	}
	return time.Time{}, false
}

func indexNL(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}
