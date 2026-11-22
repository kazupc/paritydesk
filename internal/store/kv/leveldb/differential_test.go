package leveldb_test

// Differential tests against the C++ LevelDB library. They run only when
// PARITYDESK_LEVELDB_CXX names the helper built from
// testdata/leveldb-cxx (scripts/ci/build-leveldb-cxx.sh); the CI job
// "leveldb differential" builds it with Homebrew's leveldb and snappy.
// Without the variable every test here is skipped.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/store/kv"
	"github.com/kazupc/paritydesk/internal/store/kv/leveldb"
)

// cxxHelperEnv names the helper binary.
const cxxHelperEnv = "PARITYDESK_LEVELDB_CXX"

// cxxSeed fixes the helper's workload.
const cxxSeed = "20261122"

// cxxHelper returns the helper's path, or skips the test.
func cxxHelper(t *testing.T) string {
	t.Helper()
	p := os.Getenv(cxxHelperEnv)
	if p == "" {
		t.Skipf("set %s to the helper built from testdata/leveldb-cxx (CI only)", cxxHelperEnv)
	}
	if !filepath.IsAbs(p) {
		t.Fatalf("%s must be an absolute path", cxxHelperEnv)
	}
	return p
}

// runCXX runs the helper and returns its standard output.
func runCXX(t *testing.T, bin string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("leveldb-cxx %s: %v\n%s", args[0], err, stderr.String())
	}
	return out
}

// content is a database's expected or observed content.
type content struct {
	keys    []string          // live keys in the order they were listed
	live    map[string]string // key -> value
	deleted []string          // keys the workload deleted and did not put again
}

// parseCXX parses "P <hex key> <hex value>" and "D <hex key>" lines.
func parseCXX(t *testing.T, out []byte) content {
	t.Helper()
	c := content{live: map[string]string{}}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<23)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		switch {
		case len(fields) == 3 && fields[0] == "P":
			c.keys = append(c.keys, decodeHex(t, fields[1]))
			c.live[c.keys[len(c.keys)-1]] = decodeHex(t, fields[2])
		case len(fields) == 2 && fields[0] == "P":
			// An empty value prints as "P <hex key> ", which Fields
			// shortens to two fields.
			c.keys = append(c.keys, decodeHex(t, fields[1]))
			c.live[c.keys[len(c.keys)-1]] = ""
		case len(fields) == 2 && fields[0] == "D":
			c.deleted = append(c.deleted, decodeHex(t, fields[1]))
		default:
			t.Fatalf("leveldb-cxx: unexpected output line of %d fields", len(fields))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("leveldb-cxx output: %v", err)
	}
	if len(c.keys) != len(c.live) {
		t.Fatal("leveldb-cxx listed a key twice")
	}
	return c
}

func decodeHex(t *testing.T, s string) string {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("leveldb-cxx output: %v", err)
	}
	return string(b)
}

