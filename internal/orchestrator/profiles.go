package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/state"
)

// chromeNote explains Claude in Chrome with several accounts (ADR 0003).
const chromeNote = "Claude in Chrome follows the Chrome profile's sign-in, not the Claude folder: use one Chrome profile per account"

// InitOptions are the answers `init` collects interactively.
type InitOptions struct {
	Alias   ids.Alias
	Label   string
	Managed bool
}

// session is a locked command that edits state (init and profile
// commands).
type session struct {
	o    *Orchestrator
	root *state.Root
	lock *state.Lock
}

// lockedSession takes the run lock and loads the root, refusing an open
// run and a pending profile add (recover settles both first).
func (o *Orchestrator) lockedSession(ctx context.Context, op string) (*session, error) {
	if o.d.ReadOnly {
		return nil, pderr.New(pderr.Usage, op, op+" writes; it cannot run in read-only mode")
	}
	l, err := o.d.State.Lock(ctx, 0)
	if err != nil {
		return nil, err
	}
	root, err := o.loadRoot()
	if err != nil {
		_ = l.Release()
		return nil, err
	}
	if root.Run != nil {
		_ = l.Release()
		return nil, pderr.New(pderr.NeedsRecover, op, "a run was interrupted; run paritydesk recover")
	}
	if root.PendingAdd != nil {
		_ = l.Release()
		return nil, pderr.New(pderr.NeedsRecover, op, "a profile add is waiting for its sign-in; run paritydesk recover")
	}
	if err := o.checkBinding(root); err != nil {
		_ = l.Release()
		return nil, err
	}
	return &session{o: o, root: root, lock: l}, nil
}

func (s *session) close() { _ = s.lock.Release() }

// scanFolder returns the scanned folder with the given name.
func (o *Orchestrator) scanFolder(ctx context.Context, name string) (*profile.Folder, []profile.Folder, error) {
	folders, err := o.d.Folders.Scan(ctx)
	if err != nil {
		return nil, nil, pderr.Wrap(pderr.Internal, "scan", err)
	}
	for i := range folders {
		if folders[i].Name == name {
			return &folders[i], folders, nil
		}
	}
	return nil, folders, nil
}

// validateNewAlias checks an alias that is about to be added.
func (o *Orchestrator) validateNewAlias(root *state.Root, a ids.Alias) error {
	if _, err := ids.ParseAlias(string(a)); err != nil {
		return pderr.Wrap(pderr.Usage, "profile", err)
	}
	if _, ok := o.d.Config.Profile(a); ok || root.Profiles[a] != nil {
		return pderr.New(pderr.Usage, "profile", "profile "+string(a)+" already exists")
	}
	return nil
}

// Init records the active folder's account as a profile, writes the config
// (printing each change first), detects other tools and asks for
// Automation while the user is present.
func (o *Orchestrator) Init(ctx context.Context, opts InitOptions) error {
	s, err := o.lockedSession(ctx, "init")
	if err != nil {
		return err
	}
	defer s.close()
	if err := o.validateNewAlias(s.root, opts.Alias); err != nil {
		return err
	}
	f, _, err := o.scanFolder(ctx, ids.DefaultFolder)
	if err != nil {
		return err
	}
	if f == nil || f.Identity == nil {
		return pderr.New(pderr.IdentityMismatch, "init", "Claude holds no signed-in account; open Claude and sign in first")
	}
	if a := aliasOf(s.root, f.Identity.Account); a != "" {
		return pderr.New(pderr.Usage, "init", "the active account is already registered as "+string(a))
	}
	cfg := o.d.Config
	o.progress(P0Preflight, "start", fmt.Sprintf("config: add profile %s (account %s)", opts.Alias, ids.Mask(string(f.Identity.Account))))
	cfg.Profiles = append(cfg.Profiles, config.Profile{Alias: opts.Alias, Label: opts.Label, ManagedByOrg: opts.Managed})
	if err := o.saveConfig(); err != nil {
		return err
	}
	s.root.Profiles[opts.Alias] = newProfileState(opts.Alias, f)
	s.root.Active = opts.Alias
	if err := o.commit(s.root); err != nil {
		return err
	}
	o.initChecks(ctx, s.root)
	o.progress(P0Preflight, "ok", chromeNote)
	return nil
}

