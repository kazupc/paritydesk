package app

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// collect returns a lineFunc that records messages, and the list.
func collect() (lineFunc, *[]string) {
	var out []string
	return func(_ time.Time, msg []byte) { out = append(out, string(msg)) }, &out
}

// msgs builds stamped lines "<prefix> 1" … "<prefix> n" and their
// messages.
func msgs(prefix string, n int) (lines, want []string) {
	for i := 1; i <= n; i++ {
		m := fmt.Sprintf("%s %d", prefix, i)
		lines, want = append(lines, stamp(m)), append(want, m)
	}
	return lines, want
}

func TestReadFromRotation(t *testing.T) {
	ctx := t.Context()
	t.Run("rotated once", func(t *testing.T) {
		l := newTestLog(t)
		old, _ := msgs("old", 3)
		l.append(old...)
		cur, _ := l.log().Cursor()
		a, wantA := msgs("a", 2)
		l.append(a...)
		l.rotate()
		b, wantB := msgs("b", 2)
		l.append(b...)
		fn, got := collect()
		next, gap, err := l.log().readFrom(ctx, cur, fn)
		if err != nil || gap {
			t.Fatalf("gap %v err %v", gap, err)
		}
		if want := append(wantA, wantB...); !slices.Equal(*got, want) {
			t.Fatalf("got %q, want %q", *got, want)
		}
		end, _ := l.log().Cursor()
		if next != end {
			t.Fatalf("next %+v, want %+v", next, end)
		}
		// Reading again from next gives nothing new.
		fn, got = collect()
		if _, gap, err := l.log().readFrom(ctx, next, fn); err != nil || gap || len(*got) != 0 {
			t.Fatalf("again: %q %v %v", *got, gap, err)
		}
	})
	t.Run("rotated twice, cursor in main2.log", func(t *testing.T) {
		l := newTestLog(t)
		l.append(stamp("old"))
		cur, _ := l.log().Cursor()
		var want []string
		for _, p := range []string{"x", "y", "z"} {
			lines, w := msgs(p, 2)
			l.append(lines...)
			want = append(want, w...)
			if p != "z" {
				l.rotate()
			}
		}
		fn, got := collect()
		_, gap, err := l.log().readFrom(ctx, cur, fn)
		if err != nil || gap || !slices.Equal(*got, want) {
			t.Fatalf("got %q gap %v err %v, want %q", *got, gap, err, want)
		}
	})
	t.Run("a rotated file's last line without a newline counts", func(t *testing.T) {
		l := newTestLog(t)
		l.append(stamp("old"))
		cur, _ := l.log().Cursor()
		l.append(strings.TrimSuffix(stamp("tail"), "\n"))
		l.rotate()
		l.append(stamp("new"))
		fn, got := collect()
		if _, gap, err := l.log().readFrom(ctx, cur, fn); err != nil || gap || !slices.Equal(*got, []string{"tail", "new"}) {
			t.Fatalf("got %q gap %v err %v", *got, gap, err)
		}
	})
	t.Run("rotated out", func(t *testing.T) {
		l := newTestLog(t)
		l.append(stamp("old"))
		cur, _ := l.log().Cursor()
		for i := 0; i < maxRotated+1; i++ {
			l.rotate()
			l.append(stamp(fmt.Sprintf("r%d", i)))
		}
		fn, got := collect()
		_, gap, err := l.log().readFrom(ctx, cur, fn)
		if err != nil || !gap || !slices.Equal(*got, []string{"r4"}) {
			t.Fatalf("got %q gap %v err %v", *got, gap, err)
		}
	})
	t.Run("truncated in place", func(t *testing.T) {
		l := newTestLog(t)
		lines, _ := msgs("a", 5)
		l.append(lines...)
		cur, _ := l.log().Cursor()
		if err := os.Truncate(l.path("main.log"), 0); err != nil {
			t.Fatal(err)
		}
		l.append(stamp("after"))
		fn, got := collect()
		_, gap, err := l.log().readFrom(ctx, cur, fn)
		if err != nil || !gap || !slices.Equal(*got, []string{"after"}) {
			t.Fatalf("got %q gap %v err %v", *got, gap, err)
		}
	})
	t.Run("legacy cursor without inode", func(t *testing.T) {
		l := newTestLog(t)
		l.append(stamp("old"))
		size, _ := l.log().Size()
		l.append(stamp("new"))
		fn, got := collect()
		_, gap, err := l.log().readFrom(ctx, LogCursor{Off: size}, fn)
		if err != nil || gap || !slices.Equal(*got, []string{"new"}) {
			t.Fatalf("got %q gap %v err %v", *got, gap, err)
		}
	})
	t.Run("cursor taken mid-line skips the partial line", func(t *testing.T) {
		l := newTestLog(t)
		l.append(stamp("first"), "[2026-01-01 00:00:00.000] [info] partial ")
		cur, _ := l.log().Cursor()
		l.append("rest of it\n", stamp("next"))
		fn, got := collect()
		_, gap, err := l.log().readFrom(ctx, cur, fn)
		if err != nil || gap || !slices.Equal(*got, []string{"next"}) {
			t.Fatalf("got %q gap %v err %v", *got, gap, err)
		}
	})
	t.Run("main.log missing between rename and create", func(t *testing.T) {
		l := newTestLog(t)
		l.append(stamp("old"))
		cur, _ := l.log().Cursor()
		l.append(stamp("a 1"))
		l.rotate() // main.log -> main1.log; the new main.log does not exist yet
		fn, got := collect()
		next, gap, err := l.log().readFrom(ctx, cur, fn)
		if err != nil || gap || !slices.Equal(*got, []string{"a 1"}) {
			t.Fatalf("during: got %q gap %v err %v", *got, gap, err)
		}
		if next.Ino != cur.Ino {
			t.Fatalf("cursor left the rotated file: %+v, want ino %d", next, cur.Ino)
		}
		l.append(stamp("b 1"))
		fn, got = collect()
		next, gap, err = l.log().readFrom(ctx, next, fn)
		if err != nil || gap || !slices.Equal(*got, []string{"b 1"}) {
			t.Fatalf("after: got %q gap %v err %v", *got, gap, err)
		}
		if end, _ := l.log().Cursor(); next != end {
			t.Fatalf("next %+v, want %+v", next, end)
		}
	})
	t.Run("main.log missing keeps a cursor the reader cannot place", func(t *testing.T) {
		l := newTestLog(t)
		for _, c := range []LogCursor{{}, {Off: 12}} {
			fn, got := collect()
			next, gap, err := l.log().readFrom(ctx, c, fn)
			if err != nil || gap || next != c || len(*got) != 0 {
				t.Fatalf("%+v: next %+v gap %v err %v got %q", c, next, gap, err, *got)
			}
		}
		// A cursor on a file that is gone may have missed lines.
		fn, _ := collect()
		c := LogCursor{Ino: 1 << 40, Off: 5}
		if next, gap, err := l.log().readFrom(ctx, c, fn); err != nil || !gap || next != c {
			t.Fatalf("gone: next %+v gap %v err %v", next, gap, err)
		}
	})
	t.Run("window read while main.log is missing", func(t *testing.T) {
		l := newTestLog(t)
		l.append(stamp("a 1"))
		l.rotate()
		fn, got := collect()
		end, _, err := l.log().readWindow(ctx, windowOpts{}, fn)
		if err != nil || !slices.Equal(*got, []string{"a 1"}) || end.Ino == 0 {
			t.Fatalf("window: end %+v got %q err %v", end, *got, err)
		}
		l.append(stamp("b 1"))
		fn, got = collect()
		if _, gap, err := l.log().readFrom(ctx, end, fn); err != nil || gap || !slices.Equal(*got, []string{"b 1"}) {
			t.Fatalf("after: got %q gap %v err %v", *got, gap, err)
		}
	})
	t.Run("catch-up budget", func(t *testing.T) {
		l := newTestLog(t)
		l.append(stamp("old"))
		cur, _ := l.log().Cursor()
		big, _ := msgs("big", 200)
		l.append(big...)
		l.rotate()
		l.append(stamp("new 1"), stamp("new 2"))
		lg := l.log()
		lg.budget = int64(len(stamp("new 1")) + 10)
		fn, got := collect()
		_, gap, err := lg.readFrom(ctx, cur, fn)
		if err != nil || !gap || !slices.Equal(*got, []string{"new 2"}) {
			t.Fatalf("got %q gap %v err %v", *got, gap, err)
		}
	})
}

