package app

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const fakeSession = "local_00000000-0000-4000-8000-000000000001"

func TestArgvAccepts(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		t    Template
		args []string
		want []string
	}{
		{TmplOpenBundle, []string{"com.example.app"}, []string{binOpen, "-b", "com.example.app"}},
		{TmplOpenURL, []string{sessionURLPrefix + fakeSession}, []string{binOpen, sessionURLPrefix + fakeSession}},
		{TmplQuit, []string{"com.example.app"}, []string{binOsascript, "-e", scriptQuit, "com.example.app"}},
		{TmplNotify, []string{"paritydesk", "Switched."}, []string{binOsascript, "-e", scriptNotify, "paritydesk", "Switched."}},
		{TmplLsof, nil, []string{binLsof, "-nP", "-w", "-F", "pfan"}},
		{TmplBootstrap, []string{"501", "/tmp/run/x.plist"}, []string{binLaunchctl, "bootstrap", "gui/501", "/tmp/run/x.plist"}},
		{TmplBootout, []string{"501", JobLabelPrefix + "abcdef12"}, []string{binLaunchctl, "bootout", "gui/501/" + JobLabelPrefix + "abcdef12"}},
		{TmplPlutil, []string{"/tmp/x/Contents/Info.plist"}, []string{binPlutil, "-convert", "xml1", "-o", "-", "/tmp/x/Contents/Info.plist"}},
	}
	for _, tc := range tests {
		got, err := Argv(tc.t, tc.args...)
		if err != nil {
			t.Fatalf("%s: %v", tc.t, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Fatalf("%s: got %q want %q", tc.t, got, tc.want)
		}
	}
	got, err := Argv(TmplGitPorcelain, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"core.fsmonitor=", "--no-optional-locks", "core.hooksPath=/dev/null",
		"--attr-source=" + emptyTree, "core.attributesFile=/dev/null"} {
		if !slices.Contains(got, want) {
			t.Fatalf("git argv %q lacks %q", got, want)
		}
	}
	if i := slices.Index(got, "-C"); i < 0 || got[len(got)-1] != "--porcelain" ||
		slices.Index(got, "--attr-source="+emptyTree) > i {
		t.Fatalf("git argv %q", got)
	}
}

func TestArgvRefusesHostileArguments(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		t    Template
		args []string
	}{
		{"bundle option", TmplOpenBundle, []string{"-a"}},
		{"bundle space", TmplQuit, []string{"com.example app"}},
		{"bundle empty", TmplProbe, []string{""}},
		{"url other scheme", TmplOpenURL, []string{"file:///etc/passwd"}},
		{"url bad id", TmplOpenURL, []string{sessionURLPrefix + "local_../../x"}},
		{"url option", TmplOpenURL, []string{"-a"}},
		{"url suffix", TmplOpenURL, []string{sessionURLPrefix + fakeSession + "?x=1"}},
		{"uid letters", TmplBootout, []string{"5o1", JobLabelPrefix + "abcdef12"}},
		{"label other", TmplBootout, []string{"501", "com.apple.x"}},
		{"label short id", TmplBootout, []string{"501", JobLabelPrefix + "ab"}},
		{"plist relative", TmplBootstrap, []string{"501", "run/x.plist"}},
		{"plist unclean", TmplBootstrap, []string{"501", "/tmp/../x.plist"}},
		{"plist suffix", TmplBootstrap, []string{"501", "/tmp/x.sh"}},
		{"git relative", TmplGitPorcelain, []string{"repo"}},
		{"git missing", TmplGitPorcelain, []string{filepath.Join(dir, "missing")}},
		{"git option", TmplGitPorcelain, []string{"--exec-path=/tmp"}},
		{"notify option", TmplNotify, []string{"-x", "text"}},
		{"notify long", TmplNotify, []string{"t", strings.Repeat("a", maxNotifyText+1)}},
		{"notify control", TmplNotify, []string{"t", "a\x1b[31m"}},
		{"arg count", TmplLsof, []string{"x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Argv(tc.t, tc.args...); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := Argv(TmplOpenBundle, "-a"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("not ErrInvalidArgument: %v", err)
	}
}
