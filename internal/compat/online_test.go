package compat_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
)

// online7Text returns the embedded 7.x manifest's text.
func online7Text(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("manifests", "7.x.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOnlineTablesValidate(t *testing.T) {
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		o := m.Online
		switch m.Name {
		case "6.x":
			if o.Enabled {
				t.Error("6.x: online enabled")
			}
		case "7.x":
			if !o.Enabled || o.BatchMax != 20 || o.Sidebar.GroupByReport["folder"] != "project" || len(o.Sidebar.GroupBy) != 4 ||
				len(o.Sidebar.Refusals) != 6 || o.Responses.ToolsOff[0] != "Sidebar tools are turned off right now." {
				t.Errorf("7.x online %+v", o)
			}
		}
	}
}

func TestOnlineValidationErrors(t *testing.T) {
	base := online7Text(t)
	for name, tc := range map[string]struct{ old, new, msg string }{
		"settle too long":         {`settle_after_launch = "45s"`, `settle_after_launch = "6m"`, "online.settle_after_launch"},
		"grace too short":         {`result_grace = "10m"`, `result_grace = "10s"`, "online.result_grace"},
		"read age not a duration": {`read_max_age = "2m"`, `read_max_age = "2 minutes"`, "online.read_max_age"},
		"batch":                   {`batch_max = 20`, `batch_max = 51`, "online.batch_max"},
		"shows":                   {`session_start_max_shows = 3`, `session_start_max_shows = 10`, "online.session_start_max_shows"},
		"payload":                 {`post_max_payload = 8388608`, `post_max_payload = 1024`, "online.post_max_payload"},
		"server name":             {`guarded_servers_extra = []`, `guarded_servers_extra = ["Bad Server"]`, "online.guarded_servers_extra"},
		"always_deny":             {`always_deny = []`, `always_deny = ["rm"]`, "online.always_deny"},
		"report outside the set":  {`folder = "project"`, `folder = "folder"`, "online.sidebar.group_by_report"},
		"group_by custom":         {`manual = ["date", "state", "none"]`, `manual = ["date", "custom"]`, "online.sidebar.group_by.manual"},
		"group_by class missing":  {`pinned = ["date", "state", "none"]` + "\n", ``, "online.sidebar.group_by"},
		"sort outside the enum":   {`sort_by = ["recency", "alpha", "created"]`, `sort_by = ["recency", "user_order"]`, "online.sidebar.sort_by"},
		"refusal not printable":   {`  "This section groups its own rows by",`, `  "This section\u0007 groups",`, "online.sidebar.refusals"},
		"name units":              {`max_name_units = 200`, `max_name_units = 201`, "online.sidebar.max_name_units"},
		"tools_off empty":         {`tools_off = ["Sidebar tools are turned off right now."]`, `tools_off = []`, "online.responses.tools_off"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(base, tc.old) {
				t.Fatalf("fixture lacks %q", tc.old)
			}
			_, err := compat.Decode([]byte(strings.Replace(base, tc.old, tc.new, 1)), "7.x.toml")
			if err == nil || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("err %v, want %q", err, tc.msg)
			}
		})
	}
}

func TestOnlineOverrideMayOnlyAdd(t *testing.T) {
	base := online7Text(t)
	added := strings.Replace(base, `always_deny = []`, `always_deny = ["mcp__ccd_sidebar__set_unread"]`, 1)
	dir := t.TempDir()
	writeOverride(t, dir, "add.toml", added)
	if _, err := compat.Load(dir); err != nil {
		t.Fatalf("adding always_deny: %v", err)
	}
	// An override of an override is not possible; removing from the
	// embedded table is checked by turning the embedded entries into the
	// base: build a manifest pair by hand.
	b, err := compat.Decode([]byte(added), "base.toml")
	if err != nil {
		t.Fatal(err)
	}
	o, err := compat.Decode([]byte(base), "over.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := compat.CheckOverrideForTest(b, o); err == nil || !strings.Contains(err.Error(), "online.always_deny") {
		t.Fatalf("removing always_deny: %v", err)
	}
	extra := strings.Replace(base, `guarded_servers_extra = []`, `guarded_servers_extra = ["extra_srv"]`, 1)
	if b, err = compat.Decode([]byte(extra), "base.toml"); err != nil {
		t.Fatal(err)
	}
	if err := compat.CheckOverrideForTest(b, o); err == nil || !strings.Contains(err.Error(), "guarded_servers_extra") {
		t.Fatalf("removing guarded_servers_extra: %v", err)
	}
}
