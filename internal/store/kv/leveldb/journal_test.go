package leveldb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	lerrors "github.com/syndtr/goleveldb/leveldb/errors"
	"github.com/syndtr/goleveldb/leveldb/journal"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"

	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// These tests build, with goleveldb and a hand-written journal, the state
// the C++ LevelDB library leaves behind when a compaction commits a
// manifest record after later writes reached the current journal: the
// journal holds batches numbered below the manifest's last sequence.

// testOp is one operation of a hand-written journal batch.
type testOp struct {
	del        bool
	key, value string
}

// testBatch is a hand-written journal batch with its sequence number.
type testBatch struct {
	seq uint64
	ops []testOp
}

// encodeBatch encodes b in LevelDB's write batch format: an 8-byte
// sequence number, a 4-byte count, then per record a type byte (1 put,
// 0 delete) and length-prefixed key and value.
func encodeBatch(b testBatch) []byte {
	out := binary.LittleEndian.AppendUint64(nil, b.seq)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(b.ops)))
	for _, op := range b.ops {
		if op.del {
			out = append(out, 0)
			out = binary.AppendUvarint(out, uint64(len(op.key)))
			out = append(out, op.key...)
			continue
		}
		out = append(out, 1)
		out = binary.AppendUvarint(out, uint64(len(op.key)))
		out = append(out, op.key...)
		out = binary.AppendUvarint(out, uint64(len(op.value)))
		out = append(out, op.value...)
	}
	return out
}

// writeJournalFile replaces path with a journal holding records.
func writeJournalFile(t *testing.T, path string, records [][]byte) {
	t.Helper()
	var buf bytes.Buffer
	w := journal.NewWriter(&buf)
	for _, rec := range records {
		jw, err := w.Next()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := jw.Write(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readJournalFile returns the records of a journal file.
func readJournalFile(t *testing.T, path string) [][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	jr := journal.NewReader(f, nil, true, true)
	var out [][]byte
	for {
		r, err := jr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
}

// currentManifest returns the path of the manifest CURRENT names.
func currentManifest(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "CURRENT"))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, strings.TrimSuffix(string(b), "\n"))
}

// raiseManifestSeq appends a manifest record that sets the last sequence
// to seq, as a C++ compaction does when it commits while later writes
// are in the current journal.
func raiseManifestSeq(t *testing.T, dir string, seq uint64) {
	t.Helper()
	path := currentManifest(t, dir)
	recs := readJournalFile(t, path)
	rec := binary.AppendUvarint(nil, 4) // last sequence
	rec = binary.AppendUvarint(rec, seq)
	writeJournalFile(t, path, append(recs, rec))
}

// newestJournal returns the path of the highest-numbered journal in dir.
func newestJournal(t *testing.T, dir string) string {
	t.Helper()
	logs, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("no journal in the test database: %v %v", logs, err)
	}
	sort.Strings(logs)
	return logs[len(logs)-1]
}

// Keys and values of the hand-written journal. The table seqs of makeDB
// are below 100, the journal batches start at 500 and the manifest is
// raised to 10000.
var (
	journalOnlyKey = "_https://example.com\x00\x01journal-only"
	journalBin     = "\xff\x00b\x00\x00\x00\x07"
)

func journalBatches(t *testing.T) []testBatch {
	return []testBatch{
		{seq: 500, ops: []testOp{
			{key: journalOnlyKey, value: "from the journal"},
			{del: true, key: string(lsKey(t, items[0].name))},
			{key: journalBin, value: "\x00\x01\xff"},
		}},
		{seq: 503, ops: []testOp{
			{key: string(lsKey(t, items[1].name)), value: string(localstorage.EncodeString("overwritten in the journal"))},
		}},
		{seq: 504, ops: []testOp{
			{key: journalBin, value: ""},
			{del: true, key: "never-existed"},
		}},
	}
}

// journalWant is the content after replaying journalBatches over makeDB.
func journalWant(t *testing.T) map[string]string {
	want := map[string]string{string(localstorage.MetaKey): "meta"}
	for _, it := range items {
		want[string(lsKey(t, it.name))] = string(localstorage.EncodeString(it.value))
	}
	delete(want, string(lsKey(t, items[0].name)))
	want[journalOnlyKey] = "from the journal"
	want[string(lsKey(t, items[1].name))] = string(localstorage.EncodeString("overwritten in the journal"))
	want[journalBin] = ""
	return want
}

