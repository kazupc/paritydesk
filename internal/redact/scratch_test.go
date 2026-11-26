package redact

import (
	"strings"
	"testing"
)

func TestScratchPathsMasked(t *testing.T) {
	const (
		acct = "00000000-0000-4000-8000-00000000000a"
		org  = "00000000-0000-4000-8000-00000000000b"
		leaf = "scratch-2026-01-02-0a0b0c"
	)
	canary := "CANARY-client-plan.md"
	for _, in := range []string{
		acct + "/" + org + "/" + leaf + "/docs/" + canary,
		`opening "/Volumes/x/Claude/scratch-workspaces/` + acct + "/" + org + "/" + leaf + "/docs/" + canary + `": denied`,
		"key " + acct + "/" + org + "/" + leaf + "/docs/" + canary + " skipped",
	} {
		got := Redactor{}.String(in)
		if strings.Contains(got, canary) || strings.Contains(got, acct) || strings.Contains(got, org) {
			t.Errorf("not masked: %q", got)
		}
		if !strings.Contains(got, leaf+"/") {
			t.Errorf("leaf lost: %q", got)
		}
	}
	if got := (Redactor{}).String(acct + "/" + org + "/" + leaf); got != "0000…/0000…/"+leaf {
		t.Errorf("workspace key: %q", got)
	}
	// A file name with spaces is hashed whole, up to the error separator.
	for _, name := range []string{"Acme Merger Plan.md", "notes (conflict from work 2026-01-02).md"} {
		in := "fsx: clone scratch-workspaces/" + acct + "/" + org + "/" + leaf + "/docs/" + name + ": permission denied"
		got := Redactor{}.String(in)
		for _, part := range []string{"Merger", "Plan.md", "conflict from", "work 2026"} {
			if strings.Contains(got, part) {
				t.Errorf("%q leaks %q", got, part)
			}
		}
		if !strings.HasSuffix(got, ": permission denied") || !strings.Contains(got, leaf+"/") {
			t.Errorf("message lost: %q", got)
		}
	}
	// The same rest hashes the same, so logs stay comparable.
	a := Redactor{}.String(acct + "/" + org + "/" + leaf + "/x")
	b := Redactor{}.String(acct + "/" + org + "/" + leaf + "/x")
	if a != b {
		t.Error("masking is not stable")
	}
}
