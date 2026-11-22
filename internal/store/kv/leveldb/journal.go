package leveldb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/journal"
	"github.com/syndtr/goleveldb/leveldb/opt"

	"github.com/kazupc/paritydesk/internal/store/kv"
)

// Journal sequence check.
//
// LevelDB numbers every write with a sequence number. The manifest records
// the last one, and the journals numbered at or above the manifest's
// journal number hold the writes that are not in a table yet. goleveldb
// replays a journal batch only when its number is at least the last
// sequence number it has seen, starting from the manifest's: a lenient
// open drops any other batch without an error, and a strict open refuses
// the database ("batch corrupted: invalid sequence number").
//
// The C++ library (Chromium's included) writes its current last sequence
// number into every manifest record, also when a memtable flush or a table
// compaction commits after later writes reached the current journal, and
// it does not flush the memtable when the database is closed. Its
// journals therefore often begin with batches numbered below the
// manifest's last sequence. Those batches are still the newest writes:
// every table holds older ones, and the C++ library replays them all.
//
// inspectJournals reads the manifest and the journals the way goleveldb
// will with the same options, and reports such batches. OpenSnapshot then
// replays the journals itself on its private clone. OpenWritable adds a
// replay journal (writeReplayJournal) to the original before it opens it:
// a new journal, numbered above every file, holding every batch of the
// replayed journals in order, renumbered above every sequence number in
// use. goleveldb skips the batches it would drop and then applies the
// replay journal, so the result is the C++ library's: every batch in
// order. The C++ library, which replays every journal, applies the old
// batches and then the same batches again in the same order, which gives
// the same content; so does goleveldb if it stops partway, since the
// replay journal is removed only after its batches are in a table.

// bytewiseComparator is the comparator name both libraries use by
// default, and the only one goleveldb opens a database with.
const bytewiseComparator = "leveldb.BytewiseComparator"

// batchHeaderLen is a journal batch's sequence number and record count.
const batchHeaderLen = 12

// maxSequence is the largest sequence number LevelDB can store (56 bits).
const maxSequence = 1<<56 - 1

// replayTempName is the file writeReplayJournal writes before renaming it
// to a journal name. Neither goleveldb nor the C++ library parses this
// name, so both ignore it if a crash leaves it behind.
const replayTempName = "paritydesk-replay-journal.tmp"

// Manifest field tags (LevelDB's VersionEdit).
const (
	tagComparator     = 1
	tagJournalNum     = 2
	tagNextFileNum    = 3
	tagLastSequence   = 4
	tagCompactPointer = 5
	tagDeletedFile    = 6
	tagNewFile        = 7
	tagPrevJournalNum = 9
)

var (
	// errManifestShort marks a manifest record that ends inside a field.
	errManifestShort = errors.New("manifest record ends inside a field")
	// errManifest marks a manifest record this package does not read.
	errManifest = errors.New("unreadable manifest record")
)

// manifestState is what the manifest's records add up to.
type manifestState struct {
	journalNum, prevJournalNum, nextFileNum int64
	lastSeq                                 uint64
	hasJournalNum, hasLastSeq               bool
	hasNextFileNum                          bool
	comparator                              string
	hasComparator                           bool
	// tables are the live tables: those the records add and do not
	// delete later, as both libraries build their version.
	tables map[tableID]struct{}
}

// tableID is a table file in a level of the manifest's version.
type tableID struct {
	level uint64
	num   int64
}

