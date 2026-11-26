package artifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/state"
)

// KindArtifacts is state.OnlineRecord.Kind of an artifacts round queue.
const KindArtifacts = online.QueueKindArtifacts

// Lock waits.
const (
	LockWait    = 3 * time.Second
	DueLockWait = 1 * time.Second
)

// Rounds is what the service asks of the orchestrator (A3): a callback
// under the run lock whose draft is published and recorded in
// Root.Online in one commit, and closing a round queue's record.
type Rounds interface {
	// ArtifactsRound takes the run lock (3 s; busy: exit 11), refuses an
	// open run (exit 41), loads root and calls f. When f returns a draft,
	// it is published with its header and recorded in Root.Online
	// (Kind "artifacts") before the lock is released; the steps hash is
	// returned.
	ArtifactsRound(ctx context.Context, f func(root *state.Root) (*online.Draft, *online.QueueHeader, error)) (string, error)
	// CloseArtifactsQueue records the queue complete in Root.Online when
	// the run lock is free within 3 s; false when it was busy.
	CloseArtifactsQueue(ctx context.Context, queueID string) (bool, error)
}

// Service is artifact sync over the archive and the online layer.
type Service struct {
	Archive *Archive
	// Online is the online service whose providers include Provider.
	Online     *online.Service
	OnlineSpec compat.OnlineSpec
	Spec       compat.ArtifactsSpec
	// Config is the effective config; nil when there is no config file.
	Config *config.Config
	// Root loads root.json without the run lock (nil root: no state).
	Root   func() (*state.Root, error)
	Rounds Rounds
	// Replica is main.log's replica (LastReplica with a zero since).
	Replica    func(ctx context.Context) online.ReplicaObs
	Getenv     func(string) string
	DefaultDir bool
	ReadOnly   bool
	AppVersion string
	Clock      clock.Clock
	SupportDir string
	DataRoot   string
	Rand       io.Reader
	// StrictLocks panics when a lock is taken out of the global order
	// (run, online, artifacts); tests set it.
	StrictLocks bool
	held        int
}

// lock levels of the global order.
const (
	levelOnline    = 2
	levelArtifacts = 3
)

func (s *Service) now() time.Time {
	if s.Clock == nil {
		return time.Now().UTC()
	}
	return s.Clock.Now().UTC()
}

// acquire records a lock level, panicking on an order violation when
// StrictLocks is set.
func (s *Service) acquire(level int) func() {
	if s.StrictLocks && s.held >= level {
		panic(fmt.Sprintf("artifacts: lock level %d taken while holding %d", level, s.held))
	}
	prev := s.held
	s.held = level
	return func() { s.held = prev }
}

// withOnline runs f under the online lock.
func (s *Service) withOnline(ctx context.Context, f func() error) error {
	defer s.acquire(levelOnline)()
	return s.Online.Store.Locked(ctx, f)
}

// withArchive runs f under the artifacts lock (wait; busy: exit 11).
func (s *Service) withArchive(ctx context.Context, wait time.Duration, f func() error) error {
	defer s.acquire(levelArtifacts)()
	err := s.Archive.Locked(ctx, wait, f)
	if err != nil && pderr.CodeOf(err) == pderr.RunLockBusy {
		var pe *pderr.Error
		if !errors.As(err, &pe) || pe.Op != "artifacts" {
			return pderr.NewT(pderr.RunLockBusy, "artifacts", i18n.ArtifactsErrBusy)
		}
	}
	return err
}

// policy is the data boundary of the config.
func (s *Service) policy() BoundaryPolicy { return PolicyFrom(s.Config) }

func (s *Service) cfgArtifacts() config.Artifacts {
	if s.Config == nil {
		return config.Default().Artifacts
	}
	return s.Config.Artifacts
}

func (s *Service) limits() plan.Limits {
	c := s.Config
	if c == nil {
		c = config.Default()
	}
	return plan.Limits{MaxDeleteFraction: c.Limits.MaxDeleteFraction, MaxDeleteMin: c.Limits.MaxDeleteMin}
}

// offLine is the line that says why artifact sync is off: the online
// layer turned off by online.reconcile = "off" gets its own line, since
// the remedy is another key (deviation: the online unit turns the whole
// online layer off with it, and artifact rounds run on that layer).
func (s *Service) offLine() i18n.Msg {
	if s.Config != nil && s.Config.Sync.Artifacts && s.Spec.Enabled && s.OnlineSpec.Enabled &&
		s.Config.Online.Reconcile == config.OnlineReconcileOff {
		return i18n.ArtifactsOffReconcile
	}
	return i18n.ArtifactsOff
}

// enabled reports artifact sync on for this config and app.
func (s *Service) enabled() bool {
	return s.Config != nil && s.Config.Sync.Artifacts && s.Spec.Enabled && s.OnlineSpec.Enabled &&
		s.Config.Online.Reconcile != config.OnlineReconcileOff
}

// publishes reports whether a sync can publish anything at all: the
// manifest's [artifacts] table is on and names a transport (5.8: "off"
// is capture only). Without it nothing is due (8.6, 4.6).
func (s *Service) publishes() bool {
	return s.Spec.Enabled && s.Spec.PublishVia != "" && s.Spec.PublishVia != compat.ArtifactsPublishOff
}

// due is Due (8.6) for this service's spec: 0 when publishing is off.
func (s *Service) due(m *Manifest, alias ids.Alias, rep model.Replica, inv *Inventory) int {
	if !s.publishes() {
		return 0
	}
	return Due(m, alias, rep, inv, s.policy(), s.cfgArtifacts(), s.AppVersion)
}

// manifest loads and validates the manifest (an absent one is empty).
func (s *Service) manifest() (*Manifest, error) {
	m, err := s.Archive.LoadManifest()
	if err != nil {
		return nil, pderr.WrapT(pderr.Internal, "artifacts", i18n.ArtifactsErrManifestInvalid, err)
	}
	if m == nil {
		return NewManifest(), nil
	}
	TrimHistory(m, s.cfgArtifacts().HistoryKeep)
	if err := Validate(m, s.Spec, s.cfgArtifacts().HistoryKeep); err != nil {
		return nil, pderr.NewT(pderr.Internal, "artifacts", i18n.ArtifactsErrManifestInvalid)
	}
	return m, nil
}

