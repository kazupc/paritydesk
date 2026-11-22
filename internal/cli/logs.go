package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/redact"
	"github.com/kazupc/paritydesk/internal/render"
)

// Run logs are ~/Library/Logs/paritydesk/<run id>.jsonl: one JSON object
// per line, each with "v":1. They are a debug mirror only; root.json is
// the source of truth (M1).
const (
	logExt = ".jsonl"
	// maxLogBytes bounds how much of one log is read.
	maxLogBytes = 16 << 20
)

var runIDRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)

// logFile is one run log file.
type logFile struct {
	ID       string
	Path     string
	Modified time.Time
}

// listRunLogs returns the run logs in dir, newest first. A missing
// directory has none.
func listRunLogs(dir string) ([]logFile, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []logFile
	for _, en := range entries {
		id, ok := strings.CutSuffix(en.Name(), logExt)
		if !ok || !runIDRE.MatchString(id) || !en.Type().IsRegular() {
			continue
		}
		info, err := en.Info()
		if err != nil {
			continue
		}
		out = append(out, logFile{ID: id, Path: filepath.Join(dir, en.Name()), Modified: info.ModTime()})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Modified.Equal(out[j].Modified) {
			return out[i].Modified.After(out[j].Modified)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// readLogLines returns the lines of a log that are valid JSON.
func readLogLines(path string) ([]json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, maxLogBytes))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var out []json.RawMessage
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) > 0 && json.Valid(line) {
			out = append(out, append(json.RawMessage(nil), line...))
		}
	}
	return out, sc.Err()
}

// logView is the --json document of log.
type logView struct {
	Run    string            `json:"run,omitempty"`
	Events []json.RawMessage `json:"events"`
}

func runLog(_ context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	paths, _, err := e.paths()
	if err != nil {
		return err
	}
	want := getString(fs, "run")
	if want != "" && !runIDRE.MatchString(want) {
		return usageErrorf("--run: not a run id")
	}
	logs, err := listRunLogs(paths.LogDir)
	if err != nil {
		return err
	}
	var chosen *logFile
	for i := range logs {
		if want == "" || logs[i].ID == want {
			chosen = &logs[i]
			break
		}
	}
	if chosen == nil && want != "" {
		return pderr.New(pderr.Usage, "log", "no log for that run")
	}
	v := logView{Events: []json.RawMessage{}}
	if chosen != nil {
		v.Run = chosen.ID
		if v.Events, err = readLogLines(chosen.Path); err != nil {
			return err
		}
		if v.Events == nil {
			v.Events = []json.RawMessage{}
		}
	}
	return e.emit(v, func(p *render.Printer) {
		if chosen == nil {
			p.Line("%s", i18n.T(i18n.LogNone))
			return
		}
		for _, ev := range v.Events {
			p.Line("%s", string(ev))
		}
	}, nil)
}

// historyEntry is one run in history.
type historyEntry struct {
	Run      string      `json:"run"`
	Modified time.Time   `json:"modified"`
	Lines    int         `json:"lines"`
	Code     *pderr.Code `json:"code,omitempty"`
	From     ids.Alias   `json:"from,omitempty"`
	To       ids.Alias   `json:"to,omitempty"`
}

// historyView is the --json document of history.
type historyView struct {
	Runs []historyEntry `json:"runs"`
}

func runHistory(_ context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	paths, _, err := e.paths()
	if err != nil {
		return err
	}
	logs, err := listRunLogs(paths.LogDir)
	if err != nil {
		return err
	}
	v := historyView{Runs: []historyEntry{}}
	for _, l := range logs {
		lines, err := readLogLines(l.Path)
		if err != nil {
			continue
		}
		v.Runs = append(v.Runs, summarizeRun(l, lines))
	}
	return e.emit(v, func(p *render.Printer) {
		if len(v.Runs) == 0 {
			p.Line("%s", i18n.T(i18n.HistoryNone))
			return
		}
		rows := make([][]string, len(v.Runs))
		for i, r := range v.Runs {
			code := ""
			if r.Code != nil {
				code = pderr.ID(*r.Code)
			}
			rows[i] = []string{r.Run, r.Modified.UTC().Format(time.RFC3339), string(r.From), string(r.To), strconv.Itoa(r.Lines), code}
		}
		p.Table([]string{i18n.T(i18n.ColRun), i18n.T(i18n.ColModified), i18n.T(i18n.ColFrom), i18n.T(i18n.ColTo),
			i18n.T(i18n.ColLines), i18n.T(i18n.ColCode)}, rows)
	}, nil)
}

