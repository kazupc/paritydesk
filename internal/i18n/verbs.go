package i18n

import (
	"errors"
	"fmt"
	"strings"
)

// Verb is one formatting directive.
type Verb struct {
	Spec string // flags, width and precision plus the verb rune, e.g. "-13s"
}

// verbRunes are the fmt verbs a catalog format may use.
const verbRunes = "vTtbcdoOqxXUeEfFgGsp"

// Verbs parses a format and returns, for each argument number (1-based),
// the directive that consumes it, and explicit=true if any explicit index
// ([n]) is used. "%%" is skipped. "*" width/precision, a bad verb, an
// argument used twice, a gap in the argument numbers, or a lone '%' are
// errors.
func Verbs(format string) (verbs map[int]Verb, explicit bool, err error) {
	verbs = map[int]Verb{}
	next := 1
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		if i >= len(format) {
			return nil, explicit, errors.New("lone % at the end")
		}
		if format[i] == '%' {
			continue
		}
		var spec strings.Builder
		for i < len(format) && strings.IndexByte("+-# 0", format[i]) >= 0 {
			spec.WriteByte(format[i])
			i++
		}
		n, ok, j, e := argIndex(format, i)
		if e != nil {
			return nil, explicit, e
		}
		if ok {
			explicit, next, i = true, n, j
		}
		for i < len(format) && format[i] >= '0' && format[i] <= '9' {
			spec.WriteByte(format[i])
			i++
		}
		if i < len(format) && format[i] == '*' {
			return nil, explicit, errors.New("* width is not allowed")
		}
		if i < len(format) && format[i] == '.' {
			spec.WriteByte('.')
			i++
			for i < len(format) && format[i] >= '0' && format[i] <= '9' {
				spec.WriteByte(format[i])
				i++
			}
			if i < len(format) && format[i] == '*' {
				return nil, explicit, errors.New("* precision is not allowed")
			}
		}
		n, ok, j, e = argIndex(format, i)
		if e != nil {
			return nil, explicit, e
		}
		if ok {
			explicit, next, i = true, n, j
		}
		if i >= len(format) {
			return nil, explicit, errors.New("directive without a verb")
		}
		if strings.IndexByte(verbRunes, format[i]) < 0 {
			return nil, explicit, fmt.Errorf("bad verb %q", format[i])
		}
		spec.WriteByte(format[i])
		if _, dup := verbs[next]; dup {
			return nil, explicit, fmt.Errorf("argument %d used twice", next)
		}
		verbs[next] = Verb{Spec: spec.String()}
		next++
	}
	for k := 1; k <= len(verbs); k++ {
		if _, ok := verbs[k]; !ok {
			return nil, explicit, fmt.Errorf("argument %d is never used", k)
		}
	}
	return verbs, explicit, nil
}

// argIndex parses an explicit argument index "[n]" at i.
func argIndex(format string, i int) (n int, ok bool, end int, err error) {
	if i >= len(format) || format[i] != '[' {
		return 0, false, i, nil
	}
	j := i + 1
	for j < len(format) && format[j] >= '0' && format[j] <= '9' {
		n = n*10 + int(format[j]-'0')
		j++
	}
	if j >= len(format) || format[j] != ']' || j == i+1 || n < 1 {
		return 0, false, i, errors.New("bad argument index")
	}
	return n, true, j + 1, nil
}

// Compatible reports whether tr may replace en: Verbs(en) succeeds without
// explicit indexes, Verbs(tr) succeeds, both use the same argument
// numbers, and each argument has the same Spec.
func Compatible(en, tr string) bool {
	ev, explicit, err := Verbs(en)
	if err != nil || explicit {
		return false
	}
	tv, _, err := Verbs(tr)
	if err != nil || len(ev) != len(tv) {
		return false
	}
	for k, v := range ev {
		if tv[k] != v {
			return false
		}
	}
	return true
}