// initChecks reports other tools and asks for Automation (interactive).
func (o *Orchestrator) initChecks(ctx context.Context, root *state.Root) {
	d := o.d
	if b, err := d.App.Locate(ctx, d.Config.App.BundleID); err == nil {
		if m, _, err := d.Compat.Manifest(ctx, b.Version); err == nil && m != nil {
			for _, t := range o.foreignTools(m.Footprints) {
				o.progress(P0Preflight, "warn", "another account-switching tool was detected: "+t)
			}
		}
	}
	if !o.interactive(false) {
		return
	}
	if err := d.App.ProbeAutomation(ctx, d.Config.App.BundleID); err != nil {
		o.progress(P0Preflight, "warn", "Automation permission for Claude is missing; paritydesk doctor --grant-automation asks again")
		return
	}
	o.progress(P0Preflight, "ok", "Automation permission for Claude is granted")
}

// ProfileAdopt registers a parked folder after checking its identity.
func (o *Orchestrator) ProfileAdopt(ctx context.Context, alias ids.Alias, folder string) error {
	s, err := o.lockedSession(ctx, "profile adopt")
	if err != nil {
		return err
	}
	defer s.close()
	if err := o.validateNewAlias(s.root, alias); err != nil {
		return err
	}
	if err := ids.ValidateFolder(folder); err != nil {
		return pderr.Wrap(pderr.Usage, "profile adopt", err)
	}
	f, _, err := o.scanFolder(ctx, folder)
	if err != nil {
		return err
	}
	if f == nil {
		return pderr.New(pderr.Usage, "profile adopt", "folder "+folder+" was not found")
	}
	if f.Identity == nil {
		return pderr.New(pderr.IdentityMismatch, "profile adopt", folder+" holds no signed-in account")
	}
	if a := aliasOf(s.root, f.Identity.Account); a != "" {
		return pderr.New(pderr.IdentityMismatch, "profile adopt", folder+" holds the account of profile "+string(a))
	}
	p := config.Profile{Alias: alias}
	if folder != ids.DefaultParkedFolder(alias) {
		p.Folder = folder
	}
	o.d.Config.Profiles = append(o.d.Config.Profiles, p)
	if err := o.saveConfig(); err != nil {
		return err
	}
	s.root.Profiles[alias] = newProfileState(alias, f)
	return o.commit(s.root)
}

// ProfileAdd adds an account without signing out: quit the app, park the
// current folder, launch on an empty default folder so the user signs in
// through the app's own flow (root.PendingAdd until then), and explain the
// one-Chrome-profile-per-account setup for Claude in Chrome.
func (o *Orchestrator) ProfileAdd(ctx context.Context, alias ids.Alias, managed bool) error {
	s, err := o.lockedSession(ctx, "profile add")
	if err != nil {
		return err
	}
	defer s.close()
	root := s.root
	if err := o.validateNewAlias(root, alias); err != nil {
		return err
	}
	if err := ids.ValidateFolder(ids.DefaultParkedFolder(alias)); err != nil {
		return pderr.Wrap(pderr.Usage, "profile add", err)
	}
	r := o.newRun(state.RunAdd, SwitchOptions{LogOffset: -1})
	r.root, r.interactive = root, o.interactive(false)
	if err := r.newRunID(); err != nil {
		return err
	}
	if err := r.locate(ctx); err != nil {
		return err
	}
	if err := r.resolve(ctx); err != nil {
		return err
	}
	r.T = &profile.Binding{Profile: config.Profile{Alias: alias, ManagedByOrg: managed}}
	if r.manifest != nil {
		if tools := o.foreignTools(r.manifest.Footprints); len(tools) > 0 {
			return pderr.New(pderr.ForeignTool, "profile add", "another account-switching tool was detected: "+tools[0])
		}
	}
	return r.addAccount(ctx, managed)
}