// TestOpenSetRace: a rotation between listing and opening makes openSet
// open the set again; a rotation on every attempt gives raced, and
// readFrom reports a gap.
func TestOpenSetRace(t *testing.T) {
	ctx := t.Context()
	l := newTestLog(t)
	l.append(stamp("before"))
	cur, _ := l.log().Cursor()
	l.append(stamp("a"))
	once := false
	lg := l.log()
	lg.afterList = func() {
		if !once {
			once = true
			l.rotate()
			l.append(stamp("b"))
		}
	}
	fn, got := collect()
	_, gap, err := lg.readFrom(ctx, cur, fn)
	if err != nil || gap || !slices.Equal(*got, []string{"a", "b"}) {
		t.Fatalf("got %q gap %v err %v", *got, gap, err)
	}
	n := 0
	lg.afterList = func() {
		n++
		l.rotate()
		l.append(stamp(fmt.Sprintf("c%d", n)))
	}
	set, raced, err := lg.openSet()
	closeSet(set)
	if err != nil || !raced {
		t.Fatalf("raced %v err %v", raced, err)
	}
	cur, _ = l.log().Cursor()
	if _, gap, err := lg.readFrom(ctx, cur, func(time.Time, []byte) {}); err != nil || !gap {
		t.Fatalf("gap %v err %v", gap, err)
	}
}

