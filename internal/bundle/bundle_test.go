package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/config"
)

// Planted values: every one of them must be absent from the bundle.
const (
	home        = "/Users/test"
	plantedUUID = "00000000-0000-4000-8000-000000000abc"
	alias1      = "plantedalias"
	alias2      = "otheralias"
	label       = "Planted Label Person"
	folder2     = "Claude-plantedfolder"
	title       = "Planted Session Title"
	longHex     = "0123456789abcdef0123"
)

// plantedEmail is built at run time so that this file holds no
// email-like string (policy test).
var plantedEmail = strings.Join([]string{"planted.person", "example.com"}, "@")

// mustNotAppear lists the planted text that the bundle must not hold.
func mustNotAppear() []string {
	return []string{
		alias1, alias2, "Planted Label", "plantedfolder", "plantedproject", plantedUUID,
		"PLANTED_VAR", "PlantedDisk", plantedEmail, "planted.person", title, longHex,
		"plantedother", "plantedrepo", home, "Planted Folder", "plantedtool", "Planted Title",
		"plantedpref value",
	}
}

func plantedInput() Input {
	cfg := config.Default()
	cfg.App.DataRoot = "/Volumes/PlantedDisk/Support"
	cfg.Profiles = []config.Profile{
		{Alias: alias1, Label: label},
		{Alias: alias2, Folder: folder2, ManagedByOrg: true},
	}
	cfg.Sync.Exclude = []string{"~/clients/plantedproject/**", "local_" + plantedUUID}
	cfg.Sync.SecretsAllow = []string{"preset/PLANTED_VAR"}
	cfg.Sync.Export = map[string][]string{alias1: {"sessions"}}
	cfg.Sync.Import = map[string][]string{alias2: {"sessions", "plantedpref value"}}
	msg := func(m string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"v": 1, "t": "2026-01-02T03:04:05Z", "phase": "P2", "status": "warn", "message": m})
		return b
	}
	return Input{
		Home:    home,
		Created: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Version: Version{Version: "0.1.0", Commit: "abc1234", Date: "2026-01-01T00:00:00Z", GoVersion: "go1.26.1", OS: "darwin", Arch: "arm64"},
		Checks: []Check{
			{ID: "identity", Status: "ok", Detail: "Claude: account " + plantedUUID + " (" + alias1 + ")"},
			{ID: "identity", Status: "ok", Detail: folder2 + ": account 0000... (" + alias2 + ")"},
			{ID: "probes", Status: "warn", Detail: home + "/Library/Application Support/Claude: 2 of 9 probes failed"},
			{ID: "foreign-tools", Status: "fail", Detail: "another account-switching tool was detected: /Users/plantedother/plantedtool"},
			{ID: "state", Status: "warn", Detail: "contact " + plantedEmail + " about \"" + title + "\""},
			{ID: "marker", Status: "warn", Detail: "device " + longHex},
		},
		HookBinary: home + "/go/bin/paritydesk",
		Config:     cfg, ConfigExists: true,
		Manifests: []Manifest{{Name: "6.x", MinVersion: "6.0.0", MaxVersion: "6.999999.999", Tested: []string{"6.28899.2"}}},
		Overrides: []Override{{Name: "6.x.toml", SHA256: strings.Repeat("ab", 32)}, {Name: "Planted Title.toml", SHA256: "x"}},
		Logs: []Log{
			{ID: "20260102-030405-abcd", Lines: []json.RawMessage{
				msg("switched from " + alias1 + " to " + alias2),
				msg("could not read " + home + "/Planted Folder/proj: permission denied"),
				msg("session \"" + title + "\" is working in /Users/plantedother/plantedrepo"),
				msg("session local_" + plantedUUID + " in " + folder2 + "."),
				json.RawMessage(`{"v":1,"title":"Planted Title","from":"` + alias1 + `","to":"` + alias2 + `","code":0,"run":"20260102-030405-abcd"}`),
				json.RawMessage(`not json`),
			}},
			{ID: "../escape", Lines: []json.RawMessage{json.RawMessage(`{"v":1,"from":"Planted Title","code":999}`)}},
		},
	}
}

