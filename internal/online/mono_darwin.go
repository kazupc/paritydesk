//go:build darwin

package online

import (
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/kazupc/paritydesk/internal/ids"
)

// Now reads CLOCK_MONOTONIC (it keeps counting during sleep on darwin)
// and kern.bootsessionuuid.
func (SystemMono) Now() (MonoNow, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return MonoNow{}, fmt.Errorf("online: monotonic clock: %w", err)
	}
	boot, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return MonoNow{}, fmt.Errorf("online: boot session id: %w", err)
	}
	if !ids.IsUUID(lower(boot)) {
		return MonoNow{}, fmt.Errorf("online: boot session id is not a UUID")
	}
	return MonoNow{BootID: lower(boot), NS: ts.Nano(), OK: true}, nil
}

// lower lower-cases ASCII letters.
func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
