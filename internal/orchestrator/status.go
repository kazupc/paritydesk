package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/redact"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// staleJobAge is how old a runner plist without a result must be before
// doctor reports it as stale.
const staleJobAge = time.Hour

// ProfileStatus is one profile in `status`.
type ProfileStatus struct {
	Alias     ids.Alias `json:"alias"`
	Label     string    `json:"label,omitempty"`
	Folder    string    `json:"folder"`
	Active    bool      `json:"active"`
	Account   string    `json:"account_masked,omitempty"`
	Managed   bool      `json:"managed_by_org"`
	BaseValid bool      `json:"base_valid"`
	Problem   string    `json:"problem,omitempty"`
}

// Status is the output of `status`.
type Status struct {
	Active       ids.Alias           `json:"active,omitempty"`
	Previous     ids.Alias           `json:"previous,omitempty"`
	AppRunning   bool                `json:"app_running"`
	NeedsRecover bool                `json:"needs_recover"`
	UpdateStaged bool                `json:"update_staged"`
	Profiles     []ProfileStatus     `json:"profiles"`
	LastVerify   *state.VerifyRecord `json:"last_verify,omitempty"`
	Generations  int                 `json:"generations"`
	Warnings     []string            `json:"warnings,omitempty"`
}

// Status reports the current state. With short, it reads only root.json
// and folder names (under 50 ms, never opens LevelDB, G5).
func (o *Orchestrator) Status(ctx context.Context, short bool) (*Status, error) {
	root, err := o.d.State.Load()
	if err != nil {
		return nil, err
	}
	if root == nil {
		root = state.NewRoot(o.d.DataRoot)
	}
	ensureMaps(root)
	st := &Status{Active: root.Active, Previous: root.Previous, LastVerify: root.LastVerify,
		Generations: len(root.Generations), NeedsRecover: root.Run != nil}
	if root.Run != nil {
		st.Warnings = append(st.Warnings, "a run was interrupted; run paritydesk recover")
	}
	if pa := root.PendingAdd; pa != nil {
		st.NeedsRecover = true
		st.Warnings = append(st.Warnings, fmt.Sprintf("profile add of %s waits for its sign-in; after signing in, run paritydesk recover", pa.Alias))
	}
	names := folderNames(o.d.DataRoot)
	for _, p := range o.d.Config.Profiles {
		ps := ProfileStatus{Alias: p.Alias, Label: p.Label, Folder: p.ParkedFolder(), Active: p.Alias == root.Active,
			Managed: p.ManagedByOrg, BaseValid: hasBases(root, p.Alias)}
		if ps.Active {
			ps.Folder = ids.DefaultFolder
		}
		if !names[ps.Folder] {
			ps.Problem = "folder missing"
		}
		if s := root.Profiles[p.Alias]; s != nil {
			ps.Account = ids.Mask(string(s.Replica.Account))
		}
		st.Profiles = append(st.Profiles, ps)
	}
	if short {
		return st, nil
	}
	o.statusDetails(ctx, root, st)
	return st, nil
}

// statusDetails adds what needs the app, a folder scan and the resolver.
func (o *Orchestrator) statusDetails(ctx context.Context, root *state.Root, st *Status) {
	d := o.d
	if b, err := d.App.Locate(ctx, d.Config.App.BundleID); err == nil {
		if procs, err := d.App.Running(ctx, b.Path); err == nil {
			st.AppRunning = len(procs) > 0
		}
	}
	if staged, err := d.App.UpdateStaged(ctx, d.Config.App.BundleID); err == nil {
		st.UpdateStaged = staged
	}
	if list, err := d.Gens.List(); err == nil {
		st.Generations = len(list)
	}
	folders, err := d.Folders.Scan(ctx)
	if err != nil {
		st.Warnings = append(st.Warnings, "the data folders could not be read")
		return
	}
	res, err := d.Resolve(d.Config, root, folders, profile.Hints{})
	if err != nil {
		st.Warnings = append(st.Warnings, "profiles do not match their folders; run paritydesk doctor")
		return
	}
	for i := range st.Profiles {
		b := res.Bindings[st.Profiles[i].Alias]
		if b == nil {
			continue
		}
		st.Profiles[i].BaseValid = b.BaseValid && hasBases(root, b.Profile.Alias)
		if b.Folder != nil && b.Folder.Identity != nil {
			st.Profiles[i].Account = ids.Mask(string(b.Folder.Identity.Account))
		}
		if !b.BaseValid && b.BaseInvalidWhy != "" {
			st.Profiles[i].Problem = b.BaseInvalidWhy
		}
	}
	if res.OutsideSwitch {
		st.Warnings = append(st.Warnings, "the active account changed outside paritydesk; paritydesk sync re-applies the shared state")
	}
}

