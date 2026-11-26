package app

import (
	"bytes"
	"time"
)

// invalidClock starts the timestamp the app writes when its clock is
// invalid ("invalid-clock (epoch-ms N)").
var invalidClock = []byte("invalid-clock (epoch-ms ")

// parseHeader recognizes a record line:
//
//	["["] TS ["]"] " [" LEVEL "] " MESSAGE
//	TS    = YYYY-MM-DD ("T"|" ") HH:MM:SS ["." 1*9DIGIT] ["Z"]
//	      | "invalid-clock (epoch-ms " 1*16DIGIT ")"
//	LEVEL = "error" | "warn" | "info" | "http" | "verbose" | "debug" | "silly"
//
// An opening "[" requires the closing "]". Without "Z" the time is in
// loc. For invalid-clock, t is zero (time unknown). msg is the offset of
// the message. ok is false for every other line (a continuation line). It
// allocates nothing.
func parseHeader(line []byte, loc *time.Location) (t time.Time, msg int, ok bool) {
	i := 0
	bracket := len(line) > 0 && line[0] == '['
	if bracket {
		i = 1
	}
	if bytes.HasPrefix(line[i:], invalidClock) {
		i += len(invalidClock)
		n := digitsAt(line, i, 16)
		if n == 0 || i+n >= len(line) || line[i+n] != ')' {
			return time.Time{}, 0, false
		}
		i += n + 1
	} else {
		var ok bool
		if t, i, ok = parseStamp(line, i, loc); !ok {
			return time.Time{}, 0, false
		}
	}
	if bracket {
		if i >= len(line) || line[i] != ']' {
			return time.Time{}, 0, false
		}
		i++
	}
	if i+2 > len(line) || line[i] != ' ' || line[i+1] != '[' {
		return time.Time{}, 0, false
	}
	i += 2
	end := bytes.IndexByte(line[i:min(len(line), i+8)], ']')
	if end < 0 {
		return time.Time{}, 0, false
	}
	switch string(line[i : i+end]) {
	case "error", "warn", "info", "http", "verbose", "debug", "silly":
	default:
		return time.Time{}, 0, false
	}
	i += end + 1
	if i >= len(line) || line[i] != ' ' {
		return time.Time{}, 0, false
	}
	return t, i + 1, true
}

// parseStamp parses YYYY-MM-DD("T"|" ")HH:MM:SS[.fraction][Z] at line[i:]
// and returns the time and the offset after it.
func parseStamp(line []byte, i int, loc *time.Location) (time.Time, int, bool) {
	const width = len("2006-01-02 15:04:05")
	if i+width > len(line) {
		return time.Time{}, 0, false
	}
	s := line[i : i+width]
	if s[4] != '-' || s[7] != '-' || (s[10] != ' ' && s[10] != 'T') || s[13] != ':' || s[16] != ':' {
		return time.Time{}, 0, false
	}
	year, ok1 := num(s, 0, 4)
	month, ok2 := num(s, 5, 2)
	day, ok3 := num(s, 8, 2)
	hour, ok4 := num(s, 11, 2)
	minute, ok5 := num(s, 14, 2)
	sec, ok6 := num(s, 17, 2)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 ||
		month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || sec > 59 {
		return time.Time{}, 0, false
	}
	i += width
	nsec := 0
	if i < len(line) && line[i] == '.' {
		n := digitsAt(line, i+1, 9)
		if n == 0 {
			return time.Time{}, 0, false
		}
		ms := 0
		for k := 0; k < 3; k++ {
			ms *= 10
			if k < n {
				ms += int(line[i+1+k] - '0')
			}
		}
		nsec = ms * int(time.Millisecond)
		i += 1 + n
	}
	if i < len(line) && line[i] == 'Z' {
		loc = time.UTC
		i++
	}
	if loc == nil {
		loc = time.Local
	}
	return time.Date(year, time.Month(month), day, hour, minute, sec, nsec, loc), i, true
}

// num parses n decimal digits of s at off.
func num(s []byte, off, n int) (int, bool) {
	v := 0
	for k := off; k < off+n; k++ {
		c := s[k]
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int(c-'0')
	}
	return v, true
}

// digitsAt counts the decimal digits at line[i:], up to limit; it returns 0
// when there are more than limit.
func digitsAt(line []byte, i, limit int) int {
	n := 0
	for i+n < len(line) && line[i+n] >= '0' && line[i+n] <= '9' {
		n++
		if n > limit {
			return 0
		}
	}
	return n
}
