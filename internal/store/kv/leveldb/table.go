package leveldb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/golang/snappy"

	"github.com/kazupc/paritydesk/internal/store/kv"
)

// Table layout (LevelDB "table_format"): data blocks, meta blocks, a
// metaindex block, an index block and a 48-byte footer. Every block is
// followed by a 5-byte trailer: one compression-type byte and a masked
// CRC-32C of the block contents plus that byte.
const (
	footerLen       = 48
	blockTrailerLen = 5
	tableMagic      = 0xdb4775248b80fb57
	compressionNone = 0
	compressionSnap = 1
	// maxBlockLen bounds a single block; LevelDB blocks are about 4 KiB and
	// Chromium's are far below this, so anything larger is malformed.
	maxBlockLen = 64 << 20
)

// errMalformed marks a table that does not parse. It is wrapped together
// with the file's base name and never with any of its content.
var errMalformed = errors.New("malformed table")

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// maskedCRC is LevelDB's masked CRC-32C.
func maskedCRC(b []byte) uint32 {
	c := crc32.Checksum(b, crcTable)
	return (c>>15 | c<<17) + 0xa282ead8
}

// blockHandle locates a block inside a table file (trailer excluded).
type blockHandle struct{ offset, length uint64 }

// decodeHandle reads a varint-encoded handle and returns the bytes used.
func decodeHandle(b []byte) (blockHandle, int, error) {
	off, n := binary.Uvarint(b)
	if n <= 0 {
		return blockHandle{}, 0, fmt.Errorf("%w: bad block handle", errMalformed)
	}
	length, m := binary.Uvarint(b[n:])
	if m <= 0 {
		return blockHandle{}, 0, fmt.Errorf("%w: bad block handle", errMalformed)
	}
	return blockHandle{offset: off, length: length}, n + m, nil
}

// errTableMissing marks a table the manifest lists but the directory
// lacks. Both libraries refuse such a database when they open it.
var errTableMissing = errors.New("table listed in the manifest is missing")

// CheckCompressions checks every table the database in dir uses: the
// live tables of the manifest goleveldb would open (chosen from CURRENT
// as in journal.go, read leniently). It returns an error wrapping
// kv.ErrUnknownCompression if any block uses a compression type outside
// AllowedCompressions, and another error if a live table is missing or
// malformed or the manifest cannot be read. Table files the manifest
// does not list are ignored, as both libraries ignore them: the C++
// library leaves an unfinished compaction output behind when it closes
// during a compaction (empty, or cut short before its footer) and
// removes it on its next open, as goleveldb does. It only reads files.
func CheckCompressions(dir string) error {
	m, err := readManifest(dir, false)
	if err != nil {
		return err
	}
	return checkLiveTables(dir, m)
}