// addAccount quits, parks Claude/ and launches on an empty folder.
func (r *run) addAccount(ctx context.Context, managed bool) error {
	d := r.d()
	if err := r.quitApp(ctx); err != nil {
		return err
	}
	if _, err := r.quiesce(ctx, r.sPath); err != nil {
		return r.abort(ctx, err)
	}
	if err := r.ctxCheck(ctx); err != nil {
		return err
	}
	cctx, cancel := uncancel(ctx)
	defer cancel()
	sParked := r.o.folderPath(r.S.Profile.ParkedFolder())
	data := map[string]string{"managed": fmt.Sprint(managed)}
	if err := r.o.begin(r.root, P9Park, state.IntentAddParked, data); err != nil {
		return err
	}
	if err := d.FS.RenameExcl(r.sPath, sParked); err != nil {
		if errors.Is(err, fs.ErrExist) {
			err = pderr.New(pderr.Usage, "profile add", sParked+" already exists; nothing was moved")
		}
		return r.abort(cctx, err)
	}
	r.root.PendingAdd = &state.PendingAdd{Alias: r.T.Profile.Alias, Managed: managed, ParkedFrom: r.S.Profile.Alias, Started: r.o.now()}
	if err := r.o.done(r.root, P9Park, state.IntentAddParked); err != nil {
		return err
	}
	r.root.Run = nil
	if err := r.o.commit(r.root); err != nil {
		return err
	}
	if err := d.App.Launch(cctx, d.Config.App.BundleID); err != nil {
		r.o.progress(P11Launch, "warn", "Claude could not be launched; open it manually to sign in")
	}
	r.o.progress(P11Launch, "ok", "sign in to the new account in Claude, then run paritydesk recover to register it as "+string(r.T.Profile.Alias))
	r.o.progress(P11Launch, "ok", chromeNote)
	return nil
}

// ProfileRename renames an alias (state and config; the folder is renamed
// only if it uses the default name and the app is stopped).
func (o *Orchestrator) ProfileRename(ctx context.Context, from, to ids.Alias) error {
	s, err := o.lockedSession(ctx, "profile rename")
	if err != nil {
		return err
	}
	defer s.close()
	cfg := o.d.Config
	i := slices.IndexFunc(cfg.Profiles, func(p config.Profile) bool { return p.Alias == from })
	if i < 0 {
		return pderr.New(pderr.Usage, "profile rename", "unknown profile "+string(from))
	}
	if err := o.validateNewAlias(s.root, to); err != nil {
		return err
	}
	p := cfg.Profiles[i]
	if p.Folder == "" && s.root.Active != from {
		if err := o.renameDefaultFolder(ctx, from, to); err != nil {
			if !errors.Is(err, errKeepFolder) {
				return err
			}
			p.Folder = ids.DefaultParkedFolder(from)
		}
	}
	p.Alias = to
	cfg.Profiles[i] = p
	if err := o.saveConfig(); err != nil {
		return err
	}
	renameAlias(s.root, from, to)
	o.rewriteMarker(s.root, to, p)
	return o.commit(s.root)
}

// errKeepFolder means the folder keeps its old name (recorded in the
// config instead).
var errKeepFolder = errors.New("orchestrator: folder keeps its name")

// renameDefaultFolder renames Claude-<from> to Claude-<to> while the app
// is stopped.
func (o *Orchestrator) renameDefaultFolder(ctx context.Context, from, to ids.Alias) error {
	d := o.d
	if err := ids.ValidateFolder(ids.DefaultParkedFolder(to)); err != nil {
		return errKeepFolder
	}
	b, err := d.App.Locate(ctx, d.Config.App.BundleID)
	if err != nil {
		return errKeepFolder
	}
	if procs, err := d.App.Running(ctx, b.Path); err != nil || len(procs) > 0 {
		return errKeepFolder
	}
	err = d.FS.RenameExcl(o.folderPath(ids.DefaultParkedFolder(from)), o.folderPath(ids.DefaultParkedFolder(to)))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return errKeepFolder
	}
	return pderr.Wrap(pderr.Usage, "profile rename", err)
}