// tableNums returns the numbers of the live tables, in order.
func (m manifestState) tableNums() []int64 {
	seen := map[int64]bool{}
	var out []int64
	for id := range m.tables {
		if !seen[id.num] {
			seen[id.num] = true
			out = append(out, id.num)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// journalState is what inspectJournals found.
type journalState struct {
	// journals are the names of the journals goleveldb replays, in order.
	journals []string
	// batches are the batches goleveldb would decode, in replay order.
	batches []*leveldb.Batch
	// info describes batches[i]: its journal, number and whether
	// goleveldb drops it.
	info []batchInfo
	// behind names the first journal holding a batch goleveldb would drop
	// (or refuse) for its sequence number; empty if there is none.
	behind string
	// disordered names the first journal holding a batch numbered below
	// the end of the batch before it; empty if there is none.
	disordered string
	// manifest is what the manifest records.
	manifest manifestState
	// next is goleveldb's sequence number after the replay: the end of
	// the last batch it applies, or the manifest's last sequence.
	next uint64
	// maxEnd is the largest batch end (sequence number plus count).
	maxEnd uint64
	// maxFileNum is the largest file number in the directory.
	maxFileNum int64
}

// batchInfo is where a journal batch is and how goleveldb treats it.
type batchInfo struct {
	journal int // index into journalState.journals
	seq     uint64
	dropped bool // numbered below goleveldb's running sequence number
}

// replayed reports whether the newest journal holding batches already is
// a replay journal for the ones before it: it holds their non-empty
// batches again, in order, numbered above all of them, and goleveldb
// drops none of its batches. (Empty journals after it, such as the new
// journal of a goleveldb open that failed at its commit, do not matter.)
// That is the state writeReplayJournal leaves (and a crash right after
// it, or an open that failed later), so OpenWritable then opens the
// database without adding another replay journal; a second one would hold
// every batch of the first again and double the journals on every retry.
func (s *journalState) replayed() bool {
	if len(s.info) == 0 || s.behind == "" {
		return false
	}
	n := s.info[len(s.info)-1].journal + 1 // journals up to the newest with batches
	if n < 2 || s.behind == s.journals[n-1] {
		return false
	}
	var earlier, last []int
	var earlierEnd uint64
	for i, in := range s.info {
		switch {
		case in.journal < n-1:
			earlierEnd = max(earlierEnd, in.seq+uint64(s.batches[i].Len()))
			if s.batches[i].Len() > 0 {
				earlier = append(earlier, i)
			}
		default:
			if in.dropped {
				return false
			}
			last = append(last, i)
		}
	}
	if len(last) == 0 || len(last) != len(earlier) || s.info[last[0]].seq < earlierEnd {
		return false
	}
	for k, i := range last {
		if !bytes.Equal(s.batches[i].Dump(), s.batches[earlier[k]].Dump()) {
			return false
		}
	}
	return true
}

// sequenceErr returns an error wrapping kv.ErrJournalSequence when the
// journals hold a batch goleveldb would not replay, or nil.
func (s *journalState) sequenceErr() error {
	switch {
	case s.disordered != "":
		return fmt.Errorf("leveldb: journal %s holds batches out of order: %w", s.disordered, kv.ErrJournalSequence)
	case s.behind != "":
		return fmt.Errorf("leveldb: journal %s holds batches numbered below the manifest's last sequence, which goleveldb alone would not replay: %w", s.behind, kv.ErrJournalSequence)
	}
	return nil
}

// CheckJournals reads dir's manifest and the journals LevelDB would replay
// with strict checks, as OpenWritable reads them before it opens the
// database. It returns an error wrapping kv.ErrJournalSequence when a
// journal holds a batch that goleveldb on its own would not replay (which
// OpenWritable and OpenSnapshot handle) or batches out of order (which
// they refuse), and another error when the manifest or a journal cannot
// be read. It only reads files.
func CheckJournals(dir string) error {
	st, err := inspectJournals(dir, writableOptions())
	if err != nil {
		return err
	}
	return st.sequenceErr()
}

// inspectJournals reads dir's manifest and journals with the strictness o
// gives goleveldb.
func inspectJournals(dir string, o *opt.Options) (*journalState, error) {
	m, err := readManifest(dir, o.GetStrict(opt.StrictManifest))
	if err != nil {
		return nil, err
	}
	names, maxNum, err := replayedJournals(dir, m)
	if err != nil {
		return nil, err
	}
	st := &journalState{journals: names, manifest: m, maxFileNum: maxNum}
	var (
		strict   = o.GetStrict(opt.StrictJournal)
		checksum = o.GetStrict(opt.StrictJournalChecksum)
		expect   = m.lastSeq // goleveldb's running sequence number
		prevEnd  uint64
		seen     bool
	)
	for ji, name := range names {
		err := readJournal(filepath.Join(dir, name), strict, checksum, func(seq uint64, b *leveldb.Batch) {
			end := seq + uint64(b.Len())
			if seen && seq < prevEnd && st.disordered == "" {
				st.disordered = name
			}
			st.maxEnd = max(st.maxEnd, end)
			dropped := seq < expect
			if dropped {
				if st.behind == "" {
					st.behind = name
				}
			} else {
				expect = end
			}
			prevEnd, seen = end, true
			st.batches = append(st.batches, b)
			st.info = append(st.info, batchInfo{journal: ji, seq: seq, dropped: dropped})
		})
		if err != nil {
			return nil, fmt.Errorf("leveldb: journal %s: %w", name, err)
		}
	}
	st.next = expect
	return st, nil
}

// readJournal calls fn for every batch of a journal that goleveldb would
// decode, skipping what a lenient goleveldb skips.
func readJournal(path string, strict, checksum bool, fn func(seq uint64, b *leveldb.Batch)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	jr := journal.NewReader(f, nil, strict, checksum)
	for {
		r, err := jr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		data, err := io.ReadAll(r)
		if err == io.ErrUnexpectedEOF {
			continue // a torn record; goleveldb skips it too
		}
		if err != nil {
			return err
		}
		if len(data) < batchHeaderLen {
			if strict {
				return errors.New("batch too short")
			}
			continue
		}
		b := new(leveldb.Batch)
		err = b.Load(data[batchHeaderLen:])
		if err == nil && b.Len() != int(binary.LittleEndian.Uint32(data[8:])) {
			err = errors.New("batch record count differs from its header")
		}
		if err != nil {
			if strict {
				return err
			}
			continue
		}
		fn(binary.LittleEndian.Uint64(data), b)
	}
}

// replayedJournals lists the journals goleveldb replays for m, in order:
// those numbered at or above the manifest's journal number, and the
// previous journal. Names are parsed as goleveldb parses them. It also
// returns the largest file number in dir.
func replayedJournals(dir string, m manifestState) ([]string, int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("leveldb: list database directory: %w", err)
	}
	type numbered struct {
		num  int64
		name string
	}
	var (
		js     []numbered
		maxNum int64
	)
	for _, e := range entries {
		num, kind, ok := parseFileName(e.Name())
		if !ok {
			continue
		}
		maxNum = max(maxNum, num)
		if kind == "log" && (num >= m.journalNum || num == m.prevJournalNum) {
			js = append(js, numbered{num, e.Name()})
		}
	}
	sort.Slice(js, func(i, j int) bool { return js[i].num < js[j].num })
	names := make([]string, len(js))
	for i, j := range js {
		names[i] = j.name
	}
	return names, maxNum, nil
}

// parseFileName parses a database file name as goleveldb does
// (storage.fsParseName): "<n>.log", "<n>.ldb", "<n>.sst", "<n>.tmp" or
// "MANIFEST-<n>". kind is the suffix, or "manifest".
func parseFileName(name string) (num int64, kind string, ok bool) {
	var tail string
	if _, err := fmt.Sscanf(name, "%d.%s", &num, &tail); err == nil {
		switch tail {
		case "log", "ldb", "sst", "tmp":
			return num, tail, true
		}
		return 0, "", false
	}
	if n, _ := fmt.Sscanf(name, "MANIFEST-%d%s", &num, &tail); n == 1 {
		return num, "manifest", true
	}
	return 0, "", false
}

// readManifest reads the manifest CURRENT names. Records that end inside
// a field are skipped unless strict, keeping the fields read before, as
// goleveldb does; any other record this package cannot read is an error.
func readManifest(dir string, strict bool) (manifestState, error) {
	var m manifestState
	name, err := currentManifestName(dir)
	if err != nil {
		return m, err
	}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return m, fmt.Errorf("leveldb: open manifest: %w", err)
	}
	defer f.Close()
	jr := journal.NewReader(f, nil, strict, true)
	for {
		r, err := jr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, fmt.Errorf("leveldb: read manifest %s: %w", name, err)
		}
		rec, readErr := io.ReadAll(r)
		if readErr != nil && (strict || readErr != io.ErrUnexpectedEOF) {
			return m, fmt.Errorf("leveldb: read manifest %s: %w", name, readErr)
		}
		err = m.decode(rec)
		if err == nil && readErr != nil {
			err = errManifestShort
		}
		if err != nil && (strict || !errors.Is(err, errManifestShort)) {
			return m, fmt.Errorf("leveldb: manifest %s: %w", name, err)
		}
	}
	if !m.hasJournalNum || !m.hasLastSeq {
		return m, fmt.Errorf("leveldb: manifest %s: %w: no journal number or last sequence", name, errManifest)
	}
	// goleveldb refuses these when it opens the database; refusing them
	// here keeps OpenWritable from writing anything first.
	if !m.hasNextFileNum {
		return m, fmt.Errorf("leveldb: manifest %s: %w: no next file number", name, errManifest)
	}
	if !m.hasComparator || m.comparator != bytewiseComparator {
		return m, fmt.Errorf("leveldb: manifest %s: %w: comparator is not %s", name, errManifest, bytewiseComparator)
	}
	return m, nil
}