// summarizeRun takes the exit code and the aliases from the last log line
// that has them. Aliases are validated; anything else is ignored.
func summarizeRun(l logFile, lines []json.RawMessage) historyEntry {
	h := historyEntry{Run: l.ID, Modified: l.Modified.UTC(), Lines: len(lines)}
	for i := len(lines) - 1; i >= 0; i-- {
		var f struct {
			Code *int   `json:"code"`
			From string `json:"from"`
			To   string `json:"to"`
		}
		if json.Unmarshal(lines[i], &f) != nil {
			continue
		}
		if h.Code == nil && f.Code != nil && *f.Code >= 0 && *f.Code < 256 {
			c := pderr.Code(*f.Code)
			h.Code = &c
		}
		if h.From == "" {
			if a, err := ids.ParseAlias(f.From); err == nil {
				h.From = a
			}
		}
		if h.To == "" {
			if a, err := ids.ParseAlias(f.To); err == nil {
				h.To = a
			}
		}
	}
	return h
}

// loggedEvent is one progress event and when it was seen.
type loggedEvent struct {
	At    time.Time
	Event orchestrator.Event
}

// logLine is one line of a run log. Event lines carry Phase, Status and
// Message; the last line carries Run, Code, From and To, which history
// reads.
type logLine struct {
	V         int                `json:"v"`
	Time      time.Time          `json:"t"`
	Phase     orchestrator.Phase `json:"phase,omitempty"`
	Status    string             `json:"status,omitempty"`
	Message   string             `json:"message,omitempty"`
	ElapsedNS int64              `json:"elapsed_ns,omitempty"`
	Run       string             `json:"run,omitempty"`
	Code      *pderr.Code        `json:"code,omitempty"`
	From      ids.Alias          `json:"from,omitempty"`
	To        ids.Alias          `json:"to,omitempty"`
	Mode      string             `json:"mode,omitempty"`
}

// writeRunLog writes <log dir>/<run id>.jsonl (0600, directory 0700) from
// a run's progress events and its result. Messages are redacted (home
// paths, other users' homes, UUIDs). The log is a debug mirror only, so a
// failure to write it is ignored.
func (e *env) writeRunLog(res *orchestrator.Result, events []loggedEvent) {
	if res == nil || !runIDRE.MatchString(res.RunID) || e.readOnly() {
		return
	}
	paths, home, err := e.paths()
	if err != nil {
		return
	}
	_ = writeLogFile(filepath.Join(paths.LogDir, res.RunID+logExt), runLogLines(res, events, redact.Redactor{Home: home}, e.sys.now()))
}

// runLogLines renders the log lines of one run.
func runLogLines(res *orchestrator.Result, events []loggedEvent, r redact.Redactor, now time.Time) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for _, ev := range events {
		_ = enc.Encode(logLine{V: 1, Time: ev.At.UTC(), Phase: ev.Event.Phase, Status: ev.Event.Status,
			Message: r.String(ev.Event.Message), ElapsedNS: int64(ev.Event.Elapsed)})
	}
	code := res.Code
	_ = enc.Encode(logLine{V: 1, Time: now.UTC(), Run: res.RunID, Code: &code, From: res.From, To: res.To, Mode: string(res.Mode)})
	return b.Bytes()
}

// writeLogFile creates the log directory (0700) and writes data to path
// (0600) without following a symlink at path.
func writeLogFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("run log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("run log: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("run log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("run log: %w", err)
	}
	return nil
}
