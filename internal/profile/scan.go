package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources/configjson"
)

// Constant IdentityErr texts. They never carry error details, which could
// hold paths or ids.
const (
	IdentityNoConfig   = "config.json is missing"
	IdentityNoAccount  = "no account is recorded"
	IdentityUnreadable = "config.json could not be read"
)

// MarkerVersion is the marker format this binary reads and writes.
const MarkerVersion = 1

// isDataFolderName reports whether name is "Claude" or "Claude-<x>".
func isDataFolderName(name string) bool {
	return name == ids.DefaultFolder || (strings.HasPrefix(name, ids.DefaultFolder+"-") && len(name) > len(ids.DefaultFolder)+1)
}

// ScanErrUnreadable is Folder.ScanErr of a folder ScanLenient could not
// identify (its session directories, marker or file id could not be read).
const ScanErrUnreadable = "the folder could not be read"

// Scan lists Claude and Claude-* directories (not symlinks) and identifies
// each. It never writes. It fails as a whole when one folder cannot be
// identified.
func (s Scanner) Scan(ctx context.Context) ([]Folder, error) {
	return s.scan(ctx, false)
}

// ScanLenient is Scan, except that a folder whose session directories,
// marker or file id cannot be read is returned with ScanErr set (constant
// text) instead of failing the whole scan. A data root that cannot be read
// still fails.
func (s Scanner) ScanLenient(ctx context.Context) ([]Folder, error) {
	return s.scan(ctx, true)
}

// Identify identifies one folder of the data root by name (Claude or
// Claude-<x>), as Scan does; a symlink or a missing folder is an error.
func (s Scanner) Identify(ctx context.Context, name string) (Folder, error) {
	if err := ctx.Err(); err != nil {
		return Folder{}, err
	}
	if s.FS == nil || s.ReadIdentity == nil || s.ListReplicas == nil {
		return Folder{}, errors.New("profile: identify: scanner is not fully configured")
	}
	if !isDataFolderName(name) || filepath.Base(name) != name {
		return Folder{}, fmt.Errorf("profile: identify: not a data folder name")
	}
	fi, err := os.Lstat(filepath.Join(s.DataRoot, name))
	if err != nil {
		return Folder{}, fmt.Errorf("profile: identify: %w", err)
	}
	if !fi.IsDir() {
		return Folder{}, fmt.Errorf("profile: identify: %s is not a directory", name)
	}
	return s.identify(name)
}

func (s Scanner) scan(ctx context.Context, lenient bool) ([]Folder, error) {
	if s.FS == nil || s.ReadIdentity == nil || s.ListReplicas == nil {
		return nil, errors.New("profile: scan: scanner is not fully configured")
	}
	if err := fsx.CheckTestPath(s.DataRoot); err != nil {
		return nil, fmt.Errorf("profile: scan: %w", err)
	}
	entries, err := os.ReadDir(s.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("profile: scan data root: %w", err)
	}
	var out []Folder
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// DirEntry types come from lstat: a symlink is never a directory.
		if !isDataFolderName(e.Name()) || !e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		f, err := s.identify(e.Name())
		if err != nil {
			if !lenient {
				return nil, err
			}
			f = Folder{Name: e.Name(), Path: filepath.Join(s.DataRoot, e.Name()), Active: e.Name() == ids.DefaultFolder,
				ScanErr: ScanErrUnreadable, IdentityErr: IdentityUnreadable}
		}
		out = append(out, f)
	}
	return out, nil
}

// identify reads one folder's identity, replicas, marker and file id.
func (s Scanner) identify(name string) (Folder, error) {
	path := filepath.Join(s.DataRoot, name)
	f := Folder{Name: name, Path: path, Active: name == ids.DefaultFolder}
	if id, err := s.ReadIdentity(path); err == nil {
		f.Identity = &id
	} else {
		f.IdentityErr = identityErrText(err)
	}
	reps, err := s.ListReplicas(path)
	if err != nil {
		return Folder{}, fmt.Errorf("profile: list session directories of %s: %w", name, err)
	}
	f.Replicas = reps
	if f.Marker, err = ReadMarker(s.FS, path); err != nil && !errors.Is(err, ErrBadMarker) {
		return Folder{}, fmt.Errorf("profile: read marker of %s: %w", name, err)
	}
	if f.ID, err = s.FS.ID(path); err != nil {
		return Folder{}, fmt.Errorf("profile: identify %s: %w", name, err)
	}
	return f, nil
}

// identityErrText maps a ReadIdentity error to constant text.
func identityErrText(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return IdentityNoConfig
	case errors.Is(err, configjson.ErrNoAccount):
		return IdentityNoAccount
	default:
		return IdentityUnreadable
	}
}

// ErrBadMarker is wrapped by ReadMarker for a marker that exists but is
// unparsable or of an unknown version. Scan treats it as no marker.
var ErrBadMarker = errors.New("profile: marker is unparsable")

// ReadMarker reads and parses a folder's marker. It returns (nil, nil)
// when the folder has none, and an error wrapping a parse failure when the
// marker is unparsable or of an unknown version.
func ReadMarker(ops fsx.Ops, folder string) (*model.Marker, error) {
	b, found, err := ops.GetXattr(folder, fsx.MarkerXattr)
	if err != nil {
		return nil, fmt.Errorf("profile: read marker: %w", err)
	}
	if !found {
		return nil, nil
	}
	m := &model.Marker{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadMarker, err)
	}
	if m.Version != MarkerVersion {
		return nil, fmt.Errorf("%w: version %d", ErrBadMarker, m.Version)
	}
	return m, nil
}

// WriteMarker sets a folder's marker.
func WriteMarker(ops fsx.Ops, folder string, m model.Marker) error {
	if m.Version == 0 {
		m.Version = MarkerVersion
	}
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("profile: encode marker: %w", err)
	}
	if err := ops.SetXattr(folder, fsx.MarkerXattr, b); err != nil {
		return fmt.Errorf("profile: write marker: %w", err)
	}
	return nil
}