// renameAlias moves every state entry of one alias to another.
func renameAlias(root *state.Root, from, to ids.Alias) {
	if ps := root.Profiles[from]; ps != nil {
		ps.Alias = to
		root.Profiles[to] = ps
		delete(root.Profiles, from)
	}
	root.Bases = rekey(root.Bases, from, to)
	root.Overlays = rekey(root.Overlays, from, to)
	root.ToolStamps = rekey(root.ToolStamps, from, to)
	if root.Contested != nil {
		root.Contested = rekey(root.Contested, from, to)
	}
	if root.Active == from {
		root.Active = to
	}
	if root.Previous == from {
		root.Previous = to
	}
	if pa := root.PendingAdd; pa != nil {
		if pa.ParkedFrom == from {
			pa.ParkedFrom = to
		}
		if pa.Alias == from {
			pa.Alias = to
		}
	}
	if root.Run != nil {
		if root.Run.From == from {
			root.Run.From = to
		}
		if root.Run.To == from {
			root.Run.To = to
		}
	}
	for i := range root.Generations {
		if root.Generations[i].Target == from {
			root.Generations[i].Target = to
		}
	}
	for k, list := range root.TombstoneLedger {
		for i, a := range list {
			if a == from {
				list[i] = to
			}
		}
		root.TombstoneLedger[k] = list
	}
}

func rekey[V any](m map[string]V, from, to ids.Alias) map[string]V {
	if m == nil {
		return nil
	}
	out := make(map[string]V, len(m))
	prefix := string(from) + "/"
	for k, v := range m {
		if rest, ok := strings.CutPrefix(k, prefix); ok {
			k = string(to) + "/" + rest
		}
		out[k] = v
	}
	return out
}

// rewriteMarker updates the alias in a renamed profile's marker.
func (o *Orchestrator) rewriteMarker(root *state.Root, a ids.Alias, p config.Profile) {
	folder := o.folderPath(p.ParkedFolder())
	if root.Active == a {
		folder = o.claudePath()
	}
	m, err := o.d.ReadMarker(o.d.FS, folder)
	if err != nil || m == nil {
		return
	}
	m.Alias = a
	_ = o.d.WriteMarker(o.d.FS, folder, *m)
}

// ProfileRemove unregisters a profile; its folder is left intact.
func (o *Orchestrator) ProfileRemove(ctx context.Context, alias ids.Alias) error {
	s, err := o.lockedSession(ctx, "profile remove")
	if err != nil {
		return err
	}
	defer s.close()
	cfg := o.d.Config
	i := slices.IndexFunc(cfg.Profiles, func(p config.Profile) bool { return p.Alias == alias })
	if i < 0 {
		return pderr.New(pderr.Usage, "profile remove", "unknown profile "+string(alias))
	}
	if s.root.Active == alias {
		return pderr.New(pderr.Usage, "profile remove", "the active profile cannot be removed; switch to another profile first")
	}
	cfg.Profiles = slices.Delete(cfg.Profiles, i, i+1)
	if err := o.saveConfig(); err != nil {
		return err
	}
	delete(s.root.Profiles, alias)
	dropAliasKeys(s.root, alias)
	if s.root.Previous == alias {
		s.root.Previous = ""
	}
	if err := o.commit(s.root); err != nil {
		return err
	}
	if err := o.d.State.GC(s.root); err != nil {
		o.progress(P10Commit, "warn", "unreferenced state objects could not be removed")
	}
	o.progress(P10Commit, "ok", "removed "+string(alias)+"; its folder was left as it is")
	return nil
}
