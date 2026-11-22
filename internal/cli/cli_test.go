package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	code := Execute(context.Background(), []string{"version"}, strings.NewReader(""), &out, &errb)
	if code != 0 || !strings.HasPrefix(out.String(), "paritydesk ") {
		t.Fatalf("code %d out %q err %q", code, out.String(), errb.String())
	}
}

func TestUnknownCommandIsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Execute(context.Background(), []string{"nope"}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("code %d", code)
	}
}

func TestPendingCommandIsInternal(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Execute(context.Background(), []string{"status"}, strings.NewReader(""), &out, &errb); code != 70 {
		t.Fatalf("code %d", code)
	}
}

func TestFlagsAfterArgsAndJSON(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Execute(context.Background(), []string{"version", "--json"}, strings.NewReader(""), &out, &errb); code != 0 || !strings.Contains(out.String(), `"version"`) {
		t.Fatalf("code %d out %q err %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := Execute(context.Background(), []string{"--json", "version"}, strings.NewReader(""), &out, &errb); code != 0 || !strings.Contains(out.String(), `"version"`) {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestArgCountIsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Execute(context.Background(), []string{"profile", "adopt", "only-one"}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("code %d", code)
	}
}