// makeJournalDB builds makeDB's database, replaces its newest journal
// with batches and, when raise is set, raises the manifest's last
// sequence above them.
func makeJournalDB(t *testing.T, batches []testBatch, raise bool) env {
	t.Helper()
	e := newEnv(t, opt.SnappyCompression)
	var recs [][]byte
	for _, b := range batches {
		recs = append(recs, encodeBatch(b))
	}
	writeJournalFile(t, newestJournal(t, e.dir), recs)
	if raise {
		raiseManifestSeq(t, e.dir, 10000)
	}
	return e
}

func assertContent(t *testing.T, what string, r kv.Reader, want map[string]string) {
	t.Helper()
	got := readAll(t, r, nil)
	for k, v := range want {
		g, ok := got[k]
		if !ok {
			t.Fatalf("%s: missing %q", what, k)
		}
		if g != v {
			t.Fatalf("%s: %q = %q, want %q", what, k, g, v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Fatalf("%s: unexpected %q", what, k)
		}
	}
}

// TestJournalFixtureControl: without the raised manifest the hand-written
// journal is replayed by goleveldb itself, so the fixture is sound.
func TestJournalFixtureControl(t *testing.T) {
	e := makeJournalDB(t, journalBatches(t), false)
	if err := CheckJournals(e.dir); err != nil {
		t.Fatalf("CheckJournals: %v", err)
	}
	r, err := e.o.OpenSnapshot(context.Background(), e.dir)
	if err != nil {
		t.Fatal(err)
	}
	assertContent(t, "snapshot", r, journalWant(t))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestGoleveldbDropsJournalBehindManifest pins the goleveldb behaviour
// this package works around, on the fixture the other tests use: a
// lenient open drops the batches without an error and a strict open
// refuses the database. If goleveldb changes, this test fails and
// journal.go must be revisited.
func TestGoleveldbDropsJournalBehindManifest(t *testing.T) {
	e := makeJournalDB(t, journalBatches(t), true)
	probe := filepath.Join(t.TempDir(), "probe")
	copyDir(t, e.dir, probe)
	if err := os.Remove(filepath.Join(probe, "LOCK")); err != nil {
		t.Fatal(err)
	}
	db, err := leveldb.OpenFile(probe, snapshotOptions())
	if err != nil {
		t.Fatalf("lenient read-only open: %v", err)
	}
	_, getErr := db.Get([]byte(journalOnlyKey), nil)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(getErr, leveldb.ErrNotFound) {
		t.Fatalf("lenient goleveldb did not drop a batch behind the manifest (Get: %v); the fixture or goleveldb changed", getErr)
	}
	db, err = leveldb.OpenFile(probe, writableOptions())
	if err == nil {
		db.Close()
		t.Fatal("strict goleveldb opened a journal behind the manifest; the fixture or goleveldb changed")
	}
	if !lerrors.IsCorrupted(err) || !strings.Contains(err.Error(), "invalid sequence number") {
		t.Fatalf("strict goleveldb open: %v, want a corrupted batch (invalid sequence number)", err)
	}
}

// TestSnapshotReplaysJournalBehindManifest: OpenSnapshot replays every
// batch of such a journal on its private clone, in order, and leaves the
// original untouched.
func TestSnapshotReplaysJournalBehindManifest(t *testing.T) {
	e := makeJournalDB(t, journalBatches(t), true)
	before := hashTree(t, e.dir)
	r, err := e.o.OpenSnapshot(context.Background(), e.dir)
	if err != nil {
		t.Fatalf("OpenSnapshot: %v", err)
	}
	want := journalWant(t)
	assertContent(t, "snapshot", r, want)
	for _, k := range []string{string(lsKey(t, items[0].name)), "never-existed"} {
		if _, ok, err := r.Get([]byte(k)); err != nil || ok {
			t.Fatalf("Get deleted %q: found=%v err=%v", k, ok, err)
		}
	}
	if v, ok, err := r.Get([]byte(journalOnlyKey)); err != nil || !ok || string(v) != want[journalOnlyKey] {
		t.Fatalf("Get %q: %q found=%v err=%v", journalOnlyKey, v, ok, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	assertSameTree(t, e.dir, before)
	assertTempRootEmpty(t, e.root)
	assertCallsConfined(t, e)
}

// TestOpenWritableReplaysJournalBehindManifest: OpenWritable opens such a
// database with every batch applied in order, a write lands on top, and
// the database is left in a state goleveldb reads without help.
func TestOpenWritableReplaysJournalBehindManifest(t *testing.T) {
	e := makeJournalDB(t, journalBatches(t), true)
	behind := filepath.Base(newestJournal(t, e.dir))
	err := CheckJournals(e.dir)
	if !errors.Is(err, kv.ErrJournalSequence) {
		t.Fatalf("CheckJournals: %v, want ErrJournalSequence", err)
	}
	if !strings.Contains(err.Error(), behind) {
		t.Fatalf("error does not name the journal: %v", err)
	}
	w, err := e.o.OpenWritable(context.Background(), e.dir)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	want := journalWant(t)
	assertContent(t, "writable", w, want)
	b := &kv.Batch{}
	b.Put([]byte("after-replay"), []byte("x"))
	b.Delete([]byte(journalOnlyKey))
	if err := w.Write(b); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want["after-replay"] = "x"
	delete(want, journalOnlyKey)
	if err := CheckJournals(e.dir); err != nil {
		t.Fatalf("CheckJournals after the write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, replayTempName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("temporary replay journal left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, behind)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the journal behind the manifest is still there: %v", err)
	}
	r, err := e.o.OpenSnapshot(context.Background(), e.dir)
	if err != nil {
		t.Fatal(err)
	}
	assertContent(t, "snapshot after the write", r, want)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := leveldb.OpenFile(e.dir, writableOptions())
	if err != nil {
		t.Fatalf("strict goleveldb open after the write: %v", err)
	}
	defer db.Close()
	if v, err := db.Get([]byte("after-replay"), nil); err != nil || string(v) != "x" {
		t.Fatalf("strict goleveldb Get: %q %v", v, err)
	}
}

// TestReplayJournalCrashState: a crash between adding the replay journal
// and goleveldb's open (or during either) leaves a state that goleveldb
// on its own, the snapshot path and a second OpenWritable all read as the
// full replay; a temporary file left by a crash during the write is
// ignored. (The C++ library's reading of this state is checked by the
// differential tests.)
func TestReplayJournalCrashState(t *testing.T) {
	e := makeJournalDB(t, journalBatches(t), true)
	st, err := inspectJournals(e.dir, writableOptions())
	if err != nil {
		t.Fatal(err)
	}
	name, err := writeReplayJournal(e.dir, st)
	if err != nil {
		t.Fatalf("writeReplayJournal: %v", err)
	}
	if name != filepath.Base(newestJournal(t, e.dir)) {
		t.Fatalf("replay journal %s is not the newest journal", name)
	}
	if err := os.WriteFile(filepath.Join(e.dir, replayTempName), []byte("torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := journalWant(t)

	// goleveldb alone, leniently, on a copy: the old copies are dropped
	// and the replay journal applied.
	probe := filepath.Join(t.TempDir(), "probe")
	copyDir(t, e.dir, probe)
	db, err := leveldb.OpenFile(probe, cloneRecoveryOptions())
	if err != nil {
		t.Fatalf("lenient goleveldb open: %v", err)
	}
	got := map[string]string{}
	it := db.NewIterator(nil, nil)
	for it.Next() {
		got[string(it.Key())] = string(it.Value())
	}
	it.Release()
	if err := errors.Join(it.Error(), db.Close()); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, want) {
		t.Fatalf("lenient goleveldb read %v, want %v", got, want)
	}

	r, err := e.o.OpenSnapshot(context.Background(), e.dir)
	if err != nil {
		t.Fatalf("OpenSnapshot: %v", err)
	}
	assertContent(t, "snapshot", r, want)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	w, err := e.o.OpenWritable(context.Background(), e.dir)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	assertContent(t, "writable", w, want)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := CheckJournals(e.dir); err != nil {
		t.Fatalf("CheckJournals: %v", err)
	}
}

// TestJournalOutOfOrderFailsClosed: batches numbered below the end of the
// batch before them are not a state the C++ library writes; both paths
// refuse the database instead of guessing an order.
func TestJournalOutOfOrderFailsClosed(t *testing.T) {
	for _, raise := range []bool{false, true} {
		t.Run(fmt.Sprintf("raised=%v", raise), func(t *testing.T) {
			batches := journalBatches(t)
			batches[2].seq = 501 // overlaps the first batch
			e := makeJournalDB(t, batches, raise)
			before := hashTree(t, e.dir)
			if _, err := e.o.OpenSnapshot(context.Background(), e.dir); !errors.Is(err, kv.ErrJournalSequence) {
				t.Fatalf("OpenSnapshot: %v, want ErrJournalSequence", err)
			}
			if _, err := e.o.OpenWritable(context.Background(), e.dir); !errors.Is(err, kv.ErrJournalSequence) {
				t.Fatalf("OpenWritable: %v, want ErrJournalSequence", err)
			}
			assertSameTree(t, e.dir, before)
			assertTempRootEmpty(t, e.root)
		})
	}
}

// TestCheckJournalsRefusesUnknownManifest: a manifest this package
// cannot read in full, or no CURRENT file naming one, is refused rather
// than guessed at.
func TestCheckJournalsRefusesUnknownManifest(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"unknown tag": func(t *testing.T, dir string) {
			path := currentManifest(t, dir)
			writeJournalFile(t, path, append(readJournalFile(t, path), binary.AppendUvarint(nil, 8)))
		},
		"another comparator": func(t *testing.T, dir string) {
			// goleveldb refuses it when it opens the database; this
			// package refuses it before OpenWritable writes anything.
			path := currentManifest(t, dir)
			rec := binary.AppendUvarint(nil, 1)
			rec = binary.AppendUvarint(rec, uint64(len("other.Comparator")))
			rec = append(rec, "other.Comparator"...)
			writeJournalFile(t, path, append(readJournalFile(t, path), rec))
		},
		"no CURRENT nor CURRENT.bak": func(t *testing.T, dir string) {
			for _, n := range []string{"CURRENT", "CURRENT.bak"} {
				if err := os.Remove(filepath.Join(dir, n)); err != nil && !errors.Is(err, fs.ErrNotExist) {
					t.Fatal(err)
				}
			}
		},
		"CURRENT names a journal": func(t *testing.T, dir string) {
			name := filepath.Base(newestJournal(t, dir)) + "\n"
			if err := os.WriteFile(filepath.Join(dir, "CURRENT"), []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, opt.SnappyCompression)
			mutate(t, e.dir)
			before := hashTree(t, e.dir)
			if err := CheckJournals(e.dir); err == nil {
				t.Fatal("CheckJournals accepted the database")
			}
			if w, err := e.o.OpenWritable(context.Background(), e.dir); err == nil {
				w.Close()
				t.Fatal("OpenWritable accepted the database")
			}
			if r, err := e.o.OpenSnapshot(context.Background(), e.dir); err == nil {
				r.Close()
				t.Fatal("OpenSnapshot accepted the database")
			}
			assertSameTree(t, e.dir, before)
			assertTempRootEmpty(t, e.root)
		})
	}
}

// TestCurrentFileResolution: the manifest is chosen as goleveldb chooses
// it, so a pending CURRENT.<n> left by an interrupted goleveldb write, or
// a CURRENT.bak without CURRENT, does not block the database.
func TestCurrentFileResolution(t *testing.T) {
	t.Run("pending CURRENT naming a missing manifest is skipped", func(t *testing.T) {
		e := makeJournalDB(t, journalBatches(t), false)
		if err := os.WriteFile(filepath.Join(e.dir, "CURRENT.99"), []byte("MANIFEST-000099\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertOpensWith(t, e, nil)
	})
	t.Run("CURRENT.bak without CURRENT", func(t *testing.T) {
		e := makeJournalDB(t, journalBatches(t), false)
		if err := os.Rename(filepath.Join(e.dir, "CURRENT"), filepath.Join(e.dir, "CURRENT.bak")); err != nil {
			t.Fatal(err)
		}
		assertOpensWith(t, e, nil)
	})
	t.Run("pending CURRENT naming a newer manifest wins", func(t *testing.T) {
		// CURRENT names a manifest whose last sequence is below the
		// journal; the pending file names a copy raised above it. Only
		// reading the pending manifest finds the journal behind it.
		e := makeJournalDB(t, journalBatches(t), false)
		recs := readJournalFile(t, currentManifest(t, e.dir))
		raised := binary.AppendUvarint(binary.AppendUvarint(nil, 4), 10000)
		writeJournalFile(t, filepath.Join(e.dir, "MANIFEST-000099"), append(recs, raised))
		if err := os.WriteFile(filepath.Join(e.dir, "CURRENT.99"), []byte("MANIFEST-000099\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertOpensWith(t, e, kv.ErrJournalSequence)
	})
}

// assertOpensWith checks that CheckJournals returns an error matching
// want (nil for none) and that OpenSnapshot and then OpenWritable read
// journalWant.
func assertOpensWith(t *testing.T, e env, want error) {
	t.Helper()
	if err := CheckJournals(e.dir); !errors.Is(err, want) {
		t.Fatalf("CheckJournals: %v, want %v", err, want)
	}
	r, err := e.o.OpenSnapshot(context.Background(), e.dir)
	if err != nil {
		t.Fatalf("OpenSnapshot: %v", err)
	}
	assertContent(t, "snapshot", r, journalWant(t))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	assertTempRootEmpty(t, e.root)
	w, err := e.o.OpenWritable(context.Background(), e.dir)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	assertContent(t, "writable", w, journalWant(t))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := CheckJournals(e.dir); err != nil {
		t.Fatalf("CheckJournals after OpenWritable: %v", err)
	}
}

// journalFiles returns the journal names in dir with their sizes.
func journalFiles(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, de := range es {
		if strings.HasSuffix(de.Name(), ".log") {
			fi, err := de.Info()
			if err != nil {
				t.Fatal(err)
			}
			out[de.Name()] = fi.Size()
		}
	}
	return out
}

// TestOpenWritableLockHeldWritesNothing: while another open file holds
// the database's LOCK, OpenWritable on a journal behind the manifest
// fails with kv.ErrLocked and writes nothing, so no replay journal can
// be applied after the holder's later writes.
func TestOpenWritableLockHeldWritesNothing(t *testing.T) {
	e := makeJournalDB(t, journalBatches(t), true)
	f, err := os.OpenFile(filepath.Join(e.dir, "LOCK"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	before := hashTree(t, e.dir)
	for range 3 {
		w, err := e.o.OpenWritable(context.Background(), e.dir)
		if err == nil {
			w.Close()
			t.Fatal("OpenWritable opened a locked database")
		}
		if !errors.Is(err, kv.ErrLocked) {
			t.Fatalf("OpenWritable: %v, want kv.ErrLocked", err)
		}
		assertSameTree(t, e.dir, before)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	w, err := e.o.OpenWritable(context.Background(), e.dir)
	if err != nil {
		t.Fatalf("OpenWritable after the lock was released: %v", err)
	}
	assertContent(t, "writable", w, journalWant(t))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// lockHelperEnv makes the test binary hold a POSIX record lock on the
// named file, as the C++ library (and so Chromium) does, until killed.
const lockHelperEnv = "PARITYDESK_TEST_FCNTL_LOCK"

func TestMain(m *testing.M) {
	if path := os.Getenv(lockHelperEnv); path != "" {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err == nil {
			err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0})
		}
		if err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		fmt.Println("locked")
		// Sleep, not select{}: a process whose goroutines all block
		// forever is stopped by the runtime as a deadlock, releasing the
		// lock at once.
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(m.Run())
}

// TestOpenWritableRecordLockHeld: on macOS, a POSIX record lock on LOCK
// held by another process (the C++ library's kind) also stops
// OpenWritable with nothing written. (On Linux flock and record locks do
// not see each other, so the test runs on macOS only.)
func TestOpenWritableRecordLockHeld(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("flock and fcntl locks interact only on macOS")
	}
	e := makeJournalDB(t, journalBatches(t), true)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+filepath.Join(e.dir, "LOCK"))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Kill()
		<-exited
	}()
	if err != nil || line != "locked\n" {
		t.Fatalf("lock helper: %q %v", line, err)
	}
	before := hashTree(t, e.dir)
	w, err := e.o.OpenWritable(context.Background(), e.dir)
	select {
	case werr := <-exited:
		exited <- werr
		t.Fatalf("the lock helper exited before the check ended: %v", werr)
	default:
	}
	if err == nil {
		w.Close()
		t.Fatal("OpenWritable opened a database another process holds")
	}
	if !errors.Is(err, kv.ErrLocked) {
		t.Fatalf("OpenWritable: %v, want kv.ErrLocked", err)
	}
	assertSameTree(t, e.dir, before)
}

// TestOpenWritableRetryAddsNoSecondReplayJournal: when goleveldb's open
// fails after the replay journal was added, later attempts find it and
// add no other (each would hold every batch of all journals before it,
// doubling the journals on every retry), and the content stays the full
// replay for every reader.
func TestOpenWritableRetryAddsNoSecondReplayJournal(t *testing.T) {
	e := makeJournalDB(t, journalBatches(t), true)
	journals := journalFiles(t, e.dir)
	openErr := errors.New("injected open failure")
	openDB = func(storage.Storage, *opt.Options) (*leveldb.DB, error) { return nil, openErr }
	t.Cleanup(func() { openDB = leveldb.Open })
	var after map[string]int64
	for i := range 4 {
		w, err := e.o.OpenWritable(context.Background(), e.dir)
		if err == nil {
			w.Close()
			t.Fatal("OpenWritable succeeded with a failing open")
		}
		if !errors.Is(err, openErr) {
			t.Fatalf("OpenWritable #%d: %v", i+1, err)
		}
		got := journalFiles(t, e.dir)
		if i == 0 {
			if len(got) != len(journals)+1 {
				t.Fatalf("after the first attempt: journals %v, want one replay journal added to %v", got, journals)
			}
			after = got
			continue
		}
		if !maps.Equal(got, after) {
			t.Fatalf("attempt %d changed the journals: %v, was %v", i+1, got, after)
		}
	}
	r, err := e.o.OpenSnapshot(context.Background(), e.dir)
	if err != nil {
		t.Fatal(err)
	}
	assertContent(t, "snapshot", r, journalWant(t))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	openDB = leveldb.Open
	w, err := e.o.OpenWritable(context.Background(), e.dir)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	assertContent(t, "writable", w, journalWant(t))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := CheckJournals(e.dir); err != nil {
		t.Fatalf("CheckJournals: %v", err)
	}
}

// TestReplayedNeedsAnExactReplay: a newest journal counts as a replay
// journal only when it holds exactly the earlier journals' batches, in
// order, numbered above them.
func TestReplayedNeedsAnExactReplay(t *testing.T) {
	e := makeJournalDB(t, journalBatches(t), true)
	st, err := inspectJournals(e.dir, writableOptions())
	if err != nil {
		t.Fatal(err)
	}
	if st.replayed() {
		t.Fatal("a journal behind the manifest alone counts as replayed")
	}
	name, err := writeReplayJournal(e.dir, st)
	if err != nil {
		t.Fatal(err)
	}
	st, err = inspectJournals(e.dir, writableOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !st.replayed() {
		t.Fatal("the replay journal is not recognised")
	}
	// An empty journal after it (a goleveldb open that failed at its
	// commit leaves one) does not hide it.
	empty := filepath.Join(e.dir, fmt.Sprintf("%06d.log", st.maxFileNum+1))
	writeJournalFile(t, empty, nil)
	if st, err = inspectJournals(e.dir, writableOptions()); err != nil || !st.replayed() {
		t.Fatalf("the replay journal is not recognised before an empty journal: %v", err)
	}
	if err := os.Remove(empty); err != nil {
		t.Fatal(err)
	}
	recs := readJournalFile(t, filepath.Join(e.dir, name))
	for what, mutate := range map[string]func([][]byte) [][]byte{
		"a batch missing": func(r [][]byte) [][]byte { return r[:len(r)-1] },
		"batches swapped": func(r [][]byte) [][]byte { return [][]byte{r[1], r[0], r[2]} },
		"a value changed": func(r [][]byte) [][]byte {
			c := slices.Clone(r[0])
			c[len(c)-1] ^= 1
			return append([][]byte{c}, r[1:]...)
		},
		"numbered too low": func(r [][]byte) [][]byte { return renumber(r, 9000) },
	} {
		t.Run(what, func(t *testing.T) {
			writeJournalFile(t, filepath.Join(e.dir, name), mutate(slices.Clone(recs)))
			st, err := inspectJournals(e.dir, writableOptions())
			if err != nil {
				t.Fatal(err)
			}
			if st.replayed() {
				t.Fatal("counted as a replay journal")
			}
		})
	}
}

// renumber returns batch records renumbered from seq on.
func renumber(recs [][]byte, seq uint64) [][]byte {
	out := make([][]byte, len(recs))
	for i, r := range recs {
		c := slices.Clone(r)
		binary.LittleEndian.PutUint64(c, seq)
		seq += uint64(binary.LittleEndian.Uint32(c[8:]))
		out[i] = c
	}
	return out
}