// blobErr maps a blob failure to its exit.
func blobErr(err error) error {
	if errors.Is(err, ErrBlobCorrupt) {
		return pderr.NewT(pderr.Internal, "artifacts", i18n.ArtifactsErrBlobCorrupt)
	}
	return pderr.Wrap(pderr.Internal, "artifacts", err)
}

// newOut returns an empty answer.
func newOut(state string) NextOut {
	return NextOut{V: 1, State: state, Calls: []online.Call{}, Tools: []string{}, Lines: []i18n.Text{}}
}

func lineOut(state string, m i18n.Msg) NextOut {
	o := newOut(state)
	if m != "" {
		o.Lines = []i18n.Text{i18n.Tx(m)}
	}
	return o
}

// ---------------------------------------------------------------- next

// Next is `artifacts next` (6.2).
func (s *Service) Next(ctx context.Context, a NextArgs) (NextOut, error) {
	switch {
	case !s.enabled():
		return lineOut(online.StateNone, s.offLine()), nil
	case !InClaude(s.Getenv):
		return lineOut(online.StateNone, i18n.ArtifactsErrOutsideApp), nil
	case !s.DefaultDir:
		return lineOut(online.StateNone, i18n.OnlineErrSupportDir), nil
	case s.ReadOnly:
		return lineOut(online.StateNone, i18n.ArtifactsErrReadOnly), nil
	}
	root, err := s.loadRoot()
	if err != nil {
		return NextOut{}, err
	}
	if root == nil || root.Active == "" || root.Profiles[root.Active] == nil {
		return newOut(online.StateNone), nil
	}
	if root.Run != nil {
		return NextOut{}, pderr.NewT(pderr.NeedsRecover, "artifacts next", i18n.ErrInterrupted)
	}
	if switchQueueOpen(root, s.Online) {
		return lineOut(StateBlocked, i18n.ArtifactsReconcileFirst), nil
	}
	if _, err := s.manifest(); err != nil {
		return NextOut{}, err
	}
	chain, err := s.Archive.LoadChain()
	if err != nil {
		return NextOut{}, pderr.Wrap(pderr.Internal, "artifacts", err)
	}
	obs := s.Replica(ctx)
	active := root.Profiles[root.Active].Replica
	switch {
	case !obs.Known:
		s.staleChain(ctx, chain, root, "replica_unknown")
		return lineOut(StateBlocked, i18n.ArtifactsReplicaUnknown), nil
	case obs.Replica != active:
		s.staleChain(ctx, chain, root, "replica_changed")
		return lineOut(StateBlocked, i18n.ArtifactsReplicaChanged), nil
	}
	chain, err = s.startOrResume(ctx, chain, root, obs.Replica, a.Session)
	if err != nil {
		return NextOut{}, err
	}
	var unavailable []string
	for _, t := range a.Unavailable {
		switch t {
		case ToolListArtifacts, ToolGetArtifact, ToolReadDeclaration, ToolReadArtifact, ToolPublishFiles, ToolPublishVersion:
			s.staleChain(ctx, chain, root, "tools_missing")
			return lineOut(online.StateNone, i18n.ArtifactsErrToolsMissing), nil
		case ToolRequestDir:
			if chain.NoPublish == "" {
				chain.NoPublish = "grant_declined"
				if err := s.saveChain(ctx, chain); err != nil {
					return NextOut{}, err
				}
			}
		}
		unavailable = append(unavailable, t)
	}
	if len(a.Refused) > 0 && chain.Queue != "" {
		if err := s.withOnline(ctx, func() error { return s.Online.Store.MarkRefused(ctx, chain.Queue, a.Refused) }); err != nil {
			return NextOut{}, err
		}
	}
	caller := online.Caller{Session: a.Session, Token: a.Token}
	token := ""
	for attempt := 0; attempt < 3; attempt++ {
		root, err = s.loadRoot()
		if err != nil {
			return NextOut{}, err
		}
		if chain.Terminal() {
			return s.finalOut(chain), nil
		}
		switch s.roundState(root, chain) {
		case roundOpen:
			lease, _ := s.Online.Store.ReadLease()
			b, err := s.Online.Next(ctx, online.NextCall{Caller: caller, Root: artifactsRootInfo(root), Unavailable: unavailable,
				Wait: a.Wait, Replica: func(ctx context.Context, _ time.Time) online.ReplicaObs { return s.Replica(ctx) }})
			if err != nil {
				return NextOut{}, err
			}
			if b.Token != "" {
				token, caller.Token = b.Token, b.Token
			}
			switch b.State {
			case online.StateComplete:
				// The round is done: ingest it and publish the next.
				if err := s.transition(ctx, chain, obs.Replica, lease, caller); err != nil {
					return NextOut{}, err
				}
				if chain, err = s.Archive.LoadChain(); err != nil || chain == nil {
					return NextOut{}, pderr.Wrap(pderr.Internal, "artifacts", fmt.Errorf("chain lost: %v", err))
				}
				continue
			case online.StateStale, online.StateNoHooks:
				s.staleChain(ctx, chain, root, map[string]string{online.StateStale: "replica_changed", online.StateNoHooks: "no_hooks"}[b.State])
			case online.StateNone:
				s.markChain(ctx, chain, ChainCancelled, "")
			}
			return s.wrap(chain, b, token), nil
		case roundDone, roundNone:
			if err := s.transition(ctx, chain, obs.Replica, nil, caller); err != nil {
				return NextOut{}, err
			}
			chain, err = s.Archive.LoadChain()
			if err != nil || chain == nil {
				return NextOut{}, pderr.Wrap(pderr.Internal, "artifacts", fmt.Errorf("chain lost: %v", err))
			}
		case roundLost:
			s.markChain(ctx, chain, ChainStale, "round_invalid")
			return lineOut(online.StateStale, i18n.OnlineStatusStale), nil
		}
	}
	return NextOut{}, pderr.NewT(pderr.RunLockBusy, "artifacts next", i18n.OnlineErrBusy)
}

// loadRoot loads root.json.
func (s *Service) loadRoot() (*state.Root, error) {
	if s.Root == nil {
		return nil, nil
	}
	r, err := s.Root()
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, "state", err)
	}
	return r, nil
}

// switchQueueOpen reports a switch's reconcile queue still waiting for
// this profile (6.2 step 1).
func switchQueueOpen(root *state.Root, ol *online.Service) bool {
	rec := root.Online
	if rec == nil || rec.Kind != "" || rec.Epoch != root.Epoch || rec.Profile != root.Active {
		return false
	}
	if rec.State != string(online.QueuePending) && rec.State != string(online.QueueActive) {
		return false
	}
	h, err := ol.ReadHead()
	if err != nil || h == nil || h.ID != rec.Queue {
		return false
	}
	return h.State != online.QueueComplete && h.State != online.QueueCancelled
}

