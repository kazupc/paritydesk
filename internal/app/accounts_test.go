package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

func TestAccountChanges(t *testing.T) {
	ctx := t.Context()
	sig := sig7x(t)
	l := newTestLog(t)
	t1, t2 := busyT0.Add(time.Minute), busyT0.Add(2*time.Minute)
	l.append(
		at(busyT0, "[other] start"),
		at(t1, accountChangedMsg(acctP, acctQ)),
		at(t1, accountChangedMsg(acctP, acctP)), // From == To
		at(t1, accountChangedMsg(acctP, "not-a-uuid")),
		at(t1, "[account] Login-state transition (loggedOut: true → false, uuid: <none> → "+acctQ+"), clearing oauth cache"),
		at(t1, "[account] Login-state transition (loggedOut: false → false, uuid: "+acctP+" → "+acctQ+"), preserving oauth cache (same account uuid)"),
		at(t1, "[account] Login-state transition (loggedOut: false → true, uuid: "+acctP+" → "+acctQ+"), clearing oauth cache"),
		at(t2, accountChangedMsg(acctQ, acctP)),
	)
	got, truncated, err := l.log().AccountChanges(ctx, sig, time.Time{})
	if err != nil || truncated {
		t.Fatalf("truncated %v err %v", truncated, err)
	}
	want := []AccountChange{{From: acctP, To: acctQ, At: t1}, {From: acctQ, To: acctP, At: t2}}
	if len(got) != 2 || got[0].From != want[0].From || got[0].To != want[0].To || !got[0].At.Equal(t1) ||
		got[1].From != want[1].From || !got[1].At.Equal(t2) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// since limits the window.
	got, _, err = l.log().AccountChanges(ctx, sig, t2)
	if err != nil || len(got) != 1 || got[0].To != acctP {
		t.Fatalf("since: %+v %v", got, err)
	}
	if _, _, err := l.log().AccountChanges(ctx, compat.Signals{}, time.Time{}); !errors.Is(err, ErrSignalUnknown) {
		t.Fatalf("empty pattern: %v", err)
	}
	// At most 256 changes, the newest.
	l2 := newTestLog(t)
	var lines []string
	for i := 0; i < maxAccountChanges+10; i++ {
		from, to := acctP, acctQ
		if i%2 == 1 {
			from, to = acctQ, acctP
		}
		lines = append(lines, at(busyT0.Add(time.Duration(i)*time.Second), accountChangedMsg(from, to)))
	}
	l2.append(lines...)
	got, _, err = l2.log().AccountChanges(ctx, sig, time.Time{})
	if err != nil || len(got) != maxAccountChanges || !got[len(got)-1].At.Equal(busyT0.Add(time.Duration(maxAccountChanges+9)*time.Second)) {
		t.Fatalf("cap: %d %v", len(got), err)
	}
	// A small budget gives truncated.
	lg := l2.log()
	lg.budget = 200
	if _, truncated, err := lg.AccountChanges(ctx, sig, busyT0.Add(-time.Hour)); err != nil || !truncated {
		t.Fatalf("budget: %v %v", truncated, err)
	}
}

func TestAccountOutOfOrderGuard(t *testing.T) {
	ctx := t.Context()
	sig := sig7x(t)
	l := newTestLog(t)
	now := busyT0.Add(3 * time.Hour)
	l.append(
		at(now, "[other] latest line"),
		at(now.Add(-2*time.Hour), accountChangedMsg(acctP, acctQ)),    // forged with a stale time: ignored
		at(now.Add(-60*time.Minute), accountChangedMsg(acctQ, acctP)), // a DST-like step back: kept
	)
	got, _, err := l.log().AccountChanges(ctx, sig, time.Time{})
	if err != nil || len(got) != 1 || got[0].From != acctQ {
		t.Fatalf("got %+v %v", got, err)
	}
	// The guard works per file: an older file's lines count.
	l.rotate()
	l.append(at(now.Add(time.Hour), "[other] new file"))
	got, _, err = l.log().AccountChanges(ctx, sig, time.Time{})
	if err != nil || len(got) != 1 {
		t.Fatalf("after rotation: %+v %v", got, err)
	}
	// The same guard applies to the active org.
	l2 := newTestLog(t)
	l2.append(at(now, activeOrgMsg(acctP, orgX)), at(now.Add(-2*time.Hour), activeOrgMsg(acctP, orgY)))
	if org, found, err := l2.log().ActiveOrg(ctx, sig, ids.AccountID(acctP), time.Time{}); err != nil || !found || org != orgX {
		t.Fatalf("org %q %v %v", org, found, err)
	}
}