// currentManifestName returns the name of the manifest goleveldb opens,
// chosen as goleveldb's storage.GetMeta chooses it: the pending
// CURRENT.<n> files a goleveldb write may leave behind (highest first),
// then CURRENT, then CURRENT.bak. A candidate whose content is not a file
// name, or whose manifest is missing, is skipped; a pending file is used
// only when it names a newer manifest than CURRENT (or CURRENT.bak). A
// candidate naming a file that is not a manifest is refused.
func currentManifestName(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("leveldb: list database directory: %w", err)
	}
	var pending []int64
	for _, e := range entries {
		if rest, ok := strings.CutPrefix(e.Name(), "CURRENT."); ok && rest != "bak" {
			if n, err := strconv.ParseInt(rest, 10, 64); err == nil {
				pending = append(pending, n)
			}
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i] > pending[j] })
	pendNames := make([]string, len(pending))
	for i, n := range pending {
		pendNames[i] = "CURRENT." + strconv.FormatInt(n, 10)
	}
	pend, pendOK, err := firstCurrent(dir, pendNames)
	if err != nil {
		return "", err
	}
	cur, curOK, err := firstCurrent(dir, []string{"CURRENT", "CURRENT.bak"})
	if err != nil {
		return "", err
	}
	if pendOK && (!curOK || pend > cur) {
		cur, curOK = pend, true
	}
	if !curOK {
		return "", fmt.Errorf("leveldb: %w: no CURRENT file names an existing manifest", errManifest)
	}
	return manifestName(cur), nil
}