func TestOpenSetSkipsSymlinks(t *testing.T) {
	l := newTestLog(t)
	other := t.TempDir() + "/elsewhere.log"
	if err := os.WriteFile(other, []byte(stamp("[bridge] peer up")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, l.path("main.log")); err != nil {
		t.Fatal(err)
	}
	evs, _, err := l.log().Events(t.Context(), 0, testSig, "")
	if err != nil || len(evs) != 0 {
		t.Fatalf("a symlinked main.log was read: %v %v", evs, err)
	}
}

func TestReadWindowSince(t *testing.T) {
	ctx := t.Context()
	l := newTestLog(t)
	start := time.Date(2026, 11, 26, 12, 0, 0, 0, testZone)
	l.append(at(start.Add(-3*time.Hour), "older file"))
	l.rotate()
	l.append(at(start.Add(-time.Hour), "earlier run"), at(start, "run start"), at(start.Add(time.Minute), "in run 1"))
	l.rotate()
	l.append(at(start.Add(2*time.Minute), "in run 2"))
	fn, got := collect()
	end, truncated, err := l.log().readWindow(ctx, windowOpts{Since: start}, fn)
	if err != nil || truncated {
		t.Fatalf("truncated %v err %v", truncated, err)
	}
	if want := []string{"run start", "in run 1", "in run 2"}; !slices.Equal(*got, want) {
		t.Fatalf("got %q, want %q", *got, want)
	}
	if c, _ := l.log().Cursor(); end != c {
		t.Fatalf("end %+v, want %+v", end, c)
	}
	// Zero since: the whole set within the budget, oldest first.
	fn, got = collect()
	if _, truncated, err := l.log().readWindow(ctx, windowOpts{}, fn); err != nil || truncated || len(*got) != 5 || (*got)[0] != "older file" {
		t.Fatalf("zero since: %q %v %v", *got, truncated, err)
	}
	// A small budget stops early.
	fn, got = collect()
	small := l.log()
	small.budget = 40
	_, truncated, err = small.readWindow(ctx, windowOpts{Since: start}, fn)
	if err != nil || !truncated {
		t.Fatalf("budget: truncated %v err %v", truncated, err)
	}
	// Since before every file, with all five files present: older lines
	// were rotated out.
	l2 := newTestLog(t)
	for i := 0; i < maxRotated+1; i++ {
		l2.append(at(start.Add(time.Duration(i)*time.Minute), fmt.Sprintf("f%d", i)))
		if i < maxRotated {
			l2.rotate()
		}
	}
	if _, truncated, err := l2.log().readWindow(ctx, windowOpts{Since: start.Add(-time.Hour)}, func(time.Time, []byte) {}); err != nil || !truncated {
		t.Fatalf("rotated out: truncated %v err %v", truncated, err)
	}
	// With fewer files, nothing was rotated out.
	l3 := newTestLog(t)
	l3.append(at(start, "only"))
	if _, truncated, err := l3.log().readWindow(ctx, windowOpts{Since: start.Add(-time.Hour)}, func(time.Time, []byte) {}); err != nil || truncated {
		t.Fatalf("fresh log: truncated %v err %v", truncated, err)
	}
	// No log at all.
	if _, truncated, err := newTestLog(t).log().readWindow(ctx, windowOpts{Since: start}, func(time.Time, []byte) {}); err != nil || truncated {
		t.Fatalf("no log: truncated %v err %v", truncated, err)
	}
}
