package hook

import (
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/state"
)

// dueRoot is a root with home active in epoch 4 and nothing else
// pending.
func dueRoot() *state.Root {
	r := state.NewRoot("/data")
	r.Active, r.Epoch = "home", 4
	return r
}

func dueDeps(root *state.Root, due ArtifactsDue, ok bool) (Deps, *fakeOnline) {
	d := deps(root)
	f := &fakeOnline{}
	d.Online, d.Mode, d.ArtifactsOn = f, "prompt", true
	d.ArtifactsDue = func() (ArtifactsDue, bool) { return due, ok }
	return d, f
}

// TestSessionStartArtifactsDue: item 4b after the online line and before
// drift, for startup and resume, for the active profile and epoch only,
// at most three times per due.json.
func TestSessionStartArtifactsDue(t *testing.T) {
	line := i18n.T(i18n.HookArtifactsDue) + "\n"
	good := ArtifactsDue{Seq: 7, Profile: "home", Epoch: 4, Count: 2}
	tests := []struct {
		name   string
		root   func() *state.Root
		due    ArtifactsDue
		ok     bool
		source string
		on     bool
		want   string
	}{
		{"due", dueRoot, good, true, "startup", true, line},
		{"resume", dueRoot, good, true, "resume", true, line},
		{"clear", dueRoot, good, true, "clear", true, ""},
		{"off", dueRoot, good, true, "startup", false, ""},
		{"no file", dueRoot, good, false, "startup", true, ""},
		{"other profile", dueRoot, ArtifactsDue{Seq: 7, Profile: "work", Epoch: 4, Count: 2}, true, "startup", true, ""},
		{"other epoch", dueRoot, ArtifactsDue{Seq: 7, Profile: "home", Epoch: 3, Count: 2}, true, "startup", true, ""},
		{"nothing due", dueRoot, ArtifactsDue{Seq: 7, Profile: "home", Epoch: 4}, true, "startup", true, ""},
		{"before drift", func() *state.Root {
			r := dueRoot()
			r.LastVerify = &state.VerifyRecord{Drift: []model.RowID{model.RowSidebarView}}
			return r
		}, good, true, "startup", true, line},
		{"recover first", func() *state.Root {
			r := dueRoot()
			r.Run = &state.Run{ID: "run-0002", Kind: state.RunSwitch}
			return r
		}, good, true, "startup", true, i18n.T(i18n.HookRecover) + "\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := dueDeps(tc.root(), tc.due, tc.ok)
			d.ArtifactsOn = tc.on
			if got := run(t, strings.NewReader(onlinePayload(tc.source, "default")), d); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	// The online line comes first.
	r := onlineRoot()
	d, f := dueDeps(r, ArtifactsDue{Seq: 7, Profile: string(r.Active), Epoch: r.Epoch, Count: 1}, true)
	f.head = &online.QueueHead{V: 1, ID: "run-0001", Phase: online.PhaseReconcile, State: online.QueuePending}
	if got := run(t, strings.NewReader(onlinePayload("startup", "default")), d); got != i18n.T(i18n.HookOnlinePrompt)+"\n" {
		t.Fatalf("online first: %q", got)
	}
	// At most three shows per due.json seq.
	d, _ = dueDeps(dueRoot(), good, true)
	for i := 0; i < 3; i++ {
		if run(t, strings.NewReader(onlinePayload("startup", "default")), d) != line {
			t.Fatalf("show %d", i+1)
		}
	}
	if got := run(t, strings.NewReader(onlinePayload("startup", "default")), d); got != "" {
		t.Fatalf("fourth show %q", got)
	}
}
