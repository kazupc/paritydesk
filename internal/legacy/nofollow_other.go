//go:build !unix

package legacy

// openFlags: no-follow and non-blocking opens are unavailable on this
// platform; ReadHint still checks the file type on the descriptor.
const openFlags = 0
