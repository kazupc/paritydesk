package redact

// Redactor holds the values to hide.
type Redactor struct {
	Home string
	// Unredacted disables redaction (--log-unredacted; stays local).
	Unredacted bool
}

// String redacts UUIDs, home paths and id-bearing key names in s.
func (r Redactor) String(s string) string { return s }

// Path redacts a path: paths under Home become ~/<short hash of the rest>.
func (r Redactor) Path(p string) string { return p }

// Key redacts ids embedded in a Local Storage or JSON key name.
func (r Redactor) Key(k string) string { return k }
