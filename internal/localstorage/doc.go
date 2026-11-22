// Package localstorage encodes and decodes Chromium Local Storage keys and
// values as stored in the claude.ai origin's LevelDB, and edits the JSON
// envelopes the app keeps inside those values.
//
// Format (observed on disk, matching the proven legacy tooling):
//
//   - key   = "_" + origin + "\x00" + encoded(name)
//   - value = encoded(text)
//   - encoded(s) = one flag byte followed by the text: 0x01 and Latin-1 bytes
//     when every UTF-16 code unit is <= 0xFF, otherwise 0x00 and UTF-16LE.
//
// Values are kept as raw bytes. A value may be decoded, edited and
// re-encoded only if encode(decode(x)) == x; otherwise the row that needs
// it is refused (design H9). Lone surrogates therefore survive untouched.
package localstorage
