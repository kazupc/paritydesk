package render

import (
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/i18n"
)

func TestPinsLineFlags(t *testing.T) {
	for _, c := range []struct {
		source, reason  string
		approx          bool
		unread, preview bool
	}{
		{"idb", "", false, false, false},
		{"mirror", "", true, false, false},
		{"none", "", false, false, false},
		{"none", "unsupported", false, true, false},
		{"none", "unsupported", true, true, false},
		{"none", "torn", true, false, true},
		{"none", "pending", true, false, true},
		{"none", "unclean", false, true, false},
	} {
		u, p := PinsLineFlags(c.source, c.reason, c.approx)
		if u != c.unread || p != c.preview {
			t.Errorf("%+v: %v %v", c, u, p)
		}
	}
}

func TestOnlinePinsLines(t *testing.T) {
	got := strings.Join(onlineLines(i18n.English, PlanView{Online: &OnlinePlanView{PinsAdd: 1, PinsPreviewUnavailable: true}}), "\n")
	if !strings.Contains(got, "pin(s)") || !strings.Contains(got, "not shown now") {
		t.Fatalf("lines %q", got)
	}
	got = strings.Join(onlineLines(i18n.English, PlanView{Online: &OnlinePlanView{PinsUnreadable: true}}), "\n")
	if !strings.Contains(got, "not carried this time") {
		t.Fatalf("lines %q", got)
	}
}
