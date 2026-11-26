package idb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/kazupc/paritydesk/internal/store/idb/idbtest"
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
