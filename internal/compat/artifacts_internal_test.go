package compat

import (
	"strings"
	"testing"
)

// embeddedArtifacts returns the 7.x [artifacts] table with publish_via
// set to files (the transport of R-AR-4).
func embeddedArtifacts(t *testing.T) ArtifactsSpec {
	t.Helper()
	ms, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "7.x" {
			a := m.Artifacts
			a.PublishVia = ArtifactsPublishFiles
			return a
		}
	}
	t.Fatal("no 7.x manifest")
	return ArtifactsSpec{}
}

func cloneArtifacts(a ArtifactsSpec) ArtifactsSpec {
	c := a
	c.WrapperForms = append([]string(nil), a.WrapperForms...)
	c.ReminderPrefixes = append([]string(nil), a.ReminderPrefixes...)
	c.Refusals = map[string][]string{}
	for k, v := range a.Refusals {
		c.Refusals[k] = append([]string(nil), v...)
	}
	c.Fields.Version = append([]string(nil), a.Fields.Version...)
	return c
}

func TestValidateArtifacts(t *testing.T) {
	base := embeddedArtifacts(t)
	if err := ValidateArtifacts(base); err != nil {
		t.Fatalf("embedded table: %v", err)
	}
	tests := []struct {
		name string
		edit func(a *ArtifactsSpec)
		want string
	}{
		{"server", func(a *ArtifactsSpec) { a.Server = "other" }, "artifacts.server"},
		{"publish_via", func(a *ArtifactsSpec) { a.PublishVia = "copy" }, "publish_via"},
		{"read_mode", func(a *ArtifactsSpec) { a.ReadMode = "file" }, "read_mode"},
		{"entry_path absolute", func(a *ArtifactsSpec) { a.EntryPath = "/index.html" }, "entry_path"},
		{"entry_path dotdot", func(a *ArtifactsSpec) { a.EntryPath = "../index.html" }, "entry_path"},
		{"blob suffix", func(a *ArtifactsSpec) { a.BlobSuffix = "html" }, "blob_suffix"},
		{"icon", func(a *ArtifactsSpec) { a.DefaultIcon = "Page" }, "default_icon"},
		{"max_content_bytes", func(a *ArtifactsSpec) { a.MaxContentBytes = 64 << 20 }, "max_content_bytes"},
		{"version_max_bytes", func(a *ArtifactsSpec) { a.VersionMaxBytes = 10 }, "version_max_bytes"},
		{"list_page_limit", func(a *ArtifactsSpec) { a.ListPageLimit = 500 }, "list_page_limit"},
		{"max_rounds", func(a *ArtifactsSpec) { a.MaxRounds = 100 }, "max_rounds"},
		{"batch_max", func(a *ArtifactsSpec) { a.BatchMax = 0 }, "batch_max"},
		{"round steps", func(a *ArtifactsSpec) { a.MaxListPages = 100; a.InspectPerRound = 300 }, "999 steps"},
		{"grace", func(a *ArtifactsSpec) { a.ResultGraceReads = "5s" }, "result_grace_reads"},
		{"pattern unanchored", func(a *ArtifactsSpec) { a.VersionPattern = "[a-z]+" }, "version_pattern"},
		{"pattern bad", func(a *ArtifactsSpec) { a.WrapperTagPattern = "^([$" }, "wrapper_tag_pattern"},
		{"time prefix groups", func(a *ArtifactsSpec) { a.VersionTimePrefix = "^[0-9]+-" }, "exactly one group"},
		{"forms empty", func(a *ArtifactsSpec) { a.WrapperForms = nil }, "wrapper_forms"},
		{"forms unknown", func(a *ArtifactsSpec) { a.WrapperForms = []string{"crlf"} }, "wrapper_forms"},
		{"forms lead and trail", func(a *ArtifactsSpec) { a.WrapperForms = []string{"lead_nl", "trail_nl"} }, "lead_nl and trail_nl"},
		{"forms nl and lead", func(a *ArtifactsSpec) { a.WrapperForms = []string{"nl", "lead_nl"} }, "nl cannot"},
		{"reminder short", func(a *ArtifactsSpec) { a.ReminderPrefixes = []string{"ab"} }, "reminder_prefixes"},
		{"field empty", func(a *ArtifactsSpec) { a.Fields.Version = nil }, "fields.version"},
		{"field bad", func(a *ArtifactsSpec) { a.Fields.Version = []string{"Version"} }, "fields.version"},
		{"refusal keys", func(a *ArtifactsSpec) { delete(a.Refusals, "too_large") }, "refusals"},
		{"refusal entry", func(a *ArtifactsSpec) { a.Refusals["not_found"] = []string{"\x00"} }, "refusals.not_found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := cloneArtifacts(base)
			tc.edit(&a)
			err := ValidateArtifacts(a)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
	// A disabled table is not checked further.
	m := &Manifest{Artifacts: ArtifactsSpec{Enabled: false, Server: "anything"}}
	if err := m.validateArtifacts(); err != nil {
		t.Fatalf("disabled table: %v", err)
	}
}

func TestCheckOverrideArtifacts(t *testing.T) {
	base := embeddedArtifacts(t)
	ok := []func(a *ArtifactsSpec){
		func(a *ArtifactsSpec) {},
		func(a *ArtifactsSpec) { a.PublishVia = ArtifactsPublishVer },
		func(a *ArtifactsSpec) { a.PublishVia = ArtifactsPublishOff },
		func(a *ArtifactsSpec) { a.ReadPerRound = 4; a.MaxContentBytes = 1 << 20 },
		func(a *ArtifactsSpec) { a.ReminderPrefixes = append(a.ReminderPrefixes, "Reminder:") },
		func(a *ArtifactsSpec) { a.Refusals["type_made"] = []string{"This artifact was made from a type"} },
		func(a *ArtifactsSpec) { a.Fields.Version = append(a.Fields.Version, "version_name") },
		func(a *ArtifactsSpec) { a.Enabled = false },
	}
	for i, edit := range ok {
		o := cloneArtifacts(base)
		edit(&o)
		if err := checkOverrideArtifacts(&Manifest{Artifacts: base}, &Manifest{Artifacts: o}); err != nil {
			t.Errorf("ok %d: %v", i, err)
		}
	}
	bad := map[string]func(a *ArtifactsSpec){
		"server":        func(a *ArtifactsSpec) { a.Server = "x" },
		"limit":         func(a *ArtifactsSpec) { a.MaxRounds = 99 },
		"pattern":       func(a *ArtifactsSpec) { a.VersionPattern = "^.*$" },
		"forms removed": func(a *ArtifactsSpec) { a.WrapperForms = []string{"bare"} },
		"field removed": func(a *ArtifactsSpec) { a.Fields.Version = []string{"version"} },
	}
	for name, edit := range bad {
		o := cloneArtifacts(base)
		edit(&o)
		if err := checkOverrideArtifacts(&Manifest{Artifacts: base}, &Manifest{Artifacts: o}); err == nil {
			t.Errorf("%s: override accepted", name)
		}
	}
	// publish_via "files" over an embedded "off": the transport R-AR-4
	// tests through an override.
	off := cloneArtifacts(base)
	off.PublishVia = ArtifactsPublishOff
	if err := checkOverrideArtifacts(&Manifest{Artifacts: off}, &Manifest{Artifacts: base}); err != nil {
		t.Errorf("override choosing files: %v", err)
	}
	// Enabling where the embedded manifest says false.
	if err := checkOverrideArtifacts(&Manifest{}, &Manifest{Artifacts: base}); err == nil {
		t.Error("override enabled artifact sync on a manifest that has it off")
	}
}
