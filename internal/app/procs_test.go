package app

import (
	"encoding/binary"
	"slices"
	"testing"
)

// procArgs builds a KERN_PROCARGS2 buffer.
func procArgs(argc int32, exe string, pad int, rest ...string) []byte {
	buf := binary.NativeEndian.AppendUint32(nil, uint32(argc))
	buf = append(buf, exe...)
	buf = append(buf, make([]byte, 1+pad)...)
	for _, s := range rest {
		buf = append(buf, s...)
		buf = append(buf, 0)
	}
	return buf
}

func TestParseProcArgs(t *testing.T) {
	exe := "/Apps/Example.app/Contents/MacOS/Example"
	tests := []struct {
		name    string
		buf     []byte
		exe     string
		argv    []string
		wantErr bool
	}{
		{"plain", procArgs(2, exe, 3, exe, "--flag", "PATH=/bin", "HOME=/x"), exe, []string{exe, "--flag"}, false},
		{"no padding", procArgs(1, exe, 0, "Example", "ENV=1"), exe, []string{"Example"}, false},
		{"empty args kept", procArgs(4, exe, 2, "a", "", "", "b", "ENV=1"), exe, []string{"a", "", "", "b"}, false},
		{"argc zero", procArgs(0, exe, 1, "ENV=1"), exe, nil, false},
		{"truncated args", procArgs(5, exe, 1, "a", "b"), exe, []string{"a", "b"}, false},
		{"unterminated last arg", append(procArgs(3, exe, 1, "a"), "partial"...), exe, []string{"a"}, false},
		{"too short", []byte{1, 0}, "", nil, true},
		{"no exe terminator", append(binary.NativeEndian.AppendUint32(nil, 1), "/bin/x"...), "", nil, true},
		{"empty exe", procArgs(1, "", 0, "a"), "", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotExe, argv, err := parseProcArgs(tc.buf)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if gotExe != tc.exe || !slices.Equal(argv, tc.argv) {
				t.Fatalf("got %q %q, want %q %q", gotExe, argv, tc.exe, tc.argv)
			}
		})
	}
}

func TestIsAppProcess(t *testing.T) {
	const bundle = "/Applications/Example.app"
	tests := []struct {
		name string
		exe  string
		args []string
		want bool
	}{
		{"main exe", bundle + "/Contents/MacOS/Example", nil, true},
		{"renderer helper", bundle + "/Contents/Frameworks/Example Helper (Renderer).app/Contents/MacOS/Example Helper (Renderer)", []string{"--type=renderer"}, true},
		{"gpu helper", bundle + "/Contents/Frameworks/Example Helper (GPU).app/Contents/MacOS/Example Helper (GPU)", nil, true},
		{"vm service", bundle + "/Contents/Resources/vm/service", nil, true},
		{"native host", bundle + "/Contents/Helpers/chrome-native-host", []string{"chrome-extension://x/"}, false},
		{"updater", bundle + "/Contents/Frameworks/Squirrel.framework/Resources/ShipIt", nil, false},
		{"crash handler", bundle + "/Contents/Frameworks/Electron Framework.framework/Helpers/chrome_crashpad_handler", nil, false},
		{"other app", "/Applications/Other.app/Contents/MacOS/Other", nil, false},
		{"prefix lookalike", "/Applications/Example.app2/Contents/MacOS/Example", nil, false},
		{"bundle root file", bundle + "/Example", nil, false},
		{"translocated", "/private/var/folders/ab/cd/T/AppTranslocation/0000-AAAA/d/Example.app/Contents/MacOS/Example", nil, true},
		{"translocated other", "/private/var/folders/ab/cd/T/AppTranslocation/0000-AAAA/d/Other.app/Contents/MacOS/Other", nil, false},
		{"translocated native host", "/private/var/folders/ab/cd/T/AppTranslocation/0000-AAAA/d/Example.app/Contents/Helpers/chrome-native-host", nil, false},
		{"user data dir equals", bundle + "/Contents/MacOS/Example", []string{"--user-data-dir=/tmp/profile"}, true},
		{"user data dir separate", bundle + "/Contents/MacOS/Example", []string{"--user-data-dir", "/tmp/profile"}, true},
		{"empty exe", "", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAppProcess(Process{PID: 10, Exe: tc.exe, Args: tc.args}, bundle); got != tc.want {
				t.Fatalf("IsAppProcess = %v, want %v", got, tc.want)
			}
		})
	}
	if IsAppProcess(Process{Exe: bundle + "/Contents/MacOS/Example"}, "") {
		t.Fatal("matched an empty bundle path")
	}
}

