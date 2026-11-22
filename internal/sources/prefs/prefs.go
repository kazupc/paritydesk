package prefs

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
)

// Top-level members of claude_desktop_config.json this package reads.
const (
	prefsMember = "preferences"
	mcpMember   = "mcpServers"
)

// defaultSecret is the secret member of an MCP server object when the
// manifest names none.
var defaultSecret = []string{"env"}

// AppPrefs is the adapter for row model.RowAppPrefs.
type AppPrefs struct{}

var _ sources.Adapter = AppPrefs{}

// NewAppPrefs returns the adapter.
func NewAppPrefs() sources.Adapter { return AppPrefs{} }

// Row implements sources.Adapter.
func (AppPrefs) Row() model.RowID { return model.RowAppPrefs }

// allowed returns the preference names the row may read and write: the
// allow-list the orchestrator passes in spec.Fields["allow"] (else every
// key the manifest tags ui_state or other), restricted to keys the
// manifest tags ui_state or other. Without a manifest nothing is allowed.
func allowed(rc sources.ReadContext) map[string]bool {
	out := map[string]bool{}
	if rc.Manifest == nil {
		return out
	}
	ok := func(k string) bool {
		t := rc.Manifest.PrefTags[k]
		return t == compat.TagUIState || t == compat.TagOther
	}
	names := rc.Spec.Fields[sources.RoleAllow]
	if len(names) == 0 {
		for k := range rc.Manifest.PrefTags {
			names = append(names, k)
		}
	}
	for _, k := range names {
		if ok(k) {
			out[k] = true
		}
	}
	return out
}

// Read implements sources.Adapter. Items are the allowed preference names
// present under preferences; values are as stored.
func (AppPrefs) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowAppPrefs)
	doc, err := sources.LoadFile(rc.Folder, sources.DesktopConfigFile)
	if err != nil {
		return nil, err
	}
	v, found, err := doc.Get(jsonx.Path{prefsMember})
	if err != nil || !found {
		return snap, err
	}
	obj, err := sources.DecodeObject(v, sources.DesktopConfigFile)
	if err != nil {
		return nil, err
	}
	for k := range allowed(rc) {
		raw, ok := obj[k]
		if !ok {
			continue
		}
		it, err := model.NewItem(k, raw, raw, model.Stamp{}, false)
		if err != nil {
			return nil, fmt.Errorf("prefs: %s: %w", sources.DesktopConfigFile, err)
		}
		snap.Items[k] = it
	}
	return snap, nil
}

// Ops implements sources.Adapter: ["preferences", <name>] is set or
// deleted for allowed names only. A target without the file or without
// preferences is left unchanged and the items are reported skipped (the
// container is shared with other rows, so no row creates it).
func (AppPrefs) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	var res sources.OpsResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	current, desired = sources.OrEmpty(current, model.RowAppPrefs), sources.OrEmpty(desired, model.RowAppPrefs)
	allow := allowed(oc.ReadContext)
	changed := changedKeys(current, desired, allow)
	if len(changed) == 0 {
		return res, nil
	}
	doc, err := sources.LoadFile(oc.Folder, sources.DesktopConfigFile)
	if err != nil {
		return res, err
	}
	b := sources.NewBuilder(doc, model.RowAppPrefs)
	if ok, err := b.Has(jsonx.Path{prefsMember}); err != nil || !ok {
		res.Skipped = skipAll(model.RowAppPrefs, changed, containerNote(doc.Present))
		return res, err
	}
	for _, k := range changed {
		if d, ok := desired.Items[k]; ok {
			b.Set(jsonx.Path{prefsMember, k}, d.Value, k, false)
		} else if err := b.Delete(jsonx.Path{prefsMember, k}, k, true); err != nil {
			return res, err
		}
	}
	res.Edits = b.Edits()
	return res, nil
}

func containerNote(filePresent bool) string {
	if filePresent {
		return sources.NoteMirrorMissing
	}
	return sources.NoteFileMissing
}

