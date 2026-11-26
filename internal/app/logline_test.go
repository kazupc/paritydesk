package app

import (
	"testing"
	"time"
)

func TestParseHeader(t *testing.T) {
	loc := testZone
	tests := []struct {
		name string
		line string
		ok   bool
		want time.Time // zero: unknown time
		msg  string
	}{
		{"local form", "2026-11-26 10:00:00 [info] hello", true, time.Date(2026, 11, 26, 10, 0, 0, 0, loc), "hello"},
		{"bracketed with ms", "[2026-11-26 10:00:00.123] [warn] hi there", true, time.Date(2026, 11, 26, 10, 0, 0, 123e6, loc), "hi there"},
		{"T separator", "2026-11-26T23:59:59 [error] x", true, time.Date(2026, 11, 26, 23, 59, 59, 0, loc), "x"},
		{"Z is UTC", "2026-11-26T01:02:03.5Z [debug] x", true, time.Date(2026, 11, 26, 1, 2, 3, 500e6, time.UTC), "x"},
		{"fraction truncated to ms", "2026-11-26 01:02:03.123456789 [silly] x", true, time.Date(2026, 11, 26, 1, 2, 3, 123e6, loc), "x"},
		{"invalid clock", "invalid-clock (epoch-ms 1764120000000) [info] boot", true, time.Time{}, "boot"},
		{"bracketed invalid clock", "[invalid-clock (epoch-ms 1)] [http] boot", true, time.Time{}, "boot"},
		{"verbose", "2026-11-26 10:00:00 [verbose] v", true, time.Date(2026, 11, 26, 10, 0, 0, 0, loc), "v"},
		{"empty message", "2026-11-26 10:00:00 [info] ", true, time.Date(2026, 11, 26, 10, 0, 0, 0, loc), ""},
		{"unknown level", "2026-11-26 10:00:00 [notice] x", false, time.Time{}, ""},
		{"missing closing bracket", "[2026-11-26 10:00:00.000 [info] x", false, time.Time{}, ""},
		{"closing bracket without opening", "2026-11-26 10:00:00] [info] x", false, time.Time{}, ""},
		{"continuation line", "    at Object.<anonymous> (file.js:1:2)", false, time.Time{}, ""},
		{"stderr text", "Error: something [info] x", false, time.Time{}, ""},
		{"garbage", "\x00\xff\xfe", false, time.Time{}, ""},
		{"hour 24", "2026-11-26 24:00:00 [info] x", false, time.Time{}, ""},
		{"month 13", "2026-13-26 10:00:00 [info] x", false, time.Time{}, ""},
		{"day 0", "2026-11-00 10:00:00 [info] x", false, time.Time{}, ""},
		{"minute 60", "2026-11-26 10:60:00 [info] x", false, time.Time{}, ""},
		{"no space after level", "2026-11-26 10:00:00 [info]x", false, time.Time{}, ""},
		{"empty fraction", "2026-11-26 10:00:00. [info] x", false, time.Time{}, ""},
		{"too many epoch digits", "invalid-clock (epoch-ms 12345678901234567) [info] x", false, time.Time{}, ""},
		{"short", "2026-11-26", false, time.Time{}, ""},
		{"empty", "", false, time.Time{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, msg, ok := parseHeader([]byte(tc.line), loc)
			if ok != tc.ok {
				t.Fatalf("ok %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if !got.Equal(tc.want) || got.IsZero() != tc.want.IsZero() {
				t.Errorf("time %v, want %v", got, tc.want)
			}
			if tc.line[msg:] != tc.msg {
				t.Errorf("message %q, want %q", tc.line[msg:], tc.msg)
			}
		})
	}
}

func TestParseHeaderNoAlloc(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under -race")
	}
	line := []byte("2026-11-26 10:00:00.123 [info] Sending message to session x")
	n := testing.AllocsPerRun(100, func() { _, _, _ = parseHeader(line, testZone) })
	if n != 0 {
		t.Fatalf("%v allocations", n)
	}
}
