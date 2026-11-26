package artifacts

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// synced is a harness after one artifact was synced on home and work.
func synced(t *testing.T) (*harness, PairID) {
	t.Helper()
	h := newHarness(t)
	a := h.servers["home"].add("Notes", page(2))
	h.sync("apply")
	h.switchTo("work")
	h.sync("apply")
	return h, PairIDFor(a.slug)
}

func TestStatusAndPlan(t *testing.T) {
	h, id := synced(t)
	st, err := h.svc.Status(StatusInput{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Counts.Pairs != 1 || st.Counts.InStep != 1 || st.Publishing != "available" || st.Chain == nil || len(st.Pairs) != 1 {
		t.Fatalf("%+v", st)
	}
	if st.Pairs[0].Title != "" || st.Titles[id] != "Notes" {
		t.Fatal("titles in JSON without --titles")
	}
	if st, _ := h.svc.Status(StatusInput{Titles: true}); st.Pairs[0].Title != "Notes" {
		t.Fatal("--titles")
	}
	if !strings.HasPrefix(st.Lines[len(i18n.Strings(i18n.English, st.Lines))-1].English(), "1 artifact(s): 1 in step") {
		t.Fatalf("summary %q", i18n.Strings(i18n.English, st.Lines))
	}
	p, err := h.svc.Plan("home")
	if err != nil || len(p.Publish)+len(p.Create)+len(p.Read) != 0 {
		t.Fatalf("%+v %v", p, err)
	}
	// An edit on work makes home due.
	srv := h.servers["work"]
	srv.edit(srv.sorted()[0], page(5))
	h.sync("apply")
	p, _ = h.svc.Plan("home")
	if len(p.Publish) != 1 || p.Lines[0].English() != i18n.En(i18n.ArtifactsPlanDue, 1, "home") {
		t.Fatalf("%+v", p)
	}
	if n, _ := h.svc.Due("home", repHome); n != 1 {
		t.Fatalf("due %d", n)
	}
	if e, ok, _ := h.svc.LastSyncEpoch("work"); !ok || e != 2 {
		t.Fatalf("last sync epoch %d %v", e, ok)
	}
	if n, word, err := h.svc.Doctor(); err != nil || n != 1 || !strings.HasPrefix(word.English(), "available") {
		t.Fatalf("doctor %d %q %v", n, word, err)
	}
}

func TestDecisions(t *testing.T) {
	h, id := synced(t)
	ctx := context.Background()
	no := func(string) error { return pderr.New(pderr.Declined, "confirm", "no") }
	if _, err := h.svc.Allow(ctx, AllowInput{Ref: string(id), What: "shared"}, no); pderr.CodeOf(err) != pderr.Declined {
		t.Fatalf("declined: %v", err)
	}
	if h.manifest().Pairs[id].Flags.AllowShared {
		t.Fatal("changed after a declined question")
	}
	if _, err := h.svc.Allow(ctx, AllowInput{Ref: string(id), What: "public"}, yes); err != nil {
		t.Fatal(err)
	}
	if f := h.manifest().Pairs[id].Flags; !f.AllowShared || !f.AllowPublic {
		t.Fatalf("%+v", f)
	}
	// By a copy's slug.
	slug := string(h.manifest().Pairs[id].Copies["home"].Slug)
	if _, err := h.svc.Skip(ctx, slug); err != nil || h.manifest().Pairs[id].Flags.Skipped != "user" {
		t.Fatalf("skip: %v", err)
	}
	if _, err := h.svc.Allow(ctx, AllowInput{Ref: string(id), What: "resume"}, yes); err != nil || h.manifest().Pairs[id].Flags.Skipped != "" {
		t.Fatalf("resume: %v", err)
	}
	if _, err := h.svc.Allow(ctx, AllowInput{Ref: string(id), What: "restore", Profile: "home"}, yes); err != nil ||
		!slices.Contains(h.manifest().Pairs[id].Flags.Restore, "home") {
		t.Fatalf("restore: %v", err)
	}
	// rehome: only a copy in another organization.
	if _, err := h.svc.Allow(ctx, AllowInput{Ref: string(id), What: "rehome", Profile: "home"}, yes); pderr.CodeOf(err) != pderr.Usage {
		t.Fatalf("rehome of a current copy: %v", err)
	}
	m := h.manifest()
	m.Pairs[id].Copies["home"].Replica = repWork
	_ = h.svc.Archive.CommitManifest(m, baseTime)
	out, err := h.svc.Allow(ctx, AllowInput{Ref: string(id), What: "rehome", Profile: "home"}, yes)
	if err != nil || h.manifest().Pairs[id].Copies["home"] != nil || out.Slug != "0000\u2026" {
		t.Fatalf("rehome: %+v %v", out, err)
	}
	if _, err := h.svc.Allow(ctx, AllowInput{Ref: "ffffffffffffffff", What: "shared"}, yes); pderr.CodeOf(err) != pderr.Usage {
		t.Fatalf("unknown pair: %v", err)
	}
	if _, err := h.svc.Forget(ctx, string(id), yes); err != nil {
		t.Fatal(err)
	}
	m = h.manifest()
	if m.Pairs[id] != nil || m.Forgotten[id] == nil {
		t.Fatal("not forgotten")
	}
	if st, _ := h.svc.Status(StatusInput{All: true}); len(st.Forgotten) != 1 {
		t.Fatal("status --all")
	}
}

func TestForgetDuringChain(t *testing.T) {
	h, id := synced(t)
	srv := h.servers["work"]
	srv.edit(srv.sorted()[0], page(7))
	ctx := context.Background()
	tok := ""
	// Run until a round with a step of the pair is open.
	for i := 0; i < 20; i++ {
		out, err := h.svc.Next(ctx, NextArgs{Session: testSession, Token: tok})
		if err != nil {
			t.Fatal(err)
		}
		if out.Token != "" {
			tok = out.Token
		}
		if h.svc.chainHasPair(id) {
			break
		}
		for _, c := range out.Calls {
			h.record(c, srv.call(c.Tool, c.Input))
		}
	}
	if !h.svc.chainHasPair(id) {
		t.Fatal("no round with the pair")
	}
	out, err := h.svc.Forget(ctx, string(id), yes)
	if err != nil || out.State != StateBlocked || out.Lines[0].English() != i18n.T(i18n.ArtifactsForgetChainActive) {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestRenameAndRemoveProfile(t *testing.T) {
	h, id := synced(t)
	ctx := context.Background()
	gone := PairID("00000000000000ff")
	if err := h.svc.mutate(ctx, func(m *Manifest) error {
		f := &m.Pairs[id].Flags
		f.AllowCreate, f.Restore, f.TitleTaken = []ids.Alias{"home"}, []ids.Alias{"home"}, []ids.Alias{"home", "work"}
		fp := m.Clone().Pairs[id]
		for a, c := range fp.Copies {
			cc := *c
			cc.Slug = Slug(strings.Replace(string(c.Slug), "-4000-", "-4fff-", 1))
			fp.Copies[a] = &cc
		}
		m.Forgotten = map[PairID]*Pair{gone: fp}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RenameProfile(ctx, "home", "house"); err != nil {
		t.Fatal(err)
	}
	m := h.manifest()
	if m.Pairs[id].Copies["house"] == nil || m.Pairs[id].Copies["home"] != nil || m.Profiles["house"] == nil {
		t.Fatalf("%+v", m.Pairs[id].Copies)
	}
	f := m.Pairs[id].Flags
	if !slices.Equal(f.AllowCreate, []ids.Alias{"house"}) || !slices.Equal(f.Restore, []ids.Alias{"house"}) ||
		!slices.Equal(f.TitleTaken, []ids.Alias{"house", "work"}) || m.Pairs[id].Content.SourceProfile != "house" {
		t.Fatalf("decisions not renamed: %+v %+v", f, m.Pairs[id].Content)
	}
	if fp := m.Forgotten[gone]; fp == nil || fp.Copies["house"] == nil || fp.Copies["home"] != nil {
		t.Fatalf("forgotten pair not renamed: %+v", fp)
	}
	if inv, _ := h.svc.Archive.LoadInventory("house"); inv == nil {
		t.Fatal("inventory not moved")
	}
	if err := h.svc.RemoveProfile(ctx, "house"); err != nil {
		t.Fatal(err)
	}
	m = h.manifest()
	if m.Profiles["house"] != nil || m.Pairs[id].Copies["house"] == nil {
		t.Fatal("remove must keep the copy (orphaned) and drop the record")
	}
	// A later profile under the same alias inherits no decision, and the
	// content no longer names the removed profile as its source.
	f = m.Pairs[id].Flags
	if len(f.AllowCreate) != 0 || len(f.Restore) != 0 || !slices.Equal(f.TitleTaken, []ids.Alias{"work"}) ||
		m.Pairs[id].Content.SourceProfile != "" || m.Pairs[id].Content.SHA256 == "" {
		t.Fatalf("remove: %+v %+v", f, m.Pairs[id].Content)
	}
	st, _ := h.svc.Status(StatusInput{})
	found := false
	for _, c := range st.Pairs[0].Copies {
		found = found || (c.Profile == "house" && c.Status == "orphaned")
	}
	if !found {
		t.Fatalf("orphaned copy not reported: %+v", st.Pairs[0].Copies)
	}
}

func TestWriteDueAndLockBusy(t *testing.T) {
	h, _ := synced(t)
	ctx := context.Background()
	if err := h.svc.WriteDue(ctx, "home", repHome, 9); err != nil {
		t.Fatal(err)
	}
	if d := h.svc.Archive.LoadDue(); d == nil || d.Epoch != 9 || d.Profile != "home" {
		t.Fatalf("%+v", d)
	}
	h.svc.Archive.Lock = func(context.Context, time.Duration) (func(), error) {
		return nil, pderr.New(pderr.RunLockBusy, "lock", "busy")
	}
	err := h.svc.WriteDue(ctx, "home", repHome, 10)
	if pderr.CodeOf(err) != pderr.RunLockBusy || !strings.Contains(err.Error(), i18n.T(i18n.ArtifactsErrBusy)) {
		t.Fatalf("busy: %v", err)
	}
	if _, err := h.svc.Skip(ctx, "x"); err == nil {
		t.Fatal("skip of an unknown pair")
	}
	if !errors.Is(errors.New("x"), errors.New("x")) == false {
		return
	}
}

// Lowering artifacts.history_keep must not make the archive invalid: the
// history is trimmed when the manifest is loaded and written shorter by
// the next change.
func TestLowerHistoryKeep(t *testing.T) {
	h, id := synced(t)
	ctx := context.Background()
	h.svc.Config.Artifacts.HistoryKeep = 10
	if err := h.svc.mutate(ctx, func(m *Manifest) error {
		p := m.Pairs[id]
		for i := 0; i < 5; i++ {
			p.History = append(p.History, HistoryEntry{SHA256: p.Content.SHA256, SavedAt: baseTime, SourceProfile: "home", Reason: HistReplaced})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.svc.Config.Artifacts.HistoryKeep = 2
	if _, err := h.svc.Status(StatusInput{}); err != nil {
		t.Fatalf("status after lowering history_keep: %v", err)
	}
	if _, _, err := h.svc.Doctor(); err != nil {
		t.Fatalf("doctor after lowering history_keep: %v", err)
	}
	if _, err := h.svc.Skip(ctx, string(id)); err != nil {
		t.Fatalf("skip after lowering history_keep: %v", err)
	}
	if n := len(h.manifest().Pairs[id].History); n != 2 {
		t.Fatalf("history kept %d entries, want 2", n)
	}
}

// With publish_via = "off" (capture only) nothing is due: no due.json
// count, no plan line, no status count (8.6, 5.8).
func TestDueWithPublishOff(t *testing.T) {
	h, _ := synced(t)
	srv := h.servers["work"]
	srv.edit(srv.sorted()[0], page(5))
	h.sync("apply")
	if n, _ := h.svc.Due("home", repHome); n != 1 {
		t.Fatalf("due with publishing on %d", n)
	}
	h.svc.Spec.PublishVia = compat.ArtifactsPublishOff
	if n, err := h.svc.Due("home", repHome); err != nil || n != 0 {
		t.Fatalf("due with publish_via off %d %v", n, err)
	}
	if p, _ := h.svc.Plan("home"); len(i18n.Strings(i18n.English, p.Lines)) != 0 {
		t.Fatalf("plan lines with publish_via off %q", i18n.Strings(i18n.English, p.Lines))
	}
	if st, _ := h.svc.Status(StatusInput{}); st.Counts.Due != 0 {
		t.Fatalf("status due with publish_via off %d", st.Counts.Due)
	}
	if err := h.svc.WriteDue(context.Background(), "home", repHome, 9); err != nil {
		t.Fatal(err)
	}
	if d := h.svc.Archive.LoadDue(); d == nil || d.Count != 0 {
		t.Fatalf("due.json %+v", d)
	}
}