// artifactsRootInfo is the root view online.Next gets for an artifacts
// round queue: the record as if it were a reconcile queue.
func artifactsRootInfo(root *state.Root) online.RootInfo {
	info := online.RootInfoOf(root)
	if info.Record != nil {
		rec := *info.Record
		rec.Kind = ""
		info.Record = &rec
	}
	return info
}

// round states.
const (
	roundNone = iota // no round yet, or the last one is ingested
	roundOpen        // the chain's queue is published and not finished
	roundDone        // the chain's queue is complete, not ingested yet
	roundLost        // the chain's queue is gone before it was ingested
)

func (s *Service) roundState(root *state.Root, c *Chain) int {
	if c.Round == 0 || c.Ingested >= c.Round {
		return roundNone
	}
	rec := root.Online
	if rec == nil || rec.Kind != KindArtifacts || rec.Queue != c.Queue {
		return roundLost
	}
	h, err := s.Online.ReadHead()
	if err != nil || h == nil || h.ID != c.Queue {
		return roundLost
	}
	switch h.State {
	case online.QueueComplete:
		return roundDone
	case online.QueueCancelled:
		return roundLost
	}
	return roundOpen
}

// newChainID returns art-<time>-<8 hex>.
func (s *Service) newChainID() (string, error) {
	b := make([]byte, 4)
	r := s.Rand
	if r == nil {
		r = rand.Reader
	}
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return "art-" + s.now().Format("20060102T150405Z") + "-" + hex.EncodeToString(b), nil
}

// startOrResume returns the chain to work on: a paused or active chain
// of this profile, replica, epoch and app version, else a new one (an
// open chain that no longer matches is marked stale first).
func (s *Service) startOrResume(ctx context.Context, c *Chain, root *state.Root, rep model.Replica, session string) (*Chain, error) {
	same := c != nil && c.Profile == root.Active && c.Replica == rep && c.Epoch == root.Epoch && c.AppVersion == s.AppVersion
	if same && !c.Terminal() {
		if c.State == ChainPaused {
			c.State = ChainActive
			if err := s.saveChain(ctx, c); err != nil {
				return nil, err
			}
		}
		return c, nil
	}
	if c != nil && !c.Terminal() {
		why := "profile"
		switch {
		case c.Profile != root.Active:
		case c.Replica != rep:
			why = "replica_changed"
		case c.Epoch != root.Epoch:
			why = "epoch"
		default:
			why = "app_version"
		}
		s.staleChain(ctx, c, root, why)
	}
	id, err := s.newChainID()
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, "artifacts", err)
	}
	nc := &Chain{V: 1, ID: id, Profile: root.Active, Replica: rep, Epoch: root.Epoch, AppVersion: s.AppVersion, Session: session,
		Started: s.now(), Phase: PhaseInventory, State: ChainActive, Report: Report{Pairs: []PairResult{}}}
	if err := s.withArchive(ctx, LockWait, func() error {
		m, err := s.manifest()
		if err != nil {
			return err
		}
		if m.Gate.AppVersion != s.AppVersion {
			m.Gate = Gate{Roundtrip: GateUnverified, AppVersion: s.AppVersion, At: s.now()}
			if err := s.Archive.CommitManifest(m, s.now()); err != nil {
				return err
			}
		}
		_ = s.Archive.GC(m, nil, s.now())
		return s.Archive.SaveChain(nc)
	}); err != nil {
		return nil, err
	}
	return nc, nil
}

// saveChain writes the chain under the artifacts lock.
func (s *Service) saveChain(ctx context.Context, c *Chain) error {
	return s.withArchive(ctx, LockWait, func() error { return s.Archive.SaveChain(c) })
}

// markChain sets a chain's state.
func (s *Service) markChain(ctx context.Context, c *Chain, st, why string) {
	if c == nil || c.Terminal() {
		return
	}
	c.State, c.StaleWhy = st, why
	_ = s.saveChain(ctx, c)
}

// staleChain stops an open chain: its round queue is cancelled (lease
// and armed removed) and nothing more is offered.
func (s *Service) staleChain(ctx context.Context, c *Chain, root *state.Root, why string) {
	if c == nil || c.Terminal() {
		return
	}
	if root != nil && root.Online != nil && root.Online.Kind == KindArtifacts && root.Online.Queue == c.Queue {
		_ = s.Online.Store.RemoveArmed()
		_ = s.withOnline(ctx, func() error {
			if err := s.Online.Store.ReleaseLease(ctx, c.Queue); err != nil {
				return err
			}
			q, err := s.Online.Store.ReadQueue(online.PhaseReconcile)
			if err != nil || q == nil || q.ID != c.Queue {
				return err
			}
			q.State = online.QueueCancelled
			return s.Online.Store.WriteQueue(q)
		})
	}
	s.markChain(ctx, c, ChainStale, why)
}

// wrap turns an online answer into the artifacts answer.
func (s *Service) wrap(c *Chain, b online.Batch, token string) NextOut {
	o := newOut(b.State)
	o.Chain, o.Round, o.Phase, o.Queue = c.ID, c.Round, c.Phase, c.Queue
	o.Token = token
	if b.Calls != nil {
		o.Calls = b.Calls
	}
	if b.Tools != nil {
		o.Tools = b.Tools
	}
	o.Question = b.Question
	o.RetryAfterMS = b.RetryAfterMS
	o.RemainingBatches = b.RemainingBatches + 2*phasesLeft(c.Phase) + 2
	o.Lines = append(o.Lines, b.Lines...)
	o.Counts = s.counts(c)
	return o
}

// phasesLeft is how many phases may follow p.
func phasesLeft(p Phase) int {
	switch p {
	case PhaseInventory:
		return 4
	case PhaseInspect:
		return 3
	case PhaseRead:
		return 2
	case PhasePublish:
		return 1
	}
	return 1
}

