package leveldb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
)

// ManifestInfo is what a manifest records, for readers that do not open
// the database (internal/store/idb). It makes no comparator decision.
type ManifestInfo struct {
	Comparator                   string
	LogNum, PrevLogNum, NextFile int64
	LastSeq                      uint64
	// Tables are the live tables (added and not deleted later), sorted by
	// level, then number.
	Tables []TableFile
}

// TableFile is one live table of a manifest.
type TableFile struct {
	Level     uint64
	Num, Size int64
}

// ErrCurrent marks a CURRENT file that is not exactly
// "MANIFEST-<6 or more digits>\n".
var ErrCurrent = errors.New("leveldb: CURRENT does not name a manifest")

// ErrManifestUnreadable marks a manifest record InspectManifest cannot
// read (an unknown field, a record cut short, a damaged journal).
var ErrManifestUnreadable = errors.New("leveldb: unreadable manifest")

// currentRE is the only CURRENT content InspectManifest accepts.
var currentRE = regexp.MustCompile(`^MANIFEST-[0-9]{6,}\n$`)

// maxCurrentLen bounds the CURRENT file InspectManifest reads.
const maxCurrentLen = 64

// InspectManifest reads CURRENT and the manifest it names through open
// (which opens a file of the database directory by base name; dir is the
// caller's name for that directory and is never opened here). CURRENT must
// hold exactly "MANIFEST-<digits>\n": no CURRENT.<n> or CURRENT.bak
// fallback is tried. The manifest is replayed strictly; any record it
// cannot read is an error. Errors name base file names only.
func InspectManifest(dir string, open func(name string) (io.ReadCloser, error)) (ManifestInfo, error) {
	_ = dir
	cur, err := open("CURRENT")
	if err != nil {
		return ManifestInfo{}, fmt.Errorf("%w: %w", ErrCurrent, stripPath(err))
	}
	b, err := io.ReadAll(io.LimitReader(cur, maxCurrentLen+1))
	cur.Close()
	if err != nil {
		return ManifestInfo{}, fmt.Errorf("%w: %w", ErrCurrent, stripPath(err))
	}
	if len(b) > maxCurrentLen || !currentRE.Match(b) {
		return ManifestInfo{}, ErrCurrent
	}
	name := string(b[:len(b)-1])
	f, err := open(name)
	if err != nil {
		return ManifestInfo{}, fmt.Errorf("leveldb: open %s: %w", name, stripPath(err))
	}
	defer f.Close()
	m, err := decodeManifestNamed(f, name, true)
	if err != nil {
		return ManifestInfo{}, fmt.Errorf("%w: %w", ErrManifestUnreadable, err)
	}
	info := ManifestInfo{Comparator: m.comparator, LogNum: m.journalNum, PrevLogNum: m.prevJournalNum,
		NextFile: m.nextFileNum, LastSeq: m.lastSeq}
	for id, t := range m.tables {
		info.Tables = append(info.Tables, TableFile{Level: id.level, Num: id.num, Size: t.size})
	}
	sort.Slice(info.Tables, func(i, j int) bool {
		a, b := info.Tables[i], info.Tables[j]
		if a.Level != b.Level {
			return a.Level < b.Level
		}
		return a.Num < b.Num
	})
	return info, nil
}

// ErrTableMalformed marks a table whose footer, index or block checksums
// do not parse (TableBlockTypes).
var ErrTableMalformed = errMalformed

// TableBlockTypes returns the compression type byte of every data block of
// the table in r (size bytes), in index order. It reads the footer and the
// index block with this package's block reader (checksums verified), then
// reads each data block with its 5-byte trailer and verifies its checksum.
// It never decompresses a data block. A malformed footer or index, or a
// checksum error, wraps ErrTableMalformed; an index block with an unknown
// compression type wraps kv.ErrUnknownCompression.
func TableBlockTypes(r io.ReaderAt, size int64) ([]byte, error) {
	if size < footerLen {
		return nil, fmt.Errorf("%w: file shorter than the footer", errMalformed)
	}
	footer := make([]byte, footerLen)
	if _, err := r.ReadAt(footer, size-footerLen); err != nil {
		return nil, fmt.Errorf("%w: read footer: %w", errMalformed, err)
	}
	if binary.LittleEndian.Uint64(footer[footerLen-8:]) != tableMagic {
		return nil, fmt.Errorf("%w: bad magic", errMalformed)
	}
	_, n, err := decodeHandle(footer[:footerLen-8])
	if err != nil {
		return nil, err
	}
	indexBH, _, err := decodeHandle(footer[n : footerLen-8])
	if err != nil {
		return nil, err
	}
	t := tableReader{r: r, limit: uint64(size - footerLen)}
	index, err := t.block(indexBH)
	if err != nil {
		return nil, fmt.Errorf("index block: %w", err)
	}
	var types []byte
	err = forEachValue(index, func(v []byte) error {
		h, _, err := decodeHandle(v)
		if err != nil {
			return err
		}
		typ, err := t.blockType(h)
		if err != nil {
			return err
		}
		types = append(types, typ)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return types, nil
}

// blockType reads the block at h with its trailer, verifies the checksum
// and returns the compression type byte, whatever it is.
func (t tableReader) blockType(h blockHandle) (byte, error) {
	if h.length > maxBlockLen || h.offset > t.limit || h.length+blockTrailerLen > t.limit-h.offset {
		return 0, fmt.Errorf("%w: block at offset %d out of range", errMalformed, h.offset)
	}
	buf := make([]byte, h.length+blockTrailerLen)
	if _, err := t.r.ReadAt(buf, int64(h.offset)); err != nil {
		return 0, fmt.Errorf("%w: read block at offset %d: %w", errMalformed, h.offset, err)
	}
	n := len(buf) - 4
	if maskedCRC(buf[:n]) != binary.LittleEndian.Uint32(buf[n:]) {
		return 0, fmt.Errorf("%w: checksum mismatch in block at offset %d", errMalformed, h.offset)
	}
	return buf[h.length], nil
}
