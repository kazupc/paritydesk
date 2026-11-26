package orchestrator

import (
	"slices"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
)

func TestJobArgs(t *testing.T) {
	tests := []struct {
		name string
		opts SwitchOptions
		g    JobGlobals
		want []string
	}{
		{"minimal previous", SwitchOptions{LogOffset: -1}, JobGlobals{},
			[]string{"switch", "--from-runner", "--", "-"}},
		{"full", SwitchOptions{
			To: "work", Yes: true, Only: []model.Category{model.CatSessions, model.CatPins},
			Skip: []model.Category{model.CatWindow}, NoLaunch: true, NoReopen: true, Detach: true,
			ReopenCLISession: "00000000-0000-4000-8000-000000000001", AllowDeletions: 4,
			AcceptWorktreeRisk: true, LogOffset: 0, LogInode: 4242, ConfirmedPlan: "abc",
			ConfirmFirstSync: true,
		}, JobGlobals{Config: "/c/config.toml", StateDir: "/s", AppSupportDir: "/a"},
			[]string{"switch", "--from-runner", "--yes", "--only=sessions,pins", "--skip=window",
				"--no-launch", "--no-reopen", "--reopen-session=00000000-0000-4000-8000-000000000001",
				"--allow-deletions=4", "--accept-worktree-risk", "--log-offset=0", "--log-inode=4242",
				"--confirmed-plan=abc", "--confirm-first-sync", "--config=/c/config.toml",
				"--state-dir=/s", "--app-support-dir=/a", "--", "work"}},
		{"inode without an offset is not passed", SwitchOptions{LogOffset: -1, LogInode: 7}, JobGlobals{},
			[]string{"switch", "--from-runner", "--", "-"}},
		{"offset without an inode", SwitchOptions{LogOffset: 12}, JobGlobals{},
			[]string{"switch", "--from-runner", "--log-offset=12", "--", "-"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.JobArgs(tc.g); !slices.Equal(got, tc.want) {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}
