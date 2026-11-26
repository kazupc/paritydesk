package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"syscall"
	"time"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
)

// Dir is the top-level directory inside the archive.
const Dir = "paritydesk-support"

// MaxLogs bounds how many run logs a bundle holds.
const MaxLogs = 50

// maxLogLines bounds the lines kept from one run log.
const maxLogLines = 2000

// ErrExists is returned by Write when the target already exists.
var ErrExists = errors.New("bundle: the file already exists; choose a new name")

// Check is one doctor check, as doctor prints it.
type Check struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Log is one run log: its run id and its JSON lines, newest log first in
// Input.Logs.
type Log struct {
	ID    string
	Lines []json.RawMessage
}

// Manifest describes one compatibility manifest in use.
type Manifest struct {
	Name       string   `json:"name"`
	MinVersion string   `json:"min_version"`
	MaxVersion string   `json:"max_version"`
	Tested     []string `json:"tested"`
	KnownBad   []string `json:"known_bad"`
}

// Override is one file in the compat.d override directory.
type Override struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Version describes the binary.
type Version struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Input is everything a bundle is built from. The caller reads it; Build
// redacts it.
type Input struct {
	// Home is the home directory, for redaction.
	Home    string
	Created time.Time
	Version Version
	// Checks and HookBinary are doctor's output.
	Checks     []Check
	HookBinary string
	// Config is paritydesk's config (nil when there is none or it could
	// not be read); ConfigExists and ConfigProblem say which.
	Config        *config.Config
	ConfigExists  bool
	ConfigProblem bool
	Manifests     []Manifest
	// ManifestProblem reports that the manifests could not be loaded.
	ManifestProblem bool
	Overrides       []Override
	Logs            []Log
}

// file is one archive member.
type file struct {
	name string
	data []byte
}

// Write creates path (mode 0600, never through a symbolic link, never
// over an existing file) and writes the bundle to it. A partly written
// file is removed.
func Write(path string, in Input) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("bundle: %w", cerr)
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	if err := Build(f, in); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	return nil
}

