package idb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/kazupc/paritydesk/internal/store/idb/idbtest"
	"github.com/kazupc/paritydesk/internal/store/kv/leveldb"
)

// A log larger than the byte-copy budget is refused before it is read
// whole: the clone budget (2 GiB) is not a memory budget.
func TestLogReadBoundedByCopyBudget(t *testing.T) {
	f := build(t, idbtest.PinDB(idbtest.PinValue([]string{a1})))
	fi, err := os.Stat(filepath.Join(f, idbtest.Dir, "000003.log"))
	if err != nil {
		t.Fatal(err)
	}
	s := testSpec()
	s.MaxCopyBytes = fi.Size() - 1
	if _, err := ReadString(context.Background(), f, s); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	s.MaxCopyBytes = fi.Size()
	if v, err := ReadString(context.Background(), f, s); err != nil || v.Text != pinText(a1) {
		t.Fatalf("at the budget: v = %+v err = %v", v, err)
	}
	if got := logLimit(Spec{MaxCloneBytes: 2 << 30}); got != 2<<30 {
		t.Fatalf("logLimit without a copy budget = %d", got)
	}
}

// A read of one log stops when the context ends.
func TestCtxReaderStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := io.ReadAll(ctxReader{ctx, strings.NewReader("data")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// Clone failures carry base names only, never the absolute paths an
// *os.LinkError (darwin clonefile) or a wrapped fsx copy error holds
// (spec-pins 4.1).
func TestClonerErrorsHoldNoPaths(t *testing.T) {
	cases := []struct {
		name string
		err  func(src string) error
		is   error
	}{
		{"link error", func(src string) error {
			return &os.LinkError{Op: "clonefile", Old: src, New: filepath.Join(filepath.Dir(src), "dst", "000003.log"), Err: syscall.EACCES}
		}, syscall.EACCES},
		{"wrapped text", func(src string) error {
			return fmt.Errorf("fsx: copy %s: %w", src, io.ErrUnexpectedEOF)
		}, io.ErrUnexpectedEOF},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl, fs, _ := newCloner(t)
			f := build(t, idbtest.PinDB(idbtest.PinValue([]string{a1})))
			src := filepath.Join(f, idbtest.Dir, "000003.log")
			fs.Fail("CloneFileOnly", c.err(src))
			_, err := cl.Read(context.Background(), f, testSpec())
			if err == nil || !errors.Is(err, c.is) {
				t.Fatalf("err = %v, want %v in its chain", err, c.is)
			}
			if strings.Contains(err.Error(), f) || strings.Contains(err.Error(), string(filepath.Separator)+"dst") {
				t.Fatalf("error holds a path: %v", err)
			}
			if ReasonOf(err) != ReasonOther {
				t.Fatalf("reason = %s", ReasonOf(err))
			}
		})
	}
}

// blockTrailer is an uncompressed block's trailer with a valid CRC.
func blockTrailer(block []byte) []byte {
	crc := util.NewCRC(block).Update([]byte{0}).Value()
	return binary.LittleEndian.AppendUint32([]byte{0}, crc)
}

// hostileTable builds a well-formed table whose one data entry claims a key
// length of 2^64-1. Every block checksum is valid.
func hostileTable() []byte {
	entry := []byte{0x00}
	entry = binary.AppendUvarint(entry, ^uint64(0))
	entry = append(entry, 0x0a)
	for len(entry) < 32 {
		entry = append(entry, 'a')
	}
	data := append(entry, 0, 0, 0, 0, 1, 0, 0, 0)
	file := append(append([]byte(nil), data...), blockTrailer(data)...)
	idxOff := len(file)
	h := binary.AppendUvarint(binary.AppendUvarint(nil, 0), uint64(len(data)))
	idx := append([]byte{0x00, 0x01, byte(len(h)), 'z'}, h...)
	idx = append(idx, 0, 0, 0, 0, 1, 0, 0, 0)
	file = append(append(file, idx...), blockTrailer(idx)...)
	metaOff := len(file)
	meta := []byte{0, 0, 0, 0, 1, 0, 0, 0}
	file = append(append(file, meta...), blockTrailer(meta)...)
	footer := binary.AppendUvarint(nil, uint64(metaOff))
	footer = binary.AppendUvarint(footer, uint64(len(meta)))
	footer = binary.AppendUvarint(footer, uint64(idxOff))
	footer = binary.AppendUvarint(footer, uint64(len(idx)))
	for len(footer) < 40 {
		footer = append(footer, 0)
	}
	footer = binary.LittleEndian.AppendUint64(footer, 0xdb4775248b80fb57)
	return append(file, footer...)
}

// readTableBytes writes b as table 000005.ldb and reads it.
func readTableBytes(t testing.TB, b []byte) error {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "000005.ldb"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	open := func(name string) (*os.File, error) { return os.Open(filepath.Join(dir, name)) }
	return readTable(context.Background(), open, "000005.ldb",
		leveldb.TableFile{Num: 5, Size: int64(len(b))}, newCollector(testSpec()))
}

// A crafted table that makes goleveldb's block decoder panic is reported
// as damage, never a crash (spec-pins 4.3 step 3, 6.4).
func TestHostileTableIsUnclean(t *testing.T) {
	err := readTableBytes(t, hostileTable())
	if !errors.Is(err, ErrUnclean) {
		t.Fatalf("err = %v, want ErrUnclean", err)
	}
	if ReasonOf(err) != ReasonUnclean {
		t.Fatalf("reason = %v, want %v", ReasonOf(err), ReasonUnclean)
	}
}