// newOpener returns a paritydesk opener whose snapshots live in the
// test's temp tree.
func newOpener(t *testing.T) *leveldb.Opener {
	t.Helper()
	o, err := leveldb.New(filepath.Join(t.TempDir(), "snapshots"), fsxtest.New(t))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// scanContent returns r's content in scan order.
func scanContent(t *testing.T, r kv.Reader) content {
	t.Helper()
	c := content{live: map[string]string{}}
	if err := r.Scan(nil, func(k, v []byte) error {
		c.keys = append(c.keys, string(k))
		c.live[string(k)] = string(v)
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return c
}

// readSnapshot reads dir through OpenSnapshot (the path paritydesk uses
// for every read) and returns its content in scan order.
func readSnapshot(t *testing.T, o *leveldb.Opener, dir string) (content, kv.Reader) {
	t.Helper()
	r, err := o.OpenSnapshot(context.Background(), dir)
	if err != nil {
		t.Fatalf("OpenSnapshot: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return scanContent(t, r), r
}

// sameContent fails the test when got differs from want in keys, order
// or values. Messages show at most a few keys, hex-encoded and shortened.
func sameContent(t *testing.T, what string, want, got content) {
	t.Helper()
	var problems []string
	for _, k := range want.keys {
		v, ok := got.live[k]
		switch {
		case !ok:
			problems = append(problems, "missing "+short(k))
		case v != want.live[k]:
			problems = append(problems, fmt.Sprintf("value of %s differs (%d bytes, want %d)", short(k), len(v), len(want.live[k])))
		}
	}
	for _, k := range got.keys {
		if _, ok := want.live[k]; !ok {
			problems = append(problems, "unexpected "+short(k))
		}
	}
	if len(problems) == 0 && !sort.StringsAreSorted(got.keys) {
		problems = append(problems, "keys are not in byte order")
	}
	if len(problems) > 0 {
		if len(problems) > 5 {
			problems = append(problems[:5], fmt.Sprintf("and %d more", len(problems)-5))
		}
		t.Fatalf("%s: %d keys, want %d: %s", what, len(got.keys), len(want.keys), strings.Join(problems, "; "))
	}
}

func short(k string) string {
	h := hex.EncodeToString([]byte(k))
	if len(h) > 40 {
		h = h[:40] + "..."
	}
	return h
}

// checkGets compares point reads with want: every live key has its value
// and every deleted key is absent.
func checkGets(t *testing.T, r kv.Reader, want content) {
	t.Helper()
	for _, k := range want.keys {
		v, ok, err := r.Get([]byte(k))
		if err != nil || !ok || string(v) != want.live[k] {
			t.Fatalf("Get %s: found=%v err=%v, value matches=%v", short(k), ok, err, string(v) == want.live[k])
		}
	}
	for _, k := range want.deleted {
		if _, ok, err := r.Get([]byte(k)); err != nil || ok {
			t.Fatalf("Get deleted %s: found=%v err=%v", short(k), ok, err)
		}
	}
}

// tableBytes sums the sizes of dir's table files.
func tableBytes(t *testing.T, dir string) (n int64, files int) {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if !strings.HasSuffix(e.Name(), ".ldb") && !strings.HasSuffix(e.Name(), ".sst") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		n += fi.Size()
		files++
	}
	return n, files
}

// writeCXX makes the helper write its workload into a new database and
// returns the database directory and the expected content.
func writeCXX(t *testing.T, bin, compression string) (string, content) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "db")
	want := parseCXX(t, runCXX(t, bin, "write", dir, compression, cxxSeed))
	if len(want.keys) < 2000 || len(want.deleted) < 500 {
		t.Fatalf("workload too small: %d live, %d deleted", len(want.keys), len(want.deleted))
	}
	return dir, want
}

// TestDifferentialCXXWritesGoReads: databases written by the C++ library,
// with and without Snappy, read the same through paritydesk's snapshot
// path as through the C++ library, and pass the table checker.
func TestDifferentialCXXWritesGoReads(t *testing.T) {
	bin := cxxHelper(t)
	sizes := map[string]int64{}
	for _, comp := range []string{"none", "snappy"} {
		t.Run(comp, func(t *testing.T) {
			dir, want := writeCXX(t, bin, comp)
			logJournalState(t, dir)
			n, files := tableBytes(t, dir)
			if files < 2 {
				t.Fatalf("the workload left %d table files; want several", files)
			}
			sizes[comp] = n
			if err := leveldb.CheckCompressions(dir); err != nil {
				t.Fatalf("CheckCompressions: %v", err)
			}
			got, r := readSnapshot(t, newOpener(t), dir)
			sameContent(t, "snapshot read", want, got)
			checkGets(t, r, want)
			// The C++ dump opens the database for writing, so it runs
			// after every Go read of the original files.
			sameContent(t, "C++ dump", want, parseCXX(t, runCXX(t, bin, "dump", dir)))
		})
	}
	// Without Snappy in the C++ build, LevelDB silently stores blocks
	// uncompressed; the compressible workload makes the difference plain.
	if sizes["none"] > 0 && sizes["snappy"] > 0 && sizes["snappy"]*4 > sizes["none"]*3 {
		t.Fatalf("Snappy tables are %d bytes, uncompressed %d: the C++ library may lack Snappy", sizes["snappy"], sizes["none"])
	}
}

// logJournalState logs whether the C++ library left a journal holding
// batches numbered below its manifest's last sequence (journal.go) and
// reports whether it did. It usually does: the workload's last writes
// overlap background compactions, so this depends on timing and is not
// asserted.
func logJournalState(t *testing.T, dir string) bool {
	t.Helper()
	switch err := leveldb.CheckJournals(dir); {
	case errors.Is(err, kv.ErrJournalSequence):
		t.Logf("the C++ library left a journal behind its manifest: %v", err)
		return true
	case err != nil:
		t.Fatalf("CheckJournals: %v", err)
	default:
		t.Log("the C++ library left no journal behind its manifest")
	}
	return false
}

// copyDB copies the regular files of src into a new directory.
func copyDB(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "copy")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	es, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if !e.Type().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// TestDifferentialGoWritesCXXReads: OpenWritable opens the database the
// C++ library left, as the app leaves it on quit (usually with a journal
// behind its manifest, which OpenWritable replays), and a batch written
// through it is read back the same by the C++ library, and by paritydesk
// after the C++ library has opened (and possibly compacted) it. The
// state a crash right after OpenWritable added its replay journal leaves
// is read the same by the C++ library too.
func TestDifferentialGoWritesCXXReads(t *testing.T) {
	bin := cxxHelper(t)
	for _, comp := range []string{"none", "snappy"} {
		t.Run(comp, func(t *testing.T) {
			dir, want := writeCXX(t, bin, comp)
			o := newOpener(t)
			if logJournalState(t, dir) {
				crashed := copyDB(t, dir)
				name, err := leveldb.AddReplayJournal(crashed)
				if err != nil || name == "" {
					t.Fatalf("AddReplayJournal: %q, %v", name, err)
				}
				got, r := readSnapshot(t, o, crashed)
				sameContent(t, "snapshot read after a replay journal was added", want, got)
				checkGets(t, r, want)
				sameContent(t, "C++ dump after a replay journal was added", want, parseCXX(t, runCXX(t, bin, "dump", crashed)))
			}
			w, err := o.OpenWritable(context.Background(), dir)
			if err != nil {
				t.Fatalf("OpenWritable: %v", err)
			}
			sameContent(t, "read through OpenWritable before a Go write", want, scanContent(t, w))
			b := &kv.Batch{}
			for i, k := range want.keys {
				switch {
				case i%9 == 0:
					b.Delete([]byte(k))
					delete(want.live, k)
					want.deleted = append(want.deleted, k)
				case i%13 == 5:
					v := strings.Repeat(fmt.Sprintf("go-overwrite-%d;", i), 1+i%300)
					b.Put([]byte(k), []byte(v))
					want.live[k] = v
				}
			}
			for i := range 400 {
				k := fmt.Sprintf("_https://example.org\x00\x01go-%05d", i)
				v := strings.Repeat("x", i%7) + string([]byte{0, 1, 0xff, byte(i)})
				b.Put([]byte(k), []byte(v))
				want.live[k] = v
			}
			if err := w.Write(b); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			want.keys = want.keys[:0]
			for k := range want.live {
				want.keys = append(want.keys, k)
			}
			sort.Strings(want.keys)
			want.deleted = slicesWithout(want.deleted, want.live)

			if err := leveldb.CheckCompressions(dir); err != nil {
				t.Fatalf("CheckCompressions after a Go write: %v", err)
			}
			got, r := readSnapshot(t, o, dir)
			sameContent(t, "snapshot read after a Go write", want, got)
			checkGets(t, r, want)
			sameContent(t, "C++ dump after a Go write", want, parseCXX(t, runCXX(t, bin, "dump", dir)))
			again, r2 := readSnapshot(t, o, dir)
			sameContent(t, "snapshot read after the C++ dump", want, again)
			checkGets(t, r2, want)
		})
	}
}

// slicesWithout drops keys that are live again.
func slicesWithout(keys []string, live map[string]string) []string {
	out := keys[:0]
	for _, k := range keys {
		if _, ok := live[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}