// Build writes the bundle as a gzip-compressed tar stream to w. The same
// input gives the same bytes.
func Build(w io.Writer, in Input) error {
	files, err := members(in)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	mtime := in.Created.UTC().Truncate(time.Second)
	for _, f := range files {
		hdr := &tar.Header{
			Typeflag: tar.TypeReg, Name: Dir + "/" + f.name, Mode: 0o600, Size: int64(len(f.data)),
			ModTime: mtime, Format: tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("bundle: %w", err)
		}
		if _, err := tw.Write(f.data); err != nil {
			return fmt.Errorf("bundle: %w", err)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	return nil
}

// members returns the redacted archive members in order.
func members(in Input) ([]file, error) {
	s := newScrubber(in.Home, in.Config)
	var files []file
	add := func(name string, v any) error {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("bundle: %s: %w", name, err)
		}
		files = append(files, file{name, b.Bytes()})
		return nil
	}
	logs := in.Logs
	if len(logs) > MaxLogs {
		logs = logs[:MaxLogs]
	}
	// Run logs outlive profiles: learn every alias they name first, so a
	// removed or renamed profile's alias is replaced in every message.
	for _, l := range logs {
		s.learnAliases(l.Lines)
	}
	var logFiles []file
	for i, l := range logs {
		id := l.ID
		if !runIDRE.MatchString(id) {
			id = "run"
		}
		logFiles = append(logFiles, file{fmt.Sprintf("logs/%02d-%s.jsonl", i+1, id), s.logLines(l.Lines)})
	}
	files = append(files, file{"README.txt", readme(logFiles)})
	if err := add("version.json", s.version(in.Version)); err != nil {
		return nil, err
	}
	if err := add("doctor.json", s.doctor(in.Checks, in.HookBinary)); err != nil {
		return nil, err
	}
	if err := add("config.json", s.configDoc(in)); err != nil {
		return nil, err
	}
	if err := add("manifests.json", s.manifests(in)); err != nil {
		return nil, err
	}
	return append(files, logFiles...), nil
}

// readme is the fixed text of README.txt plus the list of members.
func readme(logs []file) []byte {
	var b bytes.Buffer
	b.WriteString(`paritydesk support bundle

This archive was written by "paritydesk doctor --bundle". It holds only
the files below. Only doctor.json reflects Claude's data folders and
main.log: the doctor checks report the app version and mode, probe
results, folder identities masked to four characters, marker state, the
number of unclassified entries in the active folder (not their names),
and whether main.log showed a Claude in Chrome connection. No file
content from a data folder or main.log is included. Before it was
written, every text value was redacted:
paths under the home directory became ~/<hash>, other paths <path:hash>
(system locations are kept), UUIDs were masked to four characters, long
hex runs became <id>, quoted text became "<text>", email-like strings
became <email>, profile aliases became profile-N and parked folder names
Claude-profile-N. Profile labels, sync.exclude entries and secret names
were replaced by placeholders.

Read every file before you attach the archive to an issue.

version.json     paritydesk version, commit, build date, Go version, OS, CPU
doctor.json      the checks of paritydesk doctor
config.json      paritydesk's own config, with personal values replaced
manifests.json   compatibility manifests in use and override file hashes
`)
	if len(logs) == 0 {
		b.WriteString("logs/            no run logs\n")
	}
	for _, l := range logs {
		fmt.Fprintf(&b, "%-16s a run log, field by field, messages redacted\n", l.name)
	}
	return b.Bytes()
}

func (s *scrubber) version(v Version) Version {
	return Version{
		Version: s.text(v.Version), Commit: s.text(v.Commit), Date: s.text(v.Date),
		GoVersion: enum(v.GoVersion), OS: enum(v.OS), Arch: enum(v.Arch),
	}
}

type doctorDoc struct {
	Checks     []Check `json:"checks"`
	HookBinary string  `json:"hook_binary,omitempty"`
}

func (s *scrubber) doctor(checks []Check, hookBinary string) doctorDoc {
	d := doctorDoc{Checks: []Check{}, HookBinary: s.text(hookBinary)}
	for _, c := range checks {
		detail := s.text(c.Detail)
		if c.ID == unclassifiedCheck && c.Status != "ok" {
			detail = unclassifiedCount(c.Detail)
		}
		d.Checks = append(d.Checks, Check{ID: enum(c.ID), Status: enum(c.Status), Detail: detail})
	}
	return d
}

// unclassifiedCheck is the doctor check that lists the names of entries
// in the active Claude folder. Those names may be personal, so the bundle
// keeps only their count.
const unclassifiedCheck = "unclassified"

// unclassifiedCount replaces the entry names of the unclassified check by
// their count (the detail starts with it).
func unclassifiedCount(detail string) string {
	n := 0
	for _, r := range detail {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
		if n > 1<<20 {
			break
		}
	}
	return fmt.Sprintf("%d unclassified entries in Claude (names left out of the bundle)", n)
}

// The support archive is read by maintainers and stays English
// (spec-i18n-ja 4).
const (
	problemConfig    = "the config file could not be read or parsed; its content is not included"
	problemManifests = "the compatibility manifests could not be loaded"
)

type configDoc struct {
	Exists  bool           `json:"exists"`
	Problem string         `json:"problem,omitempty"`
	Config  *config.Config `json:"config,omitempty"`
}

func (s *scrubber) configDoc(in Input) configDoc {
	d := configDoc{Exists: in.ConfigExists}
	switch {
	case in.ConfigProblem:
		d.Problem = problemConfig
	case in.Config != nil:
		d.Config = s.config(in.Config)
	}
	return d
}

type manifestsDoc struct {
	Problem   string     `json:"problem,omitempty"`
	Manifests []Manifest `json:"manifests"`
	Overrides []Override `json:"overrides"`
}

var (
	overrideNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}\.toml$`)
	sha256RE       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	versionRE      = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,64}$`)
)