// firstCurrent returns the manifest number of the first of names that
// names an existing manifest; ok is false if none does.
func firstCurrent(dir string, names []string) (num int64, ok bool, err error) {
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, false, fmt.Errorf("leveldb: read %s: %w", name, err)
		}
		content, ok := strings.CutSuffix(string(b), "\n")
		if !ok {
			continue // corrupted or incomplete; goleveldb skips it
		}
		n, kind, ok := parseFileName(content)
		if !ok {
			continue
		}
		if kind != "manifest" {
			return 0, false, fmt.Errorf("leveldb: %w: %s does not name a manifest", errManifest, name)
		}
		if _, err := os.Stat(filepath.Join(dir, manifestName(n))); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return 0, false, fmt.Errorf("leveldb: manifest named by %s: %w", name, err)
		}
		return n, true, nil
	}
	return 0, false, nil
}

// manifestName is goleveldb's name for manifest n.
func manifestName(n int64) string { return fmt.Sprintf("MANIFEST-%06d", n) }

// decode applies one manifest record. Fields read before an error stay
// applied, as in goleveldb; the record's table changes are applied only
// when the whole record decodes (goleveldb commits a record's tables to
// its version only then), deletions before additions.
func (m *manifestState) decode(rec []byte) error {
	var added, deleted []tableID
	if err := m.decodeFields(rec, &added, &deleted); err != nil {
		return err
	}
	if m.tables == nil {
		m.tables = map[tableID]struct{}{}
	}
	for _, id := range deleted {
		delete(m.tables, id)
	}
	for _, id := range added {
		m.tables[id] = struct{}{}
	}
	return nil
}