// checkLiveTables checks the live tables of m in dir. A table is looked
// for as <n>.ldb and then <n>.sst, the order both libraries use.
func checkLiveTables(dir string, m manifestState) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("leveldb: list database directory: %w", err)
	}
	names := map[int64]string{}
	for _, e := range entries {
		num, kind, ok := parseFileName(e.Name())
		if !ok || (kind != "ldb" && kind != "sst") {
			continue
		}
		if prev, dup := names[num]; !dup || (kind == "ldb" && !strings.HasSuffix(prev, ".ldb")) {
			names[num] = e.Name()
		}
	}
	for _, num := range m.tableNums() {
		name, ok := names[num]
		if !ok {
			return tableErr(fmt.Sprintf("%06d", num), errTableMissing)
		}
		if err := checkTableFile(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// checkTableFile checks one table. Errors name only the file's base name.
func checkTableFile(path string) error {
	name := filepath.Base(path)
	fi, err := os.Lstat(path)
	if err != nil {
		return tableErr(name, stripPath(err))
	}
	if !fi.Mode().IsRegular() {
		return tableErr(name, fmt.Errorf("%w: not a regular file", errMalformed))
	}
	f, err := os.Open(path)
	if err != nil {
		return tableErr(name, stripPath(err))
	}
	defer f.Close()
	if err := checkTable(f, fi.Size()); err != nil {
		return tableErr(name, err)
	}
	return nil
}

func tableErr(name string, err error) error {
	return fmt.Errorf("leveldb: table %s: %w", name, err)
}

// stripPath drops the full path from a *fs.PathError so only the base name
// (added by the caller) reaches the message.
func stripPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	return err
}

// checkTable walks the table in r (size bytes): the footer, the metaindex
// and the meta blocks it names, the index block and every data block it
// names. Each block's checksum and compression type are checked; the
// metaindex and index blocks are decoded to find the others.
func checkTable(r io.ReaderAt, size int64) error {
	if size < footerLen {
		return fmt.Errorf("%w: file shorter than the footer", errMalformed)
	}
	footer := make([]byte, footerLen)
	if _, err := r.ReadAt(footer, size-footerLen); err != nil {
		return fmt.Errorf("read footer: %w", err)
	}
	if binary.LittleEndian.Uint64(footer[footerLen-8:]) != tableMagic {
		return fmt.Errorf("%w: bad magic", errMalformed)
	}
	metaBH, n, err := decodeHandle(footer[:footerLen-8])
	if err != nil {
		return err
	}
	indexBH, _, err := decodeHandle(footer[n : footerLen-8])
	if err != nil {
		return err
	}
	t := tableReader{r: r, limit: uint64(size - footerLen)}
	meta, err := t.block(metaBH)
	if err != nil {
		return fmt.Errorf("metaindex block: %w", err)
	}
	if err := t.checkHandles(meta); err != nil {
		return fmt.Errorf("metaindex block: %w", err)
	}
	index, err := t.block(indexBH)
	if err != nil {
		return fmt.Errorf("index block: %w", err)
	}
	if err := t.checkHandles(index); err != nil {
		return fmt.Errorf("index block: %w", err)
	}
	return nil
}

// tableReader reads blocks below limit (the footer's offset).
type tableReader struct {
	r     io.ReaderAt
	limit uint64
}

// block reads the block at h, verifies its trailer and returns its
// decompressed contents.
func (t tableReader) block(h blockHandle) ([]byte, error) {
	if h.length > maxBlockLen || h.offset > t.limit || h.length+blockTrailerLen > t.limit-h.offset {
		return nil, fmt.Errorf("%w: block at offset %d out of range", errMalformed, h.offset)
	}
	buf := make([]byte, h.length+blockTrailerLen)
	if _, err := t.r.ReadAt(buf, int64(h.offset)); err != nil {
		return nil, fmt.Errorf("read block at offset %d: %w", h.offset, err)
	}
	n := len(buf) - 4
	if maskedCRC(buf[:n]) != binary.LittleEndian.Uint32(buf[n:]) {
		return nil, fmt.Errorf("%w: checksum mismatch in block at offset %d", errMalformed, h.offset)
	}
	typ := int(buf[h.length])
	if !slices.Contains(AllowedCompressions, typ) {
		return nil, fmt.Errorf("%w: type %d in block at offset %d", kv.ErrUnknownCompression, typ, h.offset)
	}
	data := buf[:h.length]
	if typ == compressionSnap {
		dl, err := snappy.DecodedLen(data)
		if err != nil || dl > maxBlockLen {
			return nil, fmt.Errorf("%w: bad Snappy block at offset %d", errMalformed, h.offset)
		}
		out, err := snappy.Decode(nil, data)
		if err != nil {
			return nil, fmt.Errorf("%w: bad Snappy block at offset %d", errMalformed, h.offset)
		}
		return out, nil
	}
	return data, nil
}

// checkHandles decodes a block whose values are block handles (the index
// and metaindex blocks) and checks every block they point to. Keys are
// skipped, never interpreted.
func (t tableReader) checkHandles(block []byte) error {
	return forEachValue(block, func(v []byte) error {
		h, _, err := decodeHandle(v)
		if err != nil {
			return err
		}
		_, err = t.block(h)
		return err
	})
}

// forEachValue iterates the entries of a decoded block and calls fn with
// each value. Entry: shared, unshared and value lengths (varints), the
// unshared key bytes, the value. The block ends with the restart array and
// its uint32 count.
func forEachValue(block []byte, fn func(v []byte) error) error {
	if len(block) < 4 {
		return fmt.Errorf("%w: block too short", errMalformed)
	}
	restarts := uint64(binary.LittleEndian.Uint32(block[len(block)-4:]))
	if restarts*4 > uint64(len(block)-4) {
		return fmt.Errorf("%w: bad restart count", errMalformed)
	}
	entries := block[:uint64(len(block)-4)-restarts*4]
	prevKey := uint64(0)
	for len(entries) > 0 {
		var hdr [3]uint64
		for i := range hdr {
			v, n := binary.Uvarint(entries)
			if n <= 0 {
				return fmt.Errorf("%w: bad block entry", errMalformed)
			}
			hdr[i], entries = v, entries[n:]
		}
		shared, unshared, vlen := hdr[0], hdr[1], hdr[2]
		if shared > prevKey || unshared > uint64(len(entries)) || vlen > uint64(len(entries))-unshared {
			return fmt.Errorf("%w: bad block entry", errMalformed)
		}
		value := entries[unshared : unshared+vlen]
		entries = entries[unshared+vlen:]
		prevKey = shared + unshared
		if err := fn(value); err != nil {
			return err
		}
	}
	return nil
}
