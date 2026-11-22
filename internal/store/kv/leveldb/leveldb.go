package leveldb

import (
	"context"

	"github.com/syndtr/goleveldb/leveldb/opt"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// AllowedCompressions are the block compression types accepted (0 none,
// 1 Snappy). Anything else is refused before writing.
var AllowedCompressions = []int{0, 1}

// snapshotOptions opens clones read-only and leniently.
func snapshotOptions() *opt.Options {
	return &opt.Options{ReadOnly: true, ErrorIfMissing: true, Strict: opt.NoStrict}
}

// writableOptions opens the original strictly and never creates a database.
func writableOptions() *opt.Options {
	return &opt.Options{ErrorIfMissing: true, Strict: opt.StrictAll, NoSync: false}
}

// Opener opens LevelDB directories.
type Opener struct {
	// TempRoot is the parent of snapshot clones; it should be on the same
	// volume as the data root so clonefile works, and mode 0700.
	TempRoot string
	FS       fsx.Ops
}

var _ kv.Opener = (*Opener)(nil)

// New returns an Opener.
func New(tempRoot string, fs fsx.Ops) *Opener { return &Opener{TempRoot: tempRoot, FS: fs} }

// OpenSnapshot implements kv.Opener.
func (o *Opener) OpenSnapshot(ctx context.Context, dir string) (kv.Reader, error) {
	_ = snapshotOptions()
	return nil, pderr.ErrNotImplemented
}

// OpenWritable implements kv.Opener.
func (o *Opener) OpenWritable(ctx context.Context, dir string) (kv.Writer, error) {
	_ = writableOptions()
	return nil, pderr.ErrNotImplemented
}

// CheckCompressions scans every table file in dir and returns an error if
// any block uses a compression type outside AllowedCompressions.
func CheckCompressions(dir string) error {
	return pderr.ErrNotImplemented
}