// folderNames returns the names of Claude and Claude-* entries.
func folderNames(root string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() && (n == ids.DefaultFolder || strings.HasPrefix(n, ids.DefaultFolder+"-")) {
			out[n] = true
		}
	}
	return out
}

func hasBases(root *state.Root, a ids.Alias) bool {
	prefix := string(a) + "/"
	for k := range root.Bases {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// Check is one doctor finding.
type Check struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "ok" | "warn" | "fail"
	Detail string `json:"detail"`
}

// DoctorOptions are the flags of `doctor`.
type DoctorOptions struct {
	GrantAutomation bool
}

// doctor collects checks.
type doctor struct {
	o      *Orchestrator
	checks []Check
}

func (dr *doctor) add(id, status, detail string) {
	dr.checks = append(dr.checks, Check{ID: id, Status: status, Detail: detail})
}

// Doctor checks the installation: app version and mode, probes per folder,
// folder identities (masked), markers, unclassified top-level entries,
// other tools, Automation, staged update, Chrome bridge connection after
// the last launch and the one-profile-per-account setup (ADR 0003), and
// account-looks-managed hints. It never writes.
func (o *Orchestrator) Doctor(ctx context.Context, opts DoctorOptions) ([]Check, error) {
	d := o.d
	dr := &doctor{o: o}
	root, err := d.State.Load()
	if err != nil || root == nil {
		root = state.NewRoot(d.DataRoot)
		if err != nil {
			dr.add("state", "fail", "root.json is unreadable; run paritydesk recover")
		}
	}
	ensureMaps(root)
	dr.state(root)
	folders, err := d.Folders.Scan(ctx)
	if err != nil {
		dr.add("folders", "fail", "the data folders could not be read")
	}
	b, berr := d.App.Locate(ctx, d.Config.App.BundleID)
	var m *compat.Manifest
	if berr != nil {
		dr.add("version", "fail", "Claude was not found")
	} else {
		m = dr.version(ctx, root, b, folders)
	}
	dr.identities(root, folders)
	dr.markers(root, folders)
	if m != nil {
		dr.unclassified(m)
		if tools := o.foreignTools(m.Footprints); len(tools) > 0 {
			dr.add("foreign-tools", "fail", "another account-switching tool was detected: "+strings.Join(tools, ", "))
		} else {
			dr.add("foreign-tools", "ok", "no other account-switching tool")
		}
	}
	dr.automation(ctx, opts.GrantAutomation)
	dr.update(ctx)
	if m != nil {
		dr.bridge(ctx, m.Signals)
		dr.busyDetection(m)
	}
	dr.managed()
	dr.runnerJobs()
	return dr.checks, nil
}

func (dr *doctor) state(root *state.Root) {
	switch {
	case root.Run != nil:
		dr.add("state", "fail", "a run was interrupted; run paritydesk recover")
	case root.PendingAdd != nil:
		dr.add("state", "warn", "profile add of "+string(root.PendingAdd.Alias)+" waits for its sign-in")
	default:
		dr.add("state", "ok", "no open run")
	}
}

// version reports the app version, the mode and the probes per folder.
// It decides the mode from the same inputs as a switch's preflight
// (probes, Local Storage format, native multi-account, the active org, a
// downgrade and the SQLite backend) and lists the reasons and the rows a
// run would skip, so exits 31 and 32 are explained.
func (dr *doctor) version(ctx context.Context, root *state.Root, b app.BundleIdentity, folders []profile.Folder) *compat.Manifest {
	d := dr.o.d
	m, tested, err := d.Compat.Manifest(ctx, b.Version)
	if err != nil {
		m = nil
	}
	in := compat.DecideInput{Manifest: m, Version: b.Version, Tested: tested, RequireTested: d.Config.App.RequireTested,
		CompressionOK: true, Downgraded: downgraded(root.LastAppVersion, b.Version)}
	for _, f := range folders {
		if f.Name == ids.DefaultFolder {
			in.IdentityOK = f.Identity != nil
		}
		if sqliteLS(f.Path) {
			in.SQLiteLS = true
		}
		if f.Identity != nil {
			if a := aliasOf(root, f.Identity.Account); a != "" && profile.NativeMultiAccount(root.Profiles[a], f.Identity.Account) {
				in.NativeMultiAccount = true
			}
		}
		if dir := sources.LevelDBPath(f.Path); d.KV != nil {
			if _, err := os.Stat(dir); err == nil {
				if err := d.KV.CheckFormat(ctx, dir); errors.Is(err, kv.ErrUnknownCompression) {
					in.CompressionOK = false
				}
			}
		}
		if m == nil {
			continue
		}
		res := dr.probeFolder(ctx, m, f)
		in.Probes = append(in.Probes, res...)
	}
	in.OrgUnknown = dr.org(ctx, root, folders, m)
	dec := d.Compat.Decide(in)
	status := "ok"
	switch dec.Mode {
	case compat.ModeRefuse, compat.ModeSwapOnly, compat.ModePreviewOnly:
		status = "fail"
	case compat.ModeDegraded, compat.ModeFullWarn:
		status = "warn"
	}
	t := "untested"
	if tested {
		t = "tested"
	}
	dr.add("version", status, fmt.Sprintf("Claude %s (%s), mode %s", b.Version, t, dec.Mode))
	for _, reason := range dec.Reasons {
		dr.add("mode-reason", status, reason)
	}
	if len(dec.SkippedRows) > 0 {
		names := make([]string, len(dec.SkippedRows))
		for i, r := range dec.SkippedRows {
			names[i] = string(r)
		}
		dr.add("skipped-rows", "warn", "a run would skip: "+strings.Join(names, ", "))
	}
	return m
}

// org reports the active folder's organization (masked) and whether it
// is unknown, as a switch would resolve it.
func (dr *doctor) org(ctx context.Context, root *state.Root, folders []profile.Folder, m *compat.Manifest) bool {
	d := dr.o.d
	if d.Resolve == nil {
		return false
	}
	var hints profile.Hints
	var acct ids.AccountID
	for _, f := range folders {
		if f.Name == ids.DefaultFolder && f.Identity != nil {
			acct = f.Identity.Account
		}
	}
	if m != nil && acct != "" {
		if org, found, err := d.App.ActiveOrg(ctx, m.Signals, acct); err == nil && found {
			hints.ActiveOrg = org
		}
	}
	res, err := d.Resolve(d.Config, root, folders, hints)
	if err != nil || res == nil || res.Active == nil {
		return false
	}
	if res.Active.Replica.Org == "" {
		dr.add("org", "warn", "the active organization is unknown; per-organization rows are skipped (exit 32)")
		return true
	}
	dr.add("org", "ok", "active organization "+ids.Mask(string(res.Active.Replica.Org)))
	return false
}

// probeFolder runs the probes on one folder and reports them.
func (dr *doctor) probeFolder(ctx context.Context, m *compat.Manifest, f profile.Folder) []compat.ProbeResult {
	d := dr.o.d
	var rep model.Replica
	if len(f.Replicas) > 0 {
		rep = f.Replicas[0]
	}
	ls, err := dr.o.lsReader(ctx, f.Path)
	if err != nil {
		dr.add("probes", "warn", f.Name+": Local Storage could not be read")
	}
	defer closeReader(ls)
	res, err := d.Compat.Probes(ctx, m, compat.ProbeEnv{Folder: f.Path, Replica: rep, LS: ls})
	if err != nil {
		dr.add("probes", "fail", f.Name+": probes could not run")
		return nil
	}
	failed := 0
	for _, p := range res {
		if !p.OK {
			failed++
		}
	}
	if failed > 0 {
		var names []string
		for _, p := range res {
			if !p.OK {
				names = append(names, p.ProbeID)
			}
		}
		dr.add("probes", "warn", fmt.Sprintf("%s: %d of %d probes failed: %s", f.Name, failed, len(res), strings.Join(names, ", ")))
	} else {
		dr.add("probes", "ok", fmt.Sprintf("%s: %d probes passed", f.Name, len(res)))
	}
	return res
}

func (dr *doctor) identities(root *state.Root, folders []profile.Folder) {
	for _, f := range folders {
		if f.Identity == nil {
			dr.add("identity", "warn", f.Name+": no account recorded")
			continue
		}
		owner := aliasOf(root, f.Identity.Account)
		if owner == "" {
			owner = "unregistered"
		}
		dr.add("identity", "ok", fmt.Sprintf("%s: account %s (%s)", f.Name, ids.Mask(string(f.Identity.Account)), owner))
	}
}

func (dr *doctor) markers(root *state.Root, folders []profile.Folder) {
	for _, f := range folders {
		if f.Identity == nil {
			continue
		}
		owner := aliasOf(root, f.Identity.Account)
		switch {
		case owner == "":
		case f.Marker == nil:
			dr.add("marker", "warn", f.Name+": no marker; its bases are rebuilt on the next run")
		case f.Marker.Alias != owner:
			dr.add("marker", "warn", f.Name+": the marker names another profile; its bases are invalid")
		default:
			dr.add("marker", "ok", f.Name+": marker matches "+string(owner))
		}
	}
}

// unclassified lists, by name only, top-level entries of Claude/ that the
// manifest does not classify, so a maintainer can add them. Such entries
// are never touched (plan validation allows writes below synced only).
// Contents are never opened. Names pass through redact (ids embedded in
// a name become <id>) here, and the printer's redaction applies on top.
func (dr *doctor) unclassified(m *compat.Manifest) {
	entries, err := os.ReadDir(dr.o.claudePath())
	if err != nil {
		return
	}
	var out []string
	for _, e := range entries {
		if !m.Classified(e.Name()) {
			out = append(out, redact.Redactor{}.Key(e.Name()))
		}
	}
	if len(out) > 0 {
		sort.Strings(out)
		dr.add("unclassified", "warn", fmt.Sprintf("%d unclassified entries in Claude (never touched; add them to the compat manifest): %s",
			len(out), strings.Join(out, ", ")))
		return
	}
	dr.add("unclassified", "ok", "every entry in Claude is classified")
}

func (dr *doctor) automation(ctx context.Context, grant bool) {
	d := dr.o.d
	if !grant {
		dr.add("automation", "ok", "not checked; paritydesk doctor --grant-automation asks macOS for the permission")
		return
	}
	if err := d.App.ProbeAutomation(ctx, d.Config.App.BundleID); err != nil {
		dr.add("automation", "fail", "Automation permission for Claude is missing; allow it in System Settings > Privacy & Security > Automation")
		return
	}
	dr.add("automation", "ok", "Automation permission for Claude is granted")
}

func (dr *doctor) update(ctx context.Context) {
	d := dr.o.d
	staged, err := d.App.UpdateStaged(ctx, d.Config.App.BundleID)
	switch {
	case err != nil:
		dr.add("update", "warn", "could not check for a staged update")
	case staged:
		dr.add("update", "warn", "a Claude update is staged; switch with --allow-update to let it install")
	default:
		dr.add("update", "ok", "no staged update")
	}
}

// bridge reports whether Claude in Chrome connected after the last launch.
func (dr *doctor) bridge(ctx context.Context, sig compat.Signals) {
	d := dr.o.d
	var from int64
	if n, err := d.App.LogSize(); err == nil && n > verifyLogTail {
		from = n - verifyLogTail
	}
	evs, _, err := d.App.LogEvents(ctx, from, sig, "")
	if err != nil {
		dr.add("chrome-bridge", "warn", "the Claude log could not be read")
		return
	}
	for _, e := range evs {
		if e == app.EvBridgeConnected {
			dr.add("chrome-bridge", "ok", "Claude in Chrome connected after the last launch")
			return
		}
	}
	dr.add("chrome-bridge", "warn", "no Claude in Chrome connection line was found in the Claude log since the last launch. "+
		"The extension follows the Chrome profile's sign-in, not the Claude folder: use one Chrome profile per account. "+
		"If Claude in Chrome works, the log line may have changed in this Claude version (release checklist step 6)")
}

// busyDetection reports whether paritydesk can see working Code
// sessions: it needs a turn_started log pattern or a sessions busy field
// in the manifest. Without either, only the app's own quit dialog
// protects a working session.
func (dr *doctor) busyDetection(m *compat.Manifest) {
	spec, _ := m.Row(model.RowSessions)
	if m.Signals.TurnStarted != "" || len(spec.Fields[sources.RoleBusy]) > 0 {
		dr.add("busy-detection", "ok", "working Code sessions are detected before Claude quits")
		return
	}
	dr.add("busy-detection", "warn", "busy detection unavailable: this Claude version has no confirmed busy signal, "+
		"so only Claude's own quit dialog protects a working Code session")
}

func (dr *doctor) managed() {
	var unmanaged []string
	for _, p := range dr.o.d.Config.Profiles {
		if !p.ManagedByOrg {
			unmanaged = append(unmanaged, string(p.Alias))
		}
	}
	if len(unmanaged) == 0 {
		dr.add("managed", "ok", "every profile is marked managed_by_org")
		return
	}
	dr.add("managed", "ok", "if an account of "+strings.Join(unmanaged, ", ")+" belongs to an organization, set managed_by_org = true for it")
}

// runnerJobs reports runner plists without a result file that are older
// than an hour.
func (dr *doctor) runnerJobs() {
	dir := dr.o.d.Paths.RunDir
	entries, err := os.ReadDir(dir)
	if err != nil || dir == "" {
		dr.add("runner", "ok", "no detached jobs")
		return
	}
	results := map[string]bool{}
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".result.json"); ok {
			results[id] = true
		}
	}
	stale := 0
	now := time.Now()
	for _, e := range entries {
		label, ok := strings.CutSuffix(e.Name(), ".plist")
		if !ok || !strings.HasPrefix(label, app.JobLabelPrefix) || results[strings.TrimPrefix(label, app.JobLabelPrefix)] {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > staleJobAge {
			stale++
		}
	}
	if stale > 0 {
		dr.add("runner", "warn", fmt.Sprintf("%d detached job(s) never finished; they are removed on the next detached switch", stale))
		return
	}
	dr.add("runner", "ok", "no stale detached jobs")
}

// foreignTools matches the data root's entries (other than registered
// profile folders) and home paths against the manifest's footprints.
func (o *Orchestrator) foreignTools(fps []compat.Footprint) []string {
	own := map[string]bool{ids.DefaultFolder: true}
	for _, p := range o.d.Config.Profiles {
		own[p.ParkedFolder()] = true
	}
	entries, _ := os.ReadDir(o.d.DataRoot)
	var out []string
	for _, fp := range fps {
		if o.footprintFound(fp, entries, own) {
			out = append(out, fp.Tool)
		}
	}
	return out
}

func (o *Orchestrator) footprintFound(fp compat.Footprint, entries []os.DirEntry, own map[string]bool) bool {
	for _, g := range fp.FolderGlobs {
		for _, e := range entries {
			if ok, _ := filepath.Match(g, e.Name()); ok && !own[e.Name()] {
				return true
			}
		}
	}
	if o.d.Home == "" {
		return false
	}
	for _, p := range fp.PathsUnderHome {
		full := filepath.Join(o.d.Home, filepath.Clean("/"+p))
		if _, err := os.Lstat(full); err == nil {
			return true
		}
	}
	return false
}
