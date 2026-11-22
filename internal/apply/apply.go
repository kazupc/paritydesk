package apply

import (
	"context"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// HolderCheck reports whether any process holds files or the LevelDB LOCK
// under folder. Implemented by the app package's quiescence check.
type HolderCheck func(ctx context.Context, folder string) (held bool, detail string, err error)

// Applier applies plans.
type Applier struct {
	FS       fsx.Ops
	KV       kv.Opener
	Holders  HolderCheck
	ReadOnly bool
}

// Input is one apply.
type Input struct {
	Plan *plan.Plan
	// Folder is the entering folder (parked for a switch, Claude/ for sync).
	Folder string
	// TmpDir is <data_root>/.paritydesk-tmp/<run>/.
	TmpDir string
	// QuarantineDir is the generation's quarantine directory.
	QuarantineDir string
}

// Result reports what was applied.
type Result struct {
	Applied int `json:"applied"`
	// PostHashes maps "path:<p>" and "ls:<key>" to the hashes read back.
	PostHashes map[string]string `json:"post_hashes"`
}

// Apply executes in.Plan. It stops at the first error.
func (a *Applier) Apply(ctx context.Context, in Input) (Result, error) {
	return Result{}, pderr.ErrNotImplemented
}

// Check compares the folder against the plan's post-hashes without
// writing. recover uses it to decide whether T's bases can be committed.
func (a *Applier) Check(ctx context.Context, p *plan.Plan, folder string) (matches bool, mismatched []string, err error) {
	return false, nil, pderr.ErrNotImplemented
}