// counts are a `next` answer's counts.
func (s *Service) counts(c *Chain) Counts {
	var n Counts
	m, err := s.Archive.LoadManifest()
	if err == nil && m != nil {
		n.Pairs = len(m.Pairs)
		for _, p := range m.Pairs {
			if cp := p.Copies[c.Profile]; cp != nil && cp.State == StateLive && p.Content != nil && cp.SyncedSHA == p.Content.SHA256 {
				n.InStep++
			}
		}
		n.Ignored = len(m.Ignored)
	}
	for _, cand := range c.Candidates {
		if cand.State != "" {
			continue
		}
		if cand.Role == RoleRead {
			n.ToRead++
		} else {
			n.ToPublish++
		}
	}
	r := c.Report
	n.Saved, n.Published, n.Conflicts, n.DeletedHere, n.Skipped, n.Failed = r.Saved, r.Published, r.Conflicts, r.DeletedHere, r.Skipped, r.Failed
	return n
}

// finalOut is the answer for a chain that ended in this call.
func (s *Service) finalOut(c *Chain) NextOut {
	o := newOut(StateComplete)
	if c.State == ChainStale {
		o.State = online.StateStale
		if c.StaleWhy == "inventory_foreign" {
			o.Lines = []i18n.Text{i18n.Tx(i18n.ArtifactsInventoryForeign)}
		} else {
			o.Lines = []i18n.Text{i18n.Tx(i18n.OnlineStatusStale)}
		}
	}
	o.Chain, o.Round, o.Phase, o.Queue = c.ID, c.Round, c.Phase, c.Queue
	o.Counts = s.counts(c)
	return o
}

// transition ingests the chain's completed round (when not ingested yet)
// and publishes the next round, or completes the chain. lease is the
// lease of the finished round as it was before online.Next removed it
// (nil: none to carry over).
func (s *Service) transition(ctx context.Context, chain *Chain, rep model.Replica, lease *online.Lease, caller online.Caller) error {
	var newChain *Chain
	var roundErr error
	oldQueue := chain.Queue
	_, err := s.Rounds.ArtifactsRound(ctx, func(root *state.Root) (*online.Draft, *online.QueueHeader, error) {
		if root == nil || root.Active != chain.Profile || root.Epoch != chain.Epoch {
			s.markChain(ctx, chain, ChainStale, "epoch")
			return nil, nil, nil
		}
		if obs := s.Replica(ctx); !obs.Known || obs.Replica != rep {
			why := "replica_unknown"
			if obs.Known {
				why = "replica_changed"
			}
			s.staleChain(ctx, chain, root, why)
			return nil, nil, nil
		}
		var d *online.Draft
		var h *online.QueueHeader
		err := s.withOnline(ctx, func() error {
			return s.withArchive(ctx, LockWait, func() error {
				c, err := s.Archive.LoadChain()
				if err != nil || c == nil || c.ID != chain.ID {
					return pderr.Wrap(pderr.Internal, "artifacts", fmt.Errorf("chain changed: %v", err))
				}
				if c.Ingested < c.Round {
					if err := s.ingestLocked(c, rep); err != nil {
						return err
					}
				}
				if c.Terminal() {
					newChain = c
					return s.Archive.SaveChain(c)
				}
				var perr error
				d, h, perr = s.planLocked(c, root, rep)
				newChain = c
				if perr != nil {
					roundErr = perr
					c.State, c.StaleWhy = ChainStale, "round_invalid"
					d, h = nil, nil
				}
				return s.Archive.SaveChain(c)
			})
		})
		if err != nil {
			return nil, nil, err
		}
		return d, h, nil
	})
	if err != nil {
		return err
	}
	if roundErr != nil {
		return roundErr
	}
	if newChain == nil || newChain.Terminal() || newChain.Queue == "" {
		return nil
	}
	// Carry the lease over to the new round's queue (A7): same session
	// and token, a fresh deadline.
	if lease != nil && lease.Session != "" && lease.Queue == oldQueue && oldQueue != "" &&
		(caller.Session == "" || caller.Session == lease.Session) {
		l := *lease
		if caller.Token != "" {
			h := sha256.Sum256([]byte(caller.Token))
			l.TokenSHA256 = hex.EncodeToString(h[:])
		}
		return s.withOnline(ctx, func() error {
			if err := s.Online.Store.WriteLease(&l); err != nil {
				return err
			}
			mono := online.Read(s.Online.Mono)
			return s.Online.Store.RebindLease(ctx, l.Queue, newChain.Queue, s.now(), mono, s.Online.Settings.LeaseTTL)
		})
	}
	return nil
}

// roundResults reads the finished round queue and its results.
func (s *Service) roundResults(c *Chain) ([]RoundResult, error) {
	q, err := s.Online.Store.ReadQueue(online.PhaseReconcile)
	if err != nil {
		return nil, err
	}
	if q == nil || q.ID != c.Queue {
		return nil, errors.New("artifacts: the round queue is gone")
	}
	results := s.Online.Store.Results(q.ID, func(online.StepID) int64 { return 8 << 20 })
	var out []RoundResult
	for _, st := range q.Steps {
		if st.Kind != Kind {
			continue
		}
		d, err := DecodeStepData(st.Data)
		if err != nil {
			continue
		}
		rr := RoundResult{Step: st, Data: d}
		if r, ok := results[st.ID]; ok {
			r := r
			rr.Result = &r
		}
		out = append(out, rr)
	}
	return out, nil
}

// ingestLocked ingests the chain's round (the caller holds the online and
// artifacts locks) and writes the archive: blobs first, then partial
// chunks and the inventory, then the manifest, the chain and due.json.
func (s *Service) ingestLocked(c *Chain, rep model.Replica) error {
	results, err := s.roundResults(c)
	if err != nil {
		// The round is lost: nothing to ingest.
		c.Ingested = c.Round
		return nil
	}
	m, err := s.manifest()
	if err != nil {
		return err
	}
	inv, _ := s.Archive.LoadInventory(c.Profile)
	if inv != nil && inv.Chain != c.ID {
		if c.Phase != PhaseInventory {
			inv = nil
		}
	}
	out := Ingest(IngestInput{Spec: s.Spec, Config: s.cfgArtifacts(), Limits: s.limits(), Policy: s.policy(), Manifest: m,
		Chain: c, Inventory: inv, Partial: s.Archive.Partials(), Results: results, Profile: c.Profile, Replica: rep,
		AppVersion: s.AppVersion, Now: s.now(), Blob: s.Archive.ReadBlob})
	if err := s.writeIngest(out); err != nil {
		return err
	}
	*c = *out.Chain
	return nil
}