// decodeFields applies one record's scalar fields and collects its table
// changes.
func (m *manifestState) decodeFields(rec []byte, added, deleted *[]tableID) error {
	uvarint := func() (uint64, error) {
		x, n := binary.Uvarint(rec)
		switch {
		case n == 0:
			return 0, errManifestShort
		case n < 0:
			return 0, fmt.Errorf("%w: varint overflows", errManifest)
		}
		rec = rec[n:]
		return x, nil
	}
	varint := func() (int64, error) {
		x, err := uvarint()
		if err == nil && x > math.MaxInt64 {
			err = fmt.Errorf("%w: negative number", errManifest)
		}
		return int64(x), err
	}
	skipBytes := func() error {
		n, err := uvarint()
		if err != nil {
			return err
		}
		if n > uint64(len(rec)) {
			return errManifestShort
		}
		rec = rec[n:]
		return nil
	}
	for len(rec) > 0 {
		tag, err := uvarint()
		if err != nil {
			return err
		}
		switch tag {
		case tagComparator:
			var n uint64
			if n, err = uvarint(); err == nil {
				if n > uint64(len(rec)) {
					err = errManifestShort
				} else {
					m.comparator, m.hasComparator = string(rec[:n]), true
					rec = rec[n:]
				}
			}
		case tagJournalNum, tagPrevJournalNum:
			var x int64
			if x, err = varint(); err == nil {
				if tag == tagJournalNum {
					m.journalNum, m.hasJournalNum = x, true
				} else {
					m.prevJournalNum = x
				}
			}
		case tagNextFileNum:
			var x int64
			if x, err = varint(); err == nil {
				m.nextFileNum, m.hasNextFileNum = x, true
			}
		case tagLastSequence:
			var x uint64
			if x, err = uvarint(); err == nil {
				m.lastSeq, m.hasLastSeq = x, true
			}
		case tagCompactPointer:
			if _, err = uvarint(); err == nil {
				err = skipBytes()
			}
		case tagDeletedFile:
			var id tableID
			if id.level, err = uvarint(); err == nil {
				if id.num, err = varint(); err == nil {
					*deleted = append(*deleted, id)
				}
			}
		case tagNewFile:
			var id tableID
			if id.level, err = uvarint(); err == nil {
				if id.num, err = varint(); err == nil {
					if _, err = varint(); err == nil {
						if err = skipBytes(); err == nil {
							if err = skipBytes(); err == nil {
								*added = append(*added, id)
							}
						}
					}
				}
			}
		default:
			err = fmt.Errorf("%w: unknown field %d", errManifest, tag)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// writeReplayJournal adds a replay journal to dir for st (see the comment
// at the top of this file) and returns its name. It writes a temporary
// file, syncs it, renames it to a journal name no file uses and syncs the
// directory, so a crash leaves either no replay journal or a complete
// one. It never changes or removes an existing file.
func writeReplayJournal(dir string, st *journalState) (string, error) {
	seq := max(st.next, st.manifest.lastSeq+1, st.maxEnd)
	var total uint64
	for _, b := range st.batches {
		total += uint64(b.Len())
	}
	if seq > maxSequence || total > maxSequence-seq {
		return "", errors.New("leveldb: replay journal: sequence numbers exhausted")
	}
	num := max(st.manifest.nextFileNum, st.maxFileNum+1)
	name := fmt.Sprintf("%06d.log", num)
	final := filepath.Join(dir, name)
	switch _, err := os.Lstat(final); {
	case err == nil:
		return "", fmt.Errorf("leveldb: replay journal %s: the file exists", name)
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("leveldb: replay journal: %w", err)
	}
	tmp := filepath.Join(dir, replayTempName)
	if err := writeJournalBatches(tmp, seq, st.batches); err != nil {
		return "", errors.Join(fmt.Errorf("leveldb: write replay journal: %w", err), removeIfExists(tmp))
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", errors.Join(fmt.Errorf("leveldb: rename replay journal: %w", err), removeIfExists(tmp))
	}
	if err := syncDir(dir); err != nil {
		return "", fmt.Errorf("leveldb: sync database directory: %w", err)
	}
	return name, nil
}

// writeJournalBatches writes batches to a new journal file at path,
// numbered from seq on, and syncs it. Empty batches are left out.
func writeJournalBatches(path string, seq uint64, batches []*leveldb.Batch) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := journal.NewWriter(f)
	err = func() error {
		for _, b := range batches {
			if b.Len() == 0 {
				continue
			}
			jw, err := w.Next()
			if err != nil {
				return err
			}
			hdr := binary.LittleEndian.AppendUint64(make([]byte, 0, batchHeaderLen), seq)
			hdr = binary.LittleEndian.AppendUint32(hdr, uint32(b.Len()))
			if _, err := jw.Write(hdr); err != nil {
				return err
			}
			if _, err := jw.Write(b.Dump()); err != nil {
				return err
			}
			seq += uint64(b.Len())
		}
		if err := w.Close(); err != nil {
			return err
		}
		return f.Sync()
	}()
	return errors.Join(err, f.Close())
}

// removeIfExists removes path; a missing file is not an error.
func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// syncDir syncs a directory's entries, as goleveldb does; file systems
// that cannot sync a directory (EINVAL) are accepted.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = f.Sync()
	if errors.Is(err, syscall.EINVAL) {
		err = nil
	}
	return errors.Join(err, f.Close())
}