// readArchive returns the members of a bundle by name.
func readArchive(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Mode != 0o600 || !strings.HasPrefix(hdr.Name, Dir+"/") || strings.Contains(hdr.Name, "..") {
			t.Fatalf("unexpected member %q type %v mode %o", hdr.Name, hdr.Typeflag, hdr.Mode)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[strings.TrimPrefix(hdr.Name, Dir+"/")] = string(b)
	}
}

func build(t *testing.T, in Input) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := Build(&b, in); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestBundleRedactsPlantedValues(t *testing.T) {
	files := readArchive(t, build(t, plantedInput()))
	want := []string{"README.txt", "version.json", "doctor.json", "config.json", "manifests.json",
		"logs/01-20260102-030405-abcd.jsonl", "logs/02-run.jsonl"}
	if len(files) != len(want) {
		t.Fatalf("members %v, want %v", keys(files), want)
	}
	for _, name := range want {
		if _, ok := files[name]; !ok {
			t.Fatalf("missing %s in %v", name, keys(files))
		}
	}
	for name, content := range files {
		for _, bad := range mustNotAppear() {
			if strings.Contains(content, bad) {
				t.Errorf("%s holds the planted value %q:\n%s", name, bad, content)
			}
		}
		if strings.Contains(content, "/Users/") {
			t.Errorf("%s holds a home path:\n%s", name, content)
		}
	}
	// What stays useful.
	for name, sub := range map[string][]string{
		"doctor.json":                        {`"identity"`, "Claude: account 0000", "(profile-1)", "Claude-profile-2", "2 of 9 probes failed", "<email>", `\"<text>\"`, "<id>", "~/"},
		"config.json":                        {`"alias": "profile-1"`, `"label": "<label>"`, `"folder": "Claude-profile-2"`, "<exclude-1>", "<exclude-2>", "<secret-name-1>", `"profile-2": [`, `"bundle_id": "com.anthropic.claudefordesktop"`},
		"version.json":                       {`"version": "0.1.0"`, `"commit": "abc1234"`, `"arch": "arm64"`},
		"manifests.json":                     {`"6.x"`, `"6.28899.2"`, "<file>.toml", strings.Repeat("ab", 32)},
		"logs/01-20260102-030405-abcd.jsonl": {"switched from profile-1 to profile-2", `"from":"profile-1"`, `"code":0`, "<path:", "local_0000", "Claude-profile-2."},
		"logs/02-run.jsonl":                  {`"from":"profile-?"`},
	} {
		for _, s := range sub {
			if !strings.Contains(files[name], s) {
				t.Errorf("%s lacks %q:\n%s", name, s, files[name])
			}
		}
	}
	if strings.Contains(files["logs/02-run.jsonl"], `"code"`) {
		t.Errorf("an out-of-range code was kept: %s", files["logs/02-run.jsonl"])
	}
}

func TestBundleWithoutConfigOrLogs(t *testing.T) {
	in := Input{Home: home, Created: time.Unix(0, 0), ConfigProblem: true, ConfigExists: true, ManifestProblem: true}
	files := readArchive(t, build(t, in))
	if !strings.Contains(files["config.json"], "could not be read") || strings.Contains(files["config.json"], `"config"`) {
		t.Fatalf("config.json: %s", files["config.json"])
	}
	if !strings.Contains(files["README.txt"], "no run logs") || !strings.Contains(files["manifests.json"], "could not be loaded") {
		t.Fatalf("README.txt: %s\nmanifests.json: %s", files["README.txt"], files["manifests.json"])
	}
}

func TestBundleIsDeterministic(t *testing.T) {
	a, b := build(t, plantedInput()), build(t, plantedInput())
	if !bytes.Equal(a, b) {
		t.Fatal("the same input gave different bytes")
	}
}