// writeIngest writes an ingest's output in the commit order.
func (s *Service) writeIngest(out IngestOutput) error {
	shas := make([]string, 0, len(out.Blobs))
	for sha := range out.Blobs {
		shas = append(shas, sha)
	}
	sort.Strings(shas)
	for _, sha := range shas {
		if err := s.Archive.PutBlob(sha, []byte(out.Blobs[sha])); err != nil {
			return blobErr(err)
		}
	}
	for _, sl := range out.PartialDrop {
		_ = s.Archive.DropPartial(sl)
	}
	for _, ch := range out.PartialPut {
		if err := s.Archive.PutPartial(ch); err != nil {
			// A chunk that cannot be kept fails its own read only; the
			// rest of the round is still recorded.
			_ = s.Archive.DropPartial(ch.Slug)
			if cand, ok := out.Chain.Candidates[ch.Slug]; ok && cand.State == "" {
				cand.State, cand.Reason = CandFailed, ReasonPartialUnwritable
				out.Chain.Candidates[ch.Slug] = cand
				out.Chain.Report.Failed++
				out.Chain.Report.Pairs = append(out.Chain.Report.Pairs, PairResult{Pair: cand.Pair, Slug: slugIfNew(&cand, ch.Slug),
					Result: "failed", Reason: ReasonPartialUnwritable})
			}
		}
	}
	for _, sha := range out.Unstage {
		_ = s.Archive.Unstage(sha)
	}
	if out.Inventory != nil {
		if err := s.Archive.SaveInventory(out.Inventory); err != nil {
			return pderr.Wrap(pderr.Internal, "artifacts", err)
		}
	}
	if err := s.Archive.CommitManifest(out.Manifest, s.now()); err != nil {
		return pderr.Wrap(pderr.Internal, "artifacts", err)
	}
	if err := s.Archive.SaveChain(out.Chain); err != nil {
		return pderr.Wrap(pderr.Internal, "artifacts", err)
	}
	s.writeDueLocked(out.Manifest, out.Chain.Profile, out.Chain.Replica, out.Chain.Epoch)
	_ = s.Archive.GC(out.Manifest, out.Chain, s.now())
	return nil
}

// planLocked plans the chain's next round and builds its draft (6.5).
// A finished chain is marked complete and gets no draft.
func (s *Service) planLocked(c *Chain, root *state.Root, rep model.Replica) (*online.Draft, *online.QueueHeader, error) {
	m, err := s.manifest()
	if err != nil {
		return nil, nil, err
	}
	if gateState(m, s.AppVersion) == GateMismatch && c.NoPublish == "" {
		c.NoPublish = "gate_closed"
	}
	inv, _ := s.Archive.LoadInventory(c.Profile)
	in := RoundInput{Spec: s.Spec, Config: s.cfgArtifacts(), Policy: s.policy(), Manifest: m, Inventory: inv,
		Partial: s.Archive.Partials(), Chain: c, Profile: c.Profile, Replica: rep, Paths: s.Archive.Paths(),
		AppVersion: s.AppVersion, Now: s.now(), Stage: s.Archive.Stage, Blob: s.Archive.ReadBlob}
	rp, phase, done, err := PlanRound(in)
	if err != nil {
		return nil, nil, pderr.Wrap(pderr.Internal, "artifacts", err)
	}
	for _, n := range rp.Notes {
		if n.Pair != "" && !slices.Contains(c.Tried, n.Pair) {
			c.Tried = append(c.Tried, n.Pair)
		}
		c.Report.Pairs = append(c.Report.Pairs, n)
		c.Report.Skipped++
	}
	if done {
		c.State, c.Phase = ChainComplete, PhaseDone
		c.Capped = s.Spec.MaxRounds > 0 && c.Round >= s.Spec.MaxRounds
		return nil, nil, nil
	}
	planJSON, err := json.Marshal(rp)
	if err != nil {
		return nil, nil, pderr.Wrap(pderr.Internal, "artifacts", err)
	}
	cfg := s.Config
	if cfg == nil {
		cfg = config.Default()
	}
	draft := online.Build(online.BuildInput{Spec: s.OnlineSpec, Config: cfg.Online, Phase: online.PhaseReconcile,
		From: c.Profile, To: c.Profile, Replica: rep, Extensions: map[string]json.RawMessage{ExtName: planJSON}},
		[]online.Provider{online.ProbeProvider(), Provider{Spec: s.Spec}})
	if !draftOK(draft) {
		return nil, nil, pderr.NewT(pderr.Internal, "artifacts", i18n.ArtifactsErrRoundInvalid)
	}
	c.Round++
	c.Phase = phase
	c.Queue = fmt.Sprintf("%s-%d", c.ID, c.Round)
	h := &online.QueueHeader{ID: c.Queue, Kind: KindArtifacts, Profile: c.Profile, From: c.Profile, Replica: rep,
		Epoch: root.Epoch, Spec: s.OnlineSpec}
	if c.Decision != "" {
		h.Decisions = map[string]string{QuestionID: c.Decision}
	}
	return &draft, h, nil
}

