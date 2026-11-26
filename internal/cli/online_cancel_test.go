package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kazupc/paritydesk/internal/online"
)

// TestOnlineCancelWithoutOnlineEnv: the emergency stop does not need the
// config, the state or the app (4.5): with the online environment broken,
// cancel still creates stop, removes armed and the lease, and marks the
// queue cancelled.
func TestOnlineCancelWithoutOnlineEnv(t *testing.T) {
	h := newHarness(t)
	h.publish(sidebarDraft(t, manifest7(t)))
	h.nextJSON("--session", olSession)
	store := h.onlineEnv().svc.Store
	if armed, err := store.ArmedUntil(fixedTime); err != nil || !armed {
		t.Fatalf("not armed before cancel: %v %v", armed, err)
	}
	h.sys.online = func(context.Context, *env) (*onlineEnv, error) { return nil, errors.New("config: broken") }
	if code, out, errOut := h.run("", "online", "cancel"); code != 0 {
		t.Fatalf("cancel: %d %q %q", code, out, errOut)
	}
	if !store.Stopped() {
		t.Fatal("stop not created")
	}
	if armed, _ := store.ArmedUntil(fixedTime); armed {
		t.Fatal("armed kept")
	}
	if l, _ := store.ReadLease(); l != nil {
		t.Fatal("lease kept")
	}
	if q, _ := store.ReadQueue(online.PhaseReconcile); q == nil || q.State != online.QueueCancelled {
		t.Fatalf("queue %+v", q)
	}
}

// TestOnlineCancelBrokenConfigReal: the real wiring of online cancel with
// a config file that does not parse (darwin only: the platform's Ops).
func TestOnlineCancelBrokenConfigReal(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the real online folder needs the darwin Ops")
	}
	h := newHarness(t)
	h.sys.online = realOnline
	h.sys.onlineCancel = realOnlineCancel
	writeFile(h, ".config/paritydesk/config.toml", "version = 1\n[[profile]\nalias = \n")
	dir := filepath.Join(h.home, "Library", "Application Support", "paritydesk", "online")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "armed"), []byte("9999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := h.run("", "online", "cancel")
	_, stopErr := os.Stat(filepath.Join(dir, "stop"))
	_, armedErr := os.Stat(filepath.Join(dir, "armed"))
	if code != 0 || stopErr != nil || !errors.Is(armedErr, os.ErrNotExist) {
		t.Fatalf("exit %d %q %q; stop %v, armed %v", code, out, errOut, stopErr, armedErr)
	}
}