func TestLastReplica(t *testing.T) {
	ctx := t.Context()
	sig := sig7x(t)
	rep := func(a, o string) model.Replica { return model.Replica{Account: ids.AccountID(a), Org: ids.OrgID(o)} }
	start := busyT0.Add(time.Hour)
	hint1 := "[connector-owner] account changed — disconnecting 2 stored built-in connector(s)"
	hint2 := "Cannot get base query config: oauth failed (not_logged_in detail=account changed mid-flow)"
	tests := []struct {
		name  string
		lines []string
		since time.Time
		known bool
		want  model.Replica
	}{
		{"newest line", []string{at(start, activeOrgMsg(acctP, orgX)), at(start.Add(time.Second), activeOrgMsg(acctQ, orgY))},
			start, true, rep(acctQ, orgY)},
		{"none", []string{at(start, "[other] x")}, start, false, model.Replica{}},
		{"before since", []string{at(start.Add(-time.Hour), activeOrgMsg(acctP, orgX))}, start, false, model.Replica{}},
		{"zero since reads the tail", []string{at(start.Add(-time.Hour), activeOrgMsg(acctP, orgX))}, time.Time{}, true, rep(acctP, orgX)},
		{"account change after it", []string{at(start, activeOrgMsg(acctP, orgX)), at(start.Add(time.Second), accountChangedMsg(acctP, acctQ))},
			start, false, model.Replica{}},
		{"hint 1 after it", []string{at(start, activeOrgMsg(acctP, orgX)), at(start.Add(time.Second), hint1)}, start, false, model.Replica{}},
		{"hint 2 after it", []string{at(start, activeOrgMsg(acctP, orgX)), at(start.Add(time.Second), hint2)}, start, false, model.Replica{}},
		{"hint before it", []string{at(start, hint1), at(start.Add(time.Second), activeOrgMsg(acctP, orgX))}, start, true, rep(acctP, orgX)},
		{"hint on a continuation line", []string{at(start, activeOrgMsg(acctP, orgX)), hint1 + "\n"}, start, true, rep(acctP, orgX)},
		{"invalid ids", []string{at(start, activeOrgMsg(acctP, orgX)), at(start.Add(time.Second), activeOrgMsg(acctP, "NOT"))},
			start, true, rep(acctP, orgX)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := newTestLog(t)
			l.append(tc.lines...)
			got, known, err := l.log().LastReplica(ctx, sig, tc.since)
			if err != nil || known != tc.known || got != tc.want {
				t.Fatalf("got %+v %v %v, want %+v %v", got, known, err, tc.want, tc.known)
			}
		})
	}
	t.Run("across rotation", func(t *testing.T) {
		l := newTestLog(t)
		l.append(at(start, activeOrgMsg(acctP, orgX)))
		l.rotate()
		l.append(at(start.Add(time.Minute), "[other] x"))
		got, known, err := l.log().LastReplica(ctx, sig, start)
		if err != nil || !known || got != rep(acctP, orgX) {
			t.Fatalf("got %+v %v %v", got, known, err)
		}
	})
	if _, _, err := newTestLog(t).log().LastReplica(ctx, compat.Signals{}, start); !errors.Is(err, ErrSignalUnknown) {
		t.Fatalf("empty pattern: %v", err)
	}
	// The pattern the regexp sees is never in an error.
	bad := sig
	bad.AccountChangedHint = []string{"(secret-pattern"}
	if _, _, err := newTestLog(t).log().LastReplica(ctx, bad, start); err == nil || strings.Contains(err.Error(), "secret-pattern") {
		t.Fatalf("bad hint: %v", err)
	}
}
