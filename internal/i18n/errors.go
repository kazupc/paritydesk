package i18n

// Texter is implemented by errors whose text is a catalog message.
type Texter interface{ I18nText() Text }

// Error is an error whose text is a catalog message. Error() is the
// rendering, byte-identical to the errors.New/fmt.Errorf text it
// replaces; Unwrap returns Base so errors.Is keeps working.
type Error struct {
	T    Text
	Base error // optional
}

// NewErr returns an error with the text of id; for package-level
// sentinels.
func NewErr(id Msg, args ...any) *Error { return &Error{T: Tx(id, args...)} }

// WrapErr returns an error with text t that unwraps to base; for
// "%w (extra)" return sites. t's rendering must already hold the
// base text.
func WrapErr(base error, t Text) *Error { return &Error{T: t, Base: base} }

// Error returns the text.
func (e *Error) Error() string { return e.T.String() }

// Unwrap returns the base error.
func (e *Error) Unwrap() error { return e.Base }

// I18nText returns the late-bound text.
func (e *Error) I18nText() Text { return e.T }