// changedKeys returns the sorted keys whose presence or hash differs,
// limited to allow when it is non-nil.
func changedKeys(current, desired *model.RowSnapshot, allow map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range []*model.RowSnapshot{current, desired} {
		for k := range s.Items {
			if seen[k] || (allow != nil && !allow[k]) {
				continue
			}
			seen[k] = true
			c, okc := current.Items[k]
			d, okd := desired.Items[k]
			if okc != okd || c.Hash != d.Hash {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

func skipAll(row model.RowID, keys []string, note string) []model.Change {
	out := make([]model.Change, len(keys))
	for i, k := range keys {
		out[i] = sources.Skip(row, k, note)
	}
	return out
}

// MCPServers is the adapter for row model.RowMCPServers.
type MCPServers struct{}

var _ sources.Adapter = MCPServers{}

// NewMCPServers returns the adapter.
func NewMCPServers() sources.Adapter { return MCPServers{} }

// Row implements sources.Adapter.
func (MCPServers) Row() model.RowID { return model.RowMCPServers }

// Read implements sources.Adapter. Items are server names; values are the
// server objects with every value of the secret members (env) replaced by
// "sha256:<hex>". Raw holds the server's original bytes.
func (MCPServers) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowMCPServers)
	doc, err := sources.LoadFile(rc.Folder, sources.DesktopConfigFile)
	if err != nil {
		return nil, err
	}
	v, found, err := doc.Get(jsonx.Path{mcpMember})
	if err != nil || !found {
		return snap, err
	}
	obj, err := sources.DecodeObject(v, sources.DesktopConfigFile)
	if err != nil {
		return nil, err
	}
	secret := rc.Spec.Fields[sources.RoleSecret]
	if len(secret) == 0 {
		secret = defaultSecret
	}
	for name, raw := range obj {
		val, isSecret, err := sources.HashSecrets(raw, secret, sources.DesktopConfigFile)
		if err != nil {
			return nil, err
		}
		it, err := model.NewItem(name, val, raw, model.Stamp{}, isSecret)
		if err != nil {
			return nil, fmt.Errorf("prefs: %s: %w", sources.DesktopConfigFile, err)
		}
		snap.Items[name] = it
	}
	return snap, nil
}

// Ops implements sources.Adapter: ["mcpServers", <name>] is set or
// deleted. Servers with secret values are written from the source's raw
// bytes after a hash check; when the source changed since the merge the
// target keeps its value and the item is reported skipped.
func (a MCPServers) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	var res sources.OpsResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	current, desired = sources.OrEmpty(current, model.RowMCPServers), sources.OrEmpty(desired, model.RowMCPServers)
	changed := changedKeys(current, desired, nil)
	if len(changed) == 0 {
		return res, nil
	}
	doc, err := sources.LoadFile(oc.Folder, sources.DesktopConfigFile)
	if err != nil {
		return res, err
	}
	if !doc.Present {
		res.Skipped = skipAll(model.RowMCPServers, changed, sources.NoteFileMissing)
		return res, nil
	}
	b := sources.NewBuilder(doc, model.RowMCPServers)
	var cache *model.RowSnapshot
	ensured := false
	for _, k := range changed {
		d, ok := desired.Items[k]
		if !ok {
			if err := b.Delete(jsonx.Path{mcpMember, k}, k, true); err != nil {
				return res, err
			}
			continue
		}
		val := d.Value
		if d.Secret {
			src, err := sources.SourceItem(ctx, a, oc.Source, &cache, k, d.Hash)
			if err != nil {
				return res, err
			}
			if src == nil {
				res.Skipped = append(res.Skipped, sources.Skip(model.RowMCPServers, k, sources.NoteSourceChanged))
				continue
			}
			val = src.Raw
		}
		if !ensured {
			if err := b.Ensure(jsonx.Path{mcpMember}, ""); err != nil {
				return res, err
			}
			ensured = true
		}
		b.Set(jsonx.Path{mcpMember, k}, val, k, d.Secret)
	}
	res.Edits = b.Edits()
	return res, nil
}

// PermissionAcks reads the folder's own permission-mode acknowledgement
// preferences as names and booleans only (never copied anywhere). The
// sessions merge uses them to cap permissionMode (D8, H11). A preference
// counts when its name mentions permissions and its value is a boolean, or
// an object holding a boolean under the replica's account.
func PermissionAcks(ctx context.Context, folder string, r model.Replica) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	doc, err := sources.LoadFile(folder, sources.DesktopConfigFile)
	if err != nil {
		return nil, err
	}
	v, found, err := doc.Get(jsonx.Path{prefsMember})
	if err != nil || !found {
		return out, err
	}
	obj, err := sources.DecodeObject(v, sources.DesktopConfigFile)
	if err != nil {
		return nil, err
	}
	for name, raw := range obj {
		if !strings.Contains(strings.ToLower(name), "permission") {
			continue
		}
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			out[name] = b
			continue
		}
		var byAcct map[string]json.RawMessage
		if json.Unmarshal(raw, &byAcct) == nil {
			if json.Unmarshal(byAcct[string(r.Account)], &b) == nil {
				out[name] = b
			}
		}
	}
	return out, nil
}