func TestIsUpdater(t *testing.T) {
	tests := []struct {
		name string
		p    Process
		want bool
	}{
		{"in bundle", Process{Exe: testBundlePath + "/Contents/Frameworks/Squirrel.framework/Resources/ShipIt"}, true},
		{"job label arg", Process{Exe: "/private/tmp/x/ShipIt", Args: []string{testBundle + ".ShipIt"}}, true},
		{"other app's updater", Process{Exe: "/Applications/Other.app/Contents/Frameworks/Squirrel.framework/Resources/ShipIt", Args: []string{"com.example.other.ShipIt"}}, false},
		{"main exe", appProc(1), false},
	}
	for _, tc := range tests {
		if got := isUpdater(tc.p, testBundle, testBundlePath); got != tc.want {
			t.Fatalf("%s: got %v", tc.name, got)
		}
	}
}

func TestUsesDataFolder(t *testing.T) {
	folders := []string{"/data/Claude", "/data/Claude-a"}
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"--user-data-dir=/data/Claude"}, true},
		{[]string{"--user-data-dir", "/data/Claude-a/"}, true},
		{[]string{"--user-data-dir=/data/Claude-ab"}, false},
		{[]string{"--user-data-dir"}, false},
		{[]string{"--other=/data/Claude"}, false},
	}
	for _, tc := range tests {
		if got := usesDataFolder(Process{Args: tc.args}, folders); got != tc.want {
			t.Fatalf("%q: got %v", tc.args, got)
		}
	}
}

const lsofFixture = `p101
fcwd
n/data/Claude
ftxt
n/Apps/Example.app/Contents/MacOS/Example
f12
ar
n/data/Claude/Local Storage/leveldb/000005.ldb
f13
au
n/data/Claude/Local Storage/leveldb/LOCK
p202
f4
aw
n/data/Claude-a/config.json
f5
ar
n/data/Claude-ab/other
f6
a
n/data/Claude/x
p303
fNOFD
n/data/Claude (opendir: Permission denied)
fmem
n/data/Claude/mapped
`

func TestParseLsof(t *testing.T) {
	got := parseLsof([]byte(lsofFixture), []string{"/data/Claude", "/data/Claude-a/"})
	want := []Holder{
		{PID: 101, Path: "/data/Claude", Access: "cwd"},
		{PID: 101, Path: "/data/Claude/Local Storage/leveldb/000005.ldb", Access: "r"},
		{PID: 101, Path: "/data/Claude/Local Storage/leveldb/LOCK", Access: "u"},
		{PID: 202, Path: "/data/Claude-a/config.json", Access: "w"},
		{PID: 202, Path: "/data/Claude/x", Access: "u"},
		{PID: 303, Path: "/data/Claude/mapped", Access: "txt"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%v\nwant\n%v", got, want)
	}
	writes := 0
	for _, h := range got {
		if h.Writes() {
			writes++
		}
	}
	if writes != 5 {
		t.Fatalf("writes = %d", writes)
	}
}

func TestHoldersNamesCommands(t *testing.T) {
	run := &fakeRunner{t: t, reply: func(Template, []string) ([]byte, error) {
		// lsof exits 1 with partial output; the output still counts.
		return []byte(lsofFixture), &CommandError{Template: TmplLsof, ExitCode: 1}
	}}
	procs := procFunc(func() []Process { return []Process{{PID: 202, Exe: "/usr/bin/editor"}} })
	hs, err := lsofHolders{run: run, procs: procs}.Holders(t.Context(), []string{"/data/Claude-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 1 || hs[0].Command != "editor" || hs[0].Access != "w" {
		t.Fatalf("got %+v", hs)
	}
	empty := &fakeRunner{t: t, reply: func(Template, []string) ([]byte, error) {
		return nil, &CommandError{Template: TmplLsof, ExitCode: 1}
	}}
	if _, err := (lsofHolders{run: empty}).Holders(t.Context(), []string{"/x"}); err == nil {
		t.Fatal("no output and a failure gave no error")
	}
}
