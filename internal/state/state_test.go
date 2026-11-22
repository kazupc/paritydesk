package state

import (
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
)

func TestBaseKeyIncludesReplica(t *testing.T) {
	a := BaseKey{Alias: "work", Replica: model.Replica{Account: "acct", Org: "org1"}, Row: model.RowSessions}
	b := a
	b.Replica.Org = "org2"
	if a.String() == b.String() {
		t.Fatal("bases of two orgs share a key")
	}
	if a.String() != "work/acct/org1/sessions" {
		t.Fatalf("got %q", a.String())
	}
	if ContestedKey(a, "order") != "work/acct/org1/sessions/order" {
		t.Fatal(ContestedKey(a, "order"))
	}
}

func TestLockFileKeyedToDataRoot(t *testing.T) {
	a := LockFile("/s", "/data")
	if a != LockFile("/s", "/data/") || a == LockFile("/s", "/other") {
		t.Fatal("lock file not keyed to the cleaned data root")
	}
	if !strings.HasPrefix(a, "/s/locks/") || !strings.HasSuffix(a, ".lock") {
		t.Fatal(a)
	}
}
