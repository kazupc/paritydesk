package compat_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/model"
)

// v02Tables holds every v0.2 manifest table with sample values (fake ids
// only), as the units will add them below their anchors.
const v02Tables = `
[[row]]
row = "pins"
write_path = "on"
per_org = false
probes = []
[row.idb]
dir = "IndexedDB/https_claude.ai_0.indexeddb.leveldb"
backend_prefix = "IndexedDB/https_claude.ai_0.indexeddb."
database = "keyval-store"
object_store = "keyval"
key = "store:pin-state:dframe-starred-code"
ids_path = ["state", "starredIds"]
version_path = ["version"]
updated_path = ["updatedAt"]
versions = [0]
id_pattern = '^local_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
blink_versions = [19, 21]
v8_versions = [13, 16]
max_value_bytes = 1048576
max_copy_bytes = 67108864
max_clone_bytes = 2147483648
max_files = 512
max_read_ms = 10000
max_items = 5000
mirror_pref_path = ["preferences", "epitaxyPrefs", "starred-local-code-sessions"]
mirror_ls_key = "LSS-persisted.starred-local-code-sessions"
mirror_ls_ids_path = ["value"]
mirror_ls_time_path = ["timestamp"]

[[row]]
row = "scratch"
write_path = "off"
files = ["scratch-workspaces"]
probes = ["scratch-root"]
[row.scratch]
root = "scratch-workspaces"
leaf = '^scratch-[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9a-f]{6}$'
exclude = [".DS_Store", "**/.DS_Store"]
dependency_dirs = ["node_modules"]
vcs_dirs = [".svn"]
sweep_untouched_days = 7

[[probe]]
id = "scratch-root"
row = "scratch"
kind = "dir-optional"
target = "scratch-workspaces"

[online]
enabled = true
settle_after_launch = "45s"
replica_wait = "2m"
result_grace = "10m"
unbound_lease_ttl = "3m"
read_max_age = "2m"
batch_max = 20
session_start_max_shows = 3
post_max_payload = 8388608
guarded_servers_extra = []
guarded_tools_extra = ["ArtifactCheck"]
always_deny = []

[online.pins]
max_steps = 200
skip_if_true = ["isArchived"]
skip_if_present = ["scheduledTaskId"]
ok_pinned = ["Session pinned."]
ok_unpinned = ["Session unpinned."]
refusals_unpin = ["This session's section is hidden"]
unknown_outcome = ["The app window didn't answer."]
cascade_unpin = true
family_fields = ["displayParentSessionId", "lineage.sessionId"]
family_max_depth = 64
assign_store_key = "dframe-store"
assign_path = ["state", "customGroupsByScope", "{acct}/{org}"]
row_key_prefix = "code:"

[online.sidebar]
builtin = ["pinned", "routines", "sessions"]
manual_kind = "manual"
group_by_field = "groupBy"
sort_by_field = "sortBy"
name_field = "name"
kind_field = "kind"
max_name_units = 200
sort_by = ["recency", "alpha", "created"]
refusals = ["This section has no view of its own in this sidebar layout"]
[online.sidebar.group_by_report]
none = "none"
folder = "project"
[online.sidebar.group_by]
sessions = ["date", "project", "state", "none"]
routines = ["date", "state", "none"]
pinned = ["date", "state", "none"]
manual = ["date", "state", "none"]

[online.responses]
tools_off = ["Sidebar tools are turned off right now."]

[artifacts]
enabled = true
server = "claude-artifacts"
publish_via = "files"
read_mode = "chunks"
entry_path = "index.html"
blob_suffix = ".html.txt"
outbox_suffix = ".html"
default_icon = "page"
max_content_bytes = 16777216
version_max_bytes = 262144
max_title_units = 256
list_page_limit = 100
max_list_pages = 50
read_limit_lines = 1500
max_chunks = 64
first_chunk_estimate = 98304
round_bytes = 786432
inspect_per_round = 60
read_per_round = 12
publish_per_round = 5
batch_max = 2
result_grace_reads = "60s"
max_rounds = 40
others_marker = "<artifact-content-authored-by-others/>"
wrapper_tag_pattern = '^untrusted-document-[0-9a-f]{8,64}$'
wrapper_forms = ["bare", "nl"]
reminder_prefixes = []
version_pattern = '^[A-Za-z0-9._:-]{1,128}$'
version_time_prefix = '^([0-9]{9,11})-'

[artifacts.fields]
list_items = ["artifacts"]
next_cursor = ["next_cursor"]
slug = ["slug"]
url = ["url"]
version = ["version", "version_id"]
updated_at = ["updated_at"]
soft_deleted = ["soft_deleted"]
owner = ["rel"]
owner_mine = "mine"
mine_bool = ["mine"]
title = ["title"]
read = ["read"]
icon = ["icon"]
total_lines = ["total_lines"]
offset = ["offset"]
next_offset = ["next_offset"]
notice = ["notice"]
summary_files = ["files", "file_names"]
owned = ["owned_by_user"]
files_list = ["files"]
file_path = ["path"]
capabilities = ["capabilities"]

[artifacts.refusals]
type_made = []
live_doc = []
version_conflict = []
not_found = []
client_refused = []
too_large = []

[legacy]
ignore_names = [".DS_Store"]
agree_ignoring_roles = ["account_bound", "grant", "run_state", "resume_mark"]

[[legacy.stub]]
file = "scheduled-tasks.json"
empty_members = ["scheduledTasks"]
flag_members = ["sundayAliasBoundaryStamped"]

[[legacy.scoped_dir]]
id = "code-sessions"
root = "claude-code-sessions"
layout = "{acct}/{org}"
quarantine = true
unique_check = true

[[legacy.scoped_dir]]
id = "agent-skills"
root = "local-agent-mode-sessions/skills-plugin"
layout = "{org}/{acct}"
skip_names = ["00000000-0000-4000-8000-000000000001"]
quarantine = true
`

