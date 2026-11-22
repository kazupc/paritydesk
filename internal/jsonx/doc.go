// Package jsonx edits JSON documents by literal key paths and byte splices.
//
// Paths are arrays of literal object keys; there is no query syntax, so keys
// containing ".", "*", "?", "#", "|", "@" or "\" are addressed exactly
// (design H8). Edits replace the bytes of one value and leave every other
// byte of the document untouched: no re-encoding, no key reordering, no
// float changes. Documents with duplicate keys in any object are refused.
// Errors carry byte offsets only, never document content.
package jsonx
