//go:build !darwin && !linux

package cli

// isTTY is false where no termios test is wired: the commands that need a
// person at a terminal refuse there.
func isTTY(any) bool { return false }
