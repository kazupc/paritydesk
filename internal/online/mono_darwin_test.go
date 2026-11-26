//go:build darwin

package online

import (
	"testing"

	"github.com/kazupc/paritydesk/internal/ids"
)

func TestSystemMonoDarwin(t *testing.T) {
	a, err := SystemMono{}.Now()
	if err != nil {
		t.Fatal(err)
	}
	b, err := SystemMono{}.Now()
	if err != nil {
		t.Fatal(err)
	}
	if b.NS < a.NS {
		t.Fatalf("monotonic clock went back: %d then %d", a.NS, b.NS)
	}
	if !ids.IsUUID(a.BootID) || a.BootID != b.BootID {
		t.Fatalf("boot id %q / %q", a.BootID, b.BootID)
	}
	if n := Read(SystemMono{}); !n.OK {
		t.Fatal("Read: not OK")
	}
}