func TestScrubText(t *testing.T) {
	s := newScrubber(home, &config.Config{Profiles: []config.Profile{{Alias: "work"}}})
	tests := []struct{ in, want string }{
		{"mode full", "mode full"},
		{"network work", "network profile-1"},
		{"to work.", "to profile-1."},
		{"/opt/homebrew/bin/paritydesk", "/opt/homebrew/bin/paritydesk"},
		{"/opt/homebrew/../../Users/x", "<path:"},
		{"Claude-3p: no account", "Claude-<folder>: no account"},
		{"\x1b[2Jclear", "clear"},
	}
	for _, tc := range tests {
		if got := s.text(tc.in); !strings.HasPrefix(got, tc.want) {
			t.Errorf("text(%q) = %q, want prefix %q", tc.in, got, tc.want)
		}
	}
}

func TestWriteModeAndRefusals(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "support.tar.gz")
	if err := Write(p, plantedInput()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("mode %v", fi.Mode())
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	readArchive(t, data)

	// Never over an existing file.
	if err := Write(p, Input{}); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite: %v", err)
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(after, data) {
		t.Fatal("the existing file changed")
	}

	// Never through a symbolic link, dangling or not.
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link.tar.gz")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, Input{}); err == nil {
		t.Fatal("wrote through a dangling symbolic link")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the link target was created: %v", err)
	}

	// A missing directory fails and leaves nothing behind.
	if err := Write(filepath.Join(dir, "missing", "x.tar.gz"), Input{}); err == nil {
		t.Fatal("wrote into a missing directory")
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBundleDropsUnclassifiedNames checks the doctor "unclassified" check
// as the orchestrator writes it: an entry in the Claude folder may have a
// personal name, so the bundle keeps only the count. A user can still
// show the names with paritydesk doctor.
func TestBundleDropsUnclassifiedNames(t *testing.T) {
	in := plantedInput()
	in.Checks = []Check{{ID: "unclassified", Status: "warn",
		Detail: "3 unclassified entries in Claude (never touched; add them to the compat manifest): cache-" + plantedUUID + ", Acme Merger Notes.txt, unknown.bin"}}
	doc := readArchive(t, build(t, in))["doctor.json"]
	for _, want := range []string{`"unclassified"`, "3 unclassified entries in Claude"} {
		if !strings.Contains(doc, want) {
			t.Errorf("doctor.json lacks %q:\n%s", want, doc)
		}
	}
	for _, bad := range []string{plantedUUID, "Acme", "Merger", "unknown.bin"} {
		if strings.Contains(doc, bad) {
			t.Errorf("doctor.json holds the entry name part %q:\n%s", bad, doc)
		}
	}
}

// TestBundleReplacesRemovedAlias: a run log message names a profile that
// is no longer in the config. The run log records the alias, so the
// bundle replaces it in the message as well as in from and to.
func TestBundleReplacesRemovedAlias(t *testing.T) {
	in := plantedInput()
	in.Logs = []Log{{ID: "20260101-000000-abcd", Lines: []json.RawMessage{
		json.RawMessage(`{"v":1,"phase":"P2","status":"warn","message":"sessions: the base of acmeclient is unreadable","aliases":["acmeclient"]}`),
		json.RawMessage(`{"v":1,"phase":"P10","status":"ok","message":"removed formerco; its folder was left as it is"}`),
		json.RawMessage(`{"v":1,"run":"20260101-000000-abcd","code":0,"from":"formerco","to":"acmeclient"}`),
	}}}
	files := readArchive(t, build(t, in))
	for name, doc := range files {
		for _, bad := range []string{"acmeclient", "formerco"} {
			if strings.Contains(doc, bad) {
				t.Errorf("%s holds the removed alias %q:\n%s", name, bad, doc)
			}
		}
	}
}
