package configjson

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/jsonx"
)

// FileName is the file inside a data folder.
const FileName = "config.json"

// Top-level key names read by the span scan.
const (
	accountKey    = "lastKnownAccountUuid"
	tokenCacheKey = "oauth:tokenCacheV2"
)

// maxSize bounds the read; the real file is far smaller.
const maxSize = 8 << 20

// Identity is what a folder's config.json says about its account.
type Identity struct {
	Account ids.AccountID
	// HasTokenCache reports whether the top-level token-cache key exists.
	// Its value is never read. Presence does not prove a valid sign-in.
	HasTokenCache bool
}

// ErrNoAccount means the folder has never recorded a signed-in account.
var ErrNoAccount = errors.New("configjson: no account recorded")

// ReadIdentity reads folder/config.json. A symlink, a non-regular file or
// a file larger than maxSize is refused. Errors never carry file content.
func ReadIdentity(folder string) (Identity, error) {
	buf, err := readBounded(filepath.Join(folder, FileName))
	if err != nil {
		return Identity{}, err
	}
	defer clear(buf)
	return parse(buf)
}

// readBounded reads a regular file of at most maxSize bytes without
// following a symlink. The opened file must be the one checked by Lstat,
// and the read is bounded even if the file grows in between.
func readBounded(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("configjson: %s is not a regular file", FileName)
	}
	if fi.Size() > maxSize {
		return nil, fmt.Errorf("configjson: %s is too large", FileName)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(fi, opened) {
		return nil, fmt.Errorf("configjson: %s changed while opening", FileName)
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		clear(buf)
		return nil, fmt.Errorf("configjson: read %s: %w", FileName, err)
	}
	if len(buf) > maxSize {
		clear(buf)
		return nil, fmt.Errorf("configjson: %s is too large", FileName)
	}
	return buf, nil
}

func parse(buf []byte) (Identity, error) {
	acct, found, err := jsonx.TopLevelString(buf, accountKey)
	if err != nil {
		return Identity{}, fmt.Errorf("configjson: %w", err)
	}
	if !found || acct == "" {
		return Identity{}, ErrNoAccount
	}
	a, err := ids.ParseAccount(acct)
	if err != nil {
		return Identity{}, fmt.Errorf("configjson: recorded account is not a uuid")
	}
	has, err := jsonx.HasTopLevelKey(buf, tokenCacheKey)
	if err != nil {
		return Identity{}, fmt.Errorf("configjson: %w", err)
	}
	return Identity{Account: a, HasTokenCache: has}, nil
}
