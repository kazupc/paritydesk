package cli

import (
	"context"
	"os"
	"testing"

	"github.com/kazupc/paritydesk/internal/app"
)

// selfHolders reports one holder: this process.
type selfHolders struct{}

func (selfHolders) Holders(_ context.Context, prefixes []string) ([]app.Holder, error) {
	return []app.Holder{{PID: os.Getpid(), Path: prefixes[0] + "/x", Access: "w"}}, nil
}

// TestHolderCheckCountsSelf: the P7 re-check counts every holder,
// paritydesk's own process included, as it does with lsof (the native
// scan does not skip the own pid).
func TestHolderCheckCountsSelf(t *testing.T) {
	held, detail, err := holderCheck(&app.Platform{Holders: selfHolders{}})(t.Context(), "/data/Claude-a")
	if err != nil || !held || detail == "" {
		t.Fatalf("held %v %q %v", held, detail, err)
	}
}