// draftOK: no warning, the probe, and at least one artifact step (6.5).
func draftOK(d online.Draft) bool {
	if len(d.Warnings) > 0 || len(d.Steps) < 2 || d.Steps[0].ID != online.ProbeStepID {
		return false
	}
	for _, st := range d.Steps {
		if st.Kind == Kind {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- decide, pause, done

// DecideOut is the answer of `artifacts decide`.
type DecideOut struct {
	V     int         `json:"-"`
	State string      `json:"state"`
	Lines []i18n.Text `json:"lines"`
}

// Decide is `artifacts decide` (6.4).
func (s *Service) Decide(ctx context.Context, caller online.Caller, question, option string) (DecideOut, error) {
	out := DecideOut{V: 1, State: online.StateNone, Lines: []i18n.Text{}}
	if question != QuestionID || (option != "apply" && option != "skip") {
		return out, pderr.NewT(pderr.Usage, "artifacts decide", i18n.ErrArtifactsUnknownQuestion)
	}
	if !InClaude(s.Getenv) {
		out.Lines = []i18n.Text{i18n.Tx(i18n.ArtifactsErrOutsideApp)}
		return out, nil
	}
	if s.ReadOnly {
		out.Lines = []i18n.Text{i18n.Tx(i18n.ArtifactsErrReadOnly)}
		return out, nil
	}
	root, err := s.loadRoot()
	if err != nil || root == nil {
		return out, err
	}
	chain, err := s.Archive.LoadChain()
	if err != nil || chain == nil || chain.Terminal() || root.Online == nil || root.Online.Queue != chain.Queue {
		return out, err
	}
	v, err := s.Online.Decide(ctx, caller, artifactsRootInfo(root), question, option)
	if err != nil {
		return out, err
	}
	out.State, out.Lines = v.State, v.Lines
	if v.State == online.StateRecorded {
		chain.Decision = option
		if err := s.saveChain(ctx, chain); err != nil {
			return out, err
		}
	}
	return out, nil
}

// PauseOut is the answer of `artifacts pause` and of `done` on an open
// chain.
type PauseOut struct {
	V     int         `json:"-"`
	State string      `json:"state"`
	Lines []i18n.Text `json:"lines"`
}

// Pause is `artifacts pause` (6.4, M14): the lease and armed go, the
// chain and its queue stay. A fresh lease of another caller is left
// alone (busy_elsewhere).
func (s *Service) Pause(ctx context.Context, caller online.Caller) (PauseOut, error) {
	out := PauseOut{V: 1, State: StatePaused, Lines: []i18n.Text{i18n.Tx(i18n.ArtifactsPaused)}}
	chain, err := s.Archive.LoadChain()
	if err != nil {
		return out, pderr.Wrap(pderr.Internal, "artifacts", err)
	}
	if chain != nil && s.otherCaller(chain.Queue, caller) {
		out.State, out.Lines = online.StateBusyElsewhere, []i18n.Text{i18n.Tx(i18n.OnlineBusyElsewhere)}
		return out, nil
	}
	if chain != nil && chain.Queue != "" {
		if err := s.withOnline(ctx, func() error { return s.Online.Store.ReleaseLease(ctx, chain.Queue) }); err != nil {
			return out, err
		}
	}
	if chain != nil && !chain.Terminal() && !s.ReadOnly {
		chain.State = ChainPaused
		if err := s.saveChain(ctx, chain); err != nil {
			return out, err
		}
	}
	return out, nil
}

// DoneOut is the answer of `artifacts done`.
type DoneOut struct {
	V      int          `json:"-"`
	Chain  string       `json:"chain,omitempty"`
	State  string       `json:"state"`
	Counts DoneCounts   `json:"counts"`
	Pairs  []PairResult `json:"pairs"`
	Lines  []i18n.Text  `json:"lines"`
}

// DoneCounts are the counts of `artifacts done`.
type DoneCounts struct {
	Saved       int    `json:"saved"`
	Published   int    `json:"published"`
	Created     int    `json:"created"`
	Conflicts   int    `json:"conflicts"`
	DeletedHere int    `json:"deleted_here"`
	Skipped     int    `json:"skipped"`
	Ignored     int    `json:"ignored"`
	Failed      int    `json:"failed"`
	Roundtrip   string `json:"roundtrip"`
}

// otherCaller reports a fresh lease on queue that belongs to another
// caller (neither its token nor its session).
func (s *Service) otherCaller(queue string, c online.Caller) bool {
	l, _ := s.Online.Store.ReadLease()
	if l == nil || queue == "" || l.Queue != queue || !l.Fresh(s.now(), online.Read(s.Online.Mono), s.Online.Store.Stopped()) {
		return false
	}
	return !l.TokenOK(c.Token) && (l.Session == "" || l.Session != c.Session)
}

// Done is `artifacts done` (6.4).
func (s *Service) Done(ctx context.Context, caller online.Caller) (DoneOut, error) {
	out := DoneOut{V: 1, State: online.StateNone, Pairs: []PairResult{}, Lines: []i18n.Text{}}
	chain, err := s.Archive.LoadChain()
	if err != nil {
		return out, pderr.Wrap(pderr.Internal, "artifacts", err)
	}
	if chain == nil {
		return out, nil
	}
	out.Chain = chain.ID
	if s.otherCaller(chain.Queue, caller) {
		out.State, out.Lines = online.StateBusyElsewhere, []i18n.Text{i18n.Tx(i18n.OnlineBusyElsewhere)}
		return out, nil
	}
	if chain.State != ChainComplete {
		p, err := s.Pause(ctx, caller)
		out.State, out.Lines = p.State, p.Lines
		if chain.State == ChainStale {
			out.State = online.StateStale
		}
		return out, err
	}
	if chain.Queue != "" {
		if err := s.withOnline(ctx, func() error { return s.Online.Store.ReleaseLease(ctx, chain.Queue) }); err != nil {
			return out, err
		}
	}
	m, err := s.manifest()
	if err != nil {
		return out, err
	}
	if !s.ReadOnly {
		if err := s.withArchive(ctx, LockWait, func() error {
			m, err = s.manifest()
			if err != nil {
				return err
			}
			pr := m.Profiles[chain.Profile]
			if pr == nil {
				pr = &ProfileRecord{Replica: chain.Replica}
				m.Profiles[chain.Profile] = pr
			}
			pr.LastSyncAt, pr.LastSyncEpoch = s.now(), chain.Epoch
			if err := s.Archive.CommitManifest(m, s.now()); err != nil {
				return err
			}
			s.writeDueLocked(m, chain.Profile, chain.Replica, chain.Epoch)
			return nil
		}); err != nil {
			return out, err
		}
	}
	closed := true
	if chain.Queue != "" && s.Rounds != nil {
		closed, err = s.Rounds.CloseArtifactsQueue(ctx, chain.Queue)
		if err != nil {
			return out, err
		}
	}
	r := chain.Report
	out.State = StateComplete
	out.Counts = DoneCounts{Saved: r.Saved, Published: r.Published, Created: r.Created, Conflicts: r.Conflicts,
		DeletedHere: r.DeletedHere, Skipped: r.Skipped, Ignored: r.Ignored, Failed: r.Failed, Roundtrip: m.Gate.Roundtrip}
	for _, p := range r.Pairs {
		out.Pairs = append(out.Pairs, PairResult{Pair: p.Pair, Slug: p.Slug, Result: p.Result, Reason: p.Reason})
	}
	out.Lines = doneLines(chain, m, closed)
	return out, nil
}

// doneLines are the fixed lines of `artifacts done`.
func doneLines(c *Chain, m *Manifest, closed bool) []i18n.Text {
	r := c.Report
	lines := []i18n.Text{i18n.Tx(i18n.ArtifactsDoneSummary, string(c.Profile), r.Saved, r.Published, r.Created, r.Conflicts, r.Skipped)}
	if r.DeletedHere > 0 {
		lines = append(lines, i18n.Tx(i18n.ArtifactsDoneDeleted, r.DeletedHere, string(c.Profile)))
	}
	if r.Ignored > 0 {
		lines = append(lines, i18n.Tx(i18n.ArtifactsDoneIgnored, r.Ignored))
	}
	switch c.NoPublish {
	case "client_refused":
		lines = append(lines, i18n.Tx(i18n.ArtifactsPublishUnavailable))
	case "grant_declined":
		lines = append(lines, i18n.Tx(i18n.ArtifactsGrantDeclined))
	case "gate_closed":
		lines = append(lines, i18n.Tx(i18n.ArtifactsGateClosed))
	}
	if c.Capped {
		lines = append(lines, i18n.Tx(i18n.ArtifactsRoundsCapped))
	}
	for _, p := range r.Pairs {
		if p.Reason == WhyOtherOrg {
			lines = append(lines, i18n.Tx(i18n.ArtifactsOtherOrgHint))
			break
		}
	}
	if !closed {
		lines = append(lines, i18n.Tx(i18n.ArtifactsDoneRootBusy))
	}
	_ = m
	return append(lines, i18n.Tx(i18n.ArtifactsDoneDetails))
}

// writeDueLocked writes due.json for a profile (the caller holds the
// artifacts lock).
func (s *Service) writeDueLocked(m *Manifest, alias ids.Alias, rep model.Replica, epoch uint64) {
	inv, _ := s.Archive.LoadInventory(alias)
	n := s.due(m, alias, rep, inv)
	_ = s.Archive.SaveDue(DueFile{Seq: m.Seq, Profile: alias, Epoch: epoch, Count: n})
}

// ---------------------------------------------------------------- ingest (CLI)

// IngestOut is the answer of `artifacts ingest`.
type IngestOut struct {
	V     int         `json:"-"`
	State string      `json:"state"`
	Lines []i18n.Text `json:"lines"`
}

// IngestNow is `artifacts ingest`: it ingests the chain's completed round
// now (normally `next` does it). Nothing is published.
func (s *Service) IngestNow(ctx context.Context) (IngestOut, error) {
	out := IngestOut{V: 1, State: online.StateNone, Lines: []i18n.Text{i18n.Tx(i18n.ArtifactsNothingToIngest)}}
	if s.ReadOnly {
		return out, pderr.NewT(pderr.Usage, "artifacts ingest", i18n.ArtifactsErrReadOnly)
	}
	root, err := s.loadRoot()
	if err != nil {
		return out, err
	}
	chain, err := s.Archive.LoadChain()
	if err != nil {
		return out, pderr.Wrap(pderr.Internal, "artifacts", err)
	}
	if root == nil || chain == nil || chain.Terminal() || s.roundState(root, chain) != roundDone {
		return out, nil
	}
	obs := s.Replica(ctx)
	if !obs.Known || obs.Replica != chain.Replica {
		out.State, out.Lines = StateBlocked, []i18n.Text{i18n.Tx(i18n.ArtifactsReplicaUnknown)}
		return out, nil
	}
	_, err = s.Rounds.ArtifactsRound(ctx, func(root *state.Root) (*online.Draft, *online.QueueHeader, error) {
		return nil, nil, s.withOnline(ctx, func() error {
			return s.withArchive(ctx, LockWait, func() error {
				c, err := s.Archive.LoadChain()
				if err != nil || c == nil || c.ID != chain.ID || c.Ingested >= c.Round {
					return err
				}
				return s.ingestLocked(c, obs.Replica)
			})
		})
	})
	if err != nil {
		return out, err
	}
	out.State, out.Lines = "ingested", []i18n.Text{i18n.Tx(i18n.ArtifactsIngested)}
	return out, nil
}

// ---------------------------------------------------------------- view (orchestrator)

// Due is the number of pairs due on alias (8.6), from the archive (no
// lock; readers rely on atomic replacement).
func (s *Service) Due(alias ids.Alias, rep model.Replica) (int, error) {
	if !s.Archive.Exists() {
		return 0, nil
	}
	m, err := s.manifest()
	if err != nil {
		return 0, err
	}
	inv, _ := s.Archive.LoadInventory(alias)
	if inv != nil && inv.Replica != rep {
		inv = nil
	}
	return s.due(m, alias, rep, inv), nil
}

// LastSyncEpoch is the epoch of the profile's last finished sync.
func (s *Service) LastSyncEpoch(alias ids.Alias) (uint64, bool, error) {
	if !s.Archive.Exists() {
		return 0, false, nil
	}
	m, err := s.manifest()
	if err != nil {
		return 0, false, err
	}
	pr := m.Profiles[alias]
	if pr == nil || pr.LastSyncAt.IsZero() {
		return 0, false, nil
	}
	return pr.LastSyncEpoch, true, nil
}

// WriteDue writes due.json for alias (P10, after the commit). The lock
// waits at most a second; on timeout nothing is written.
func (s *Service) WriteDue(ctx context.Context, alias ids.Alias, rep model.Replica, epoch uint64) error {
	if s.ReadOnly || !s.Archive.Exists() {
		return nil
	}
	return s.withArchive(ctx, DueLockWait, func() error {
		m, err := s.manifest()
		if err != nil {
			return err
		}
		s.writeDueLocked(m, alias, rep, epoch)
		return nil
	})
}

// RenameProfile moves a profile's copies, record and inventory to a new
// alias (profile rename).
func (s *Service) RenameProfile(ctx context.Context, from, to ids.Alias) error {
	if s.ReadOnly || !s.Archive.Exists() {
		return nil
	}
	return s.withArchive(ctx, LockWait, func() error {
		m, err := s.manifest()
		if err != nil {
			return err
		}
		changed := false
		for _, pairs := range []map[PairID]*Pair{m.Pairs, m.Forgotten} {
			for _, p := range pairs {
				if renamePair(p, from, to) {
					changed = true
				}
			}
		}
		if pr, ok := m.Profiles[from]; ok {
			delete(m.Profiles, from)
			m.Profiles[to] = pr
			changed = true
		}
		for _, ig := range m.Ignored {
			if ig.Alias == from {
				ig.Alias, changed = to, true
			}
		}
		if inv, err := s.Archive.LoadInventory(from); err == nil && inv != nil {
			inv.Profile = to
			if err := s.Archive.SaveInventory(inv); err != nil {
				return err
			}
			_ = s.Archive.RemoveInventory(from)
		}
		if c, err := s.Archive.LoadChain(); err == nil && c != nil && c.Profile == from {
			c.Profile = to
			if !c.Terminal() {
				c.State, c.StaleWhy = ChainStale, "profile"
			}
			_ = s.Archive.SaveChain(c)
		}
		if !changed {
			return nil
		}
		return s.Archive.CommitManifest(m, s.now())
	})
}

// RemoveProfile forgets a profile's record and inventory; its copies stay
// in the pairs (reported orphaned) so its content and history are kept.
func (s *Service) RemoveProfile(ctx context.Context, alias ids.Alias) error {
	if s.ReadOnly || !s.Archive.Exists() {
		return nil
	}
	return s.withArchive(ctx, LockWait, func() error {
		m, err := s.manifest()
		if err != nil {
			return err
		}
		_ = s.Archive.RemoveInventory(alias)
		if c, err := s.Archive.LoadChain(); err == nil && c != nil && c.Profile == alias && !c.Terminal() {
			c.State, c.StaleWhy = ChainStale, "profile"
			_ = s.Archive.SaveChain(c)
		}
		changed := false
		for _, pairs := range []map[PairID]*Pair{m.Pairs, m.Forgotten} {
			for _, p := range pairs {
				if removeFromPair(p, alias) {
					changed = true
				}
			}
		}
		if _, ok := m.Profiles[alias]; ok {
			delete(m.Profiles, alias)
			changed = true
		}
		if !changed {
			return nil
		}
		return s.Archive.CommitManifest(m, s.now())
	})
}

// renamePair moves every mention of alias from to to inside one pair:
// its copy, content and history sources, and the per-profile decisions.
func renamePair(p *Pair, from, to ids.Alias) bool {
	if p == nil {
		return false
	}
	changed := false
	if c, ok := p.Copies[from]; ok {
		delete(p.Copies, from)
		p.Copies[to] = c
		changed = true
	}
	if p.Content != nil && p.Content.SourceProfile == from {
		p.Content.SourceProfile, changed = to, true
	}
	for i := range p.History {
		if p.History[i].SourceProfile == from {
			p.History[i].SourceProfile, changed = to, true
		}
	}
	for _, l := range []*[]ids.Alias{&p.Flags.AllowCreate, &p.Flags.Restore, &p.Flags.TitleTaken} {
		for i, a := range *l {
			if a == from {
				(*l)[i], changed = to, true
			}
		}
	}
	return changed
}

// removeFromPair forgets a removed profile's decisions in one pair and
// clears it as the content's source: the alias may later name another
// account, which must neither inherit `allow --create`/`--restore` nor
// count as the (possibly managed) source of the content (8.5, 8.7). The
// copy, content and history stay.
func removeFromPair(p *Pair, alias ids.Alias) bool {
	if p == nil {
		return false
	}
	changed := false
	for _, l := range []*[]ids.Alias{&p.Flags.AllowCreate, &p.Flags.Restore, &p.Flags.TitleTaken} {
		n := len(*l)
		*l = slices.DeleteFunc(*l, func(a ids.Alias) bool { return a == alias })
		if len(*l) != n {
			changed = true
		}
	}
	if p.Content != nil && p.Content.SourceProfile == alias {
		p.Content.SourceProfile, changed = "", true
	}
	return changed
}

// ---------------------------------------------------------------- helpers

// findPair resolves a pair id, a copy's slug, or (ignored) slug.
func findPair(m *Manifest, ref string) (PairID, bool) {
	if ValidPairID(ref) {
		if _, ok := m.Pairs[PairID(ref)]; ok {
			return PairID(ref), true
		}
	}
	if ValidSlug(strings.ToLower(ref)) {
		if id, _, ok := m.copyOwner(Slug(strings.ToLower(ref))); ok {
			return id, true
		}
	}
	return "", false
}

// chainHasPair reports a pair with a step in the open round.
func (s *Service) chainHasPair(id PairID) bool {
	c, err := s.Archive.LoadChain()
	if err != nil || c == nil || c.Terminal() {
		return false
	}
	q, err := s.Online.Store.ReadQueue(online.PhaseReconcile)
	if err != nil || q == nil || q.ID != c.Queue || q.State == online.QueueComplete || q.State == online.QueueCancelled {
		return false
	}
	for _, st := range q.Steps {
		if d, err := DecodeStepData(st.Data); err == nil && d.Pair == id {
			return true
		}
	}
	return false
}

// chainOpen reports a chain that is active or paused.
func (s *Service) chainOpen() bool {
	c, err := s.Archive.LoadChain()
	return err == nil && c != nil && !c.Terminal()
}

// Doctor is the doctor view: the number of pairs and the publish state,
// or a fixed description of the archive problem (no ids, no paths).
func (s *Service) Doctor() (int, i18n.Text, error) {
	if !s.Archive.Exists() {
		return 0, s.publishingDetail(NewManifest()), nil
	}
	m, err := s.Archive.LoadManifest()
	if err != nil {
		return 0, i18n.Text{}, i18n.NewErr(i18n.DoctorArtifactsUnreadable)
	}
	if m == nil {
		m = NewManifest()
	}
	TrimHistory(m, s.cfgArtifacts().HistoryKeep)
	if err := Validate(m, s.Spec, s.cfgArtifacts().HistoryKeep); err != nil {
		return 0, i18n.Text{}, i18n.NewErr(i18n.DoctorArtifactsInvalid)
	}
	missing := 0
	for _, sha := range sortedBlobs(m) {
		if !s.Archive.HasBlob(sha) {
			missing++
		}
	}
	if missing > 0 {
		return len(m.Pairs), i18n.Text{}, i18n.NewErr(i18n.DoctorArtifactsBlobsMissing, missing)
	}
	return len(m.Pairs), s.publishingDetail(m), nil
}

// publishingDetail is the publish state for doctor.
func (s *Service) publishingDetail(m *Manifest) i18n.Text {
	w := s.publishingWord(m)
	if w == "available" && s.Spec.PublishVia == compat.ArtifactsPublishFiles {
		return i18n.Tx(i18n.DoctorArtifactsGrantEachSync)
	}
	if w == "off" {
		return i18n.Tx(i18n.DoctorArtifactsCaptureOnly)
	}
	return i18n.Raw(w)
}

// sortedBlobs are the blobs a manifest references, in order.
func sortedBlobs(m *Manifest) []string {
	ref := referencedBlobs(m)
	out := make([]string, 0, len(ref))
	for sha := range ref {
		out = append(out, sha)
	}
	sort.Strings(out)
	return out
}