func (s *scrubber) manifests(in Input) manifestsDoc {
	d := manifestsDoc{Manifests: []Manifest{}, Overrides: []Override{}}
	if in.ManifestProblem {
		d.Problem = problemManifests
	}
	for _, m := range in.Manifests {
		d.Manifests = append(d.Manifests, Manifest{
			Name: s.text(m.Name), MinVersion: versionOnly(m.MinVersion), MaxVersion: versionOnly(m.MaxVersion),
			Tested: versions(m.Tested), KnownBad: versions(m.KnownBad),
		})
	}
	for _, o := range in.Overrides {
		name := o.Name
		if !overrideNameRE.MatchString(name) {
			name = "<file>.toml"
		}
		sum := o.SHA256
		if !sha256RE.MatchString(sum) {
			sum = ""
		}
		d.Overrides = append(d.Overrides, Override{Name: name, SHA256: sum})
	}
	return d
}

func versionOnly(v string) string {
	if versionRE.MatchString(v) {
		return v
	}
	return "<version>"
}

func versions(vs []string) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, versionOnly(v))
	}
	return out
}

// inLine is the part of a run log line that is read. Unknown members are
// dropped.
type inLine struct {
	V         *int     `json:"v"`
	Time      string   `json:"t"`
	Phase     string   `json:"phase"`
	Status    string   `json:"status"`
	Message   string   `json:"message"`
	ElapsedNS *int64   `json:"elapsed_ns"`
	Run       string   `json:"run"`
	Code      *int     `json:"code"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	Mode      string   `json:"mode"`
	Aliases   []string `json:"aliases"`
}

// outLine is one redacted run log line.
type outLine struct {
	V         int    `json:"v"`
	Time      string `json:"t,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Status    string `json:"status,omitempty"`
	Message   string `json:"message,omitempty"`
	ElapsedNS int64  `json:"elapsed_ns,omitempty"`
	Run       string `json:"run,omitempty"`
	Code      *int   `json:"code,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Mode      string `json:"mode,omitempty"`
}

// logLines redacts a run log line by line. Lines that are not JSON
// objects are dropped.
func (s *scrubber) logLines(lines []json.RawMessage) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if len(lines) > maxLogLines {
		lines = lines[len(lines)-maxLogLines:]
	}
	for _, raw := range lines {
		var l inLine
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		o := outLine{V: 1, Phase: enum(l.Phase), Status: enum(l.Status), Message: s.text(l.Message),
			From: s.logAlias(l.From), To: s.logAlias(l.To), Mode: enum(l.Mode)}
		if l.V != nil {
			o.V = *l.V
		}
		if t, err := time.Parse(time.RFC3339Nano, l.Time); err == nil {
			o.Time = t.UTC().Format(time.RFC3339Nano)
		}
		if l.ElapsedNS != nil && *l.ElapsedNS > 0 {
			o.ElapsedNS = *l.ElapsedNS
		}
		if runIDRE.MatchString(l.Run) {
			o.Run = l.Run
		}
		if l.Code != nil && *l.Code >= 0 && *l.Code < 256 {
			c := *l.Code
			o.Code = &c
		}
		_ = enc.Encode(o)
	}
	return b.Bytes()
}

// learnAliases records the aliases a run log names (from, to and the
// aliases of a message) that the config does not, so text() replaces
// them with profile-?.
func (s *scrubber) learnAliases(lines []json.RawMessage) {
	for _, raw := range lines {
		var l inLine
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		for _, a := range append([]string{l.From, l.To}, l.Aliases...) {
			if _, err := ids.ParseAlias(a); err != nil {
				continue
			}
			if _, known := s.aliases[a]; !known {
				s.unknown[a] = true
			}
		}
	}
}

// logAlias returns the pseudonym of an alias read from a run log;
// anything that is not shaped like an alias maps to profile-?.
func (s *scrubber) logAlias(a string) string {
	if a == "" {
		return ""
	}
	if _, err := ids.ParseAlias(a); err != nil {
		return unknownAlias
	}
	return s.alias(a)
}