func TestEmbeddedManifestsLoadWithV02Anchors(t *testing.T) {
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		b, err := os.ReadFile(filepath.Join("manifests", m.Name+".toml"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		for _, a := range []string{"rows scratch", "rows sidebar-offline", "rows pins", "probes scratch", "probes sidebar-offline",
			"tables online", "tables pins", "tables artifacts", "tables legacy"} {
			// A unit inserts its lines directly below its anchor, so the
			// anchor is followed by a blank line or by that unit's block.
			if strings.Count(text, "\n# v0.2 anchor: "+a+"\n") != 1 {
				t.Errorf("%s: anchor %q missing or repeated", m.Name, a)
			}
		}
		// The online layer is on for 7.x only (6.x has no confirmed tool
		// texts). Until their units land, the other v0.2 tables are
		// absent: artifact sync is off and no legacy layout is declared.
		if m.Online.Enabled != (m.Name == "7.x") {
			t.Errorf("%s: online.enabled = %v", m.Name, m.Online.Enabled)
		}
		if m.Artifacts.Enabled || len(m.Legacy.ScopedDirs) != 0 {
			t.Errorf("%s: v0.2 tables present before their units", m.Name)
		}
		for _, r := range []model.RowID{model.RowScratch, model.RowSidebarCollapse, model.RowSidebarSeed, model.RowPins} {
			if pendingV02[r] {
				if _, ok := m.Row(r); ok {
					t.Errorf("%s: row %s declared while pending", m.Name, r)
				}
			}
		}
	}
}

func TestOverrideWithV02TablesDecodes(t *testing.T) {
	dir := t.TempDir()
	// The embedded 6.x manifest has its own [online] table (enabled =
	// false); the override replaces it with the full sample.
	base := strings.Replace(embeddedText(t), "[online]\nenabled = false\n", "", 1)
	// ... and its pins row (write_path = "none") is replaced by the
	// sample's.
	base = strings.Replace(base, "[[row]]\nrow = \"pins\"\n# Where 6.x keeps pins is not verified: not carried in this version.\nwrite_path = \"none\"\n", "", 1)
	writeOverride(t, dir, "override.toml", base+v02Tables)
	ms, err := compat.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := ms[0]
	if m.Name != "6.x" {
		t.Fatalf("manifest %s", m.Name)
	}
	pins, ok := m.Row(model.RowPins)
	if !ok || pins.WritePath != compat.PathOn || pins.IDB == nil || pins.Scratch != nil {
		t.Fatalf("pins row %+v", pins)
	}
	idb := pins.IDB
	if idb.Database != "keyval-store" || idb.ObjectStore != "keyval" || !reflect.DeepEqual(idb.IDsPath, []string{"state", "starredIds"}) ||
		!reflect.DeepEqual(idb.BlinkVersions, []int{19, 21}) || idb.MaxCloneBytes != 2147483648 || idb.MirrorLSKey != "LSS-persisted.starred-local-code-sessions" {
		t.Errorf("idb %+v", idb)
	}
	sc, ok := m.Row(model.RowScratch)
	if !ok || sc.Scratch == nil || sc.IDB != nil || sc.Scratch.Root != "scratch-workspaces" || sc.Scratch.SweepUntouchedDays != 7 ||
		!reflect.DeepEqual(sc.Scratch.VCSDirs, []string{".svn"}) {
		t.Fatalf("scratch row %+v", sc)
	}
	if exp := sc.Expand(testReplica()); exp.Scratch != sc.Scratch {
		t.Error("Expand dropped [row.scratch]")
	}
	var probe compat.Probe
	for _, p := range m.Probes {
		if p.ID == "scratch-root" {
			probe = p
		}
	}
	if probe.Kind != compat.ProbeDirOptional {
		t.Errorf("probe %+v", probe)
	}
	o := m.Online
	if !o.Enabled || o.SettleAfterLaunch != "45s" || o.PostMaxPayload != 8388608 || !reflect.DeepEqual(o.GuardedToolsExtra, []string{"ArtifactCheck"}) ||
		o.Pins.MaxSteps != 200 || !o.Pins.CascadeUnpin || o.Pins.RowKeyPrefix != "code:" ||
		o.Sidebar.MaxNameUnits != 200 || o.Sidebar.GroupByReport["folder"] != "project" || len(o.Sidebar.GroupBy["sessions"]) != 4 ||
		!reflect.DeepEqual(o.Responses.ToolsOff, []string{"Sidebar tools are turned off right now."}) {
		t.Errorf("online %+v", o)
	}
	a := m.Artifacts
	if !a.Enabled || a.Server != "claude-artifacts" || a.MaxContentBytes != 16777216 || a.ResultGraceReads != "60s" ||
		a.Fields.OwnerMine != "mine" || !reflect.DeepEqual(a.Fields.Version, []string{"version", "version_id"}) ||
		len(a.Refusals) != 6 || !hasKey(a.Refusals, "too_large") {
		t.Errorf("artifacts %+v", a)
	}
	l := m.Legacy
	if len(l.ScopedDirs) != 2 || l.ScopedDirs[1].Layout != "{org}/{acct}" || !l.ScopedDirs[0].UniqueCheck ||
		len(l.Stubs) != 1 || l.Stubs[0].File != "scheduled-tasks.json" || !reflect.DeepEqual(l.IgnoreNames, []string{".DS_Store"}) {
		t.Errorf("legacy %+v", l)
	}
}

func TestV02TablesRejectUnknownKeys(t *testing.T) {
	base := embeddedText(t)
	for name, extra := range map[string]string{
		"online":           "\n[online]\nsurprise = 1\n",
		"online.pins":      "\n[online.pins]\nsurprise = 1\n",
		"online.sidebar":   "\n[online.sidebar]\nsurprise = 1\n",
		"online.responses": "\n[online.responses]\nsurprise = 1\n",
		"artifacts":        "\n[artifacts]\nsurprise = 1\n",
		"artifacts.fields": "\n[artifacts.fields]\nsurprise = [\"x\"]\n",
		"legacy":           "\n[legacy]\nsurprise = 1\n",
		"legacy.stub":      "\n[[legacy.stub]]\nfile = \"x\"\nsurprise = 1\n",
		"row.idb":          "\n[[row]]\nrow = \"pins\"\nwrite_path = \"none\"\n[row.idb]\nsurprise = 1\n",
		"row.scratch":      "\n[[row]]\nrow = \"scratch\"\nwrite_path = \"off\"\n[row.scratch]\nsurprise = 1\n",
	} {
		if _, err := compat.Decode([]byte(base+extra), "test.toml"); err == nil {
			t.Errorf("%s: unknown key accepted", name)
		}
	}
}

func TestScratchLeafRENilSafe(t *testing.T) {
	var s *compat.ScratchSpec
	if s.LeafRE() != nil {
		t.Fatal("nil spec has a pattern")
	}
	if (&compat.ScratchSpec{Leaf: "^x$"}).LeafRE() != nil {
		t.Fatal("pattern compiled outside a load")
	}
}

func testReplica() model.Replica {
	return model.Replica{Account: "00000000-0000-4000-8000-000000000001", Org: "00000000-0000-4000-8000-000000000002"}
}

func hasKey(m map[string][]string, k string) bool {
	_, ok := m[k]
	return ok
}
