package legacy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// errJSONShape is returned by checkJSON for anything but exactly one JSON
// object within the depth limit.
var errJSONShape = errors.New("legacy: not one JSON object within the nesting limit")

// checkJSON reports whether b holds exactly one JSON object whose nesting
// depth is at most maxDepth (the top-level object counts as 1), with
// nothing but white space after it. It walks tokens, so a deep document is
// refused without being decoded into memory.
func checkJSON(b []byte, maxDepth int) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	depth := 0
	first := true
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			if depth != 0 || first {
				return errJSONShape
			}
			return nil
		}
		if err != nil {
			return errJSONShape
		}
		d, isDelim := tok.(json.Delim)
		if first {
			if !isDelim || d != '{' {
				return errJSONShape
			}
			first = false
		} else if depth == 0 {
			// Trailing data after the top-level object.
			return errJSONShape
		}
		if !isDelim {
			continue
		}
		switch d {
		case '{', '[':
			depth++
			if depth > maxDepth {
				return errJSONShape
			}
		case '}', ']':
			depth--
		}
	}
}
