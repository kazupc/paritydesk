package e2e

import (
	"os"
	"testing"
)

func TestCheckTarget(t *testing.T) {
	const real = "/Users/test"
	cases := []struct {
		name, bundleID, home string
		ok                   bool
	}{
		{"stub in temp home", "dev.paritydesk.stubapp", "/private/tmp/e2e-home", true},
		{"stub in a work folder", "dev.paritydesk.stubapp", "/Users/test/work/_temp/e2e-home", true},
		{"real bundle id", RealBundleID, "/private/tmp/e2e-home", false},
		{"vendor prefix", "com.anthropic.other", "/private/tmp/e2e-home", false},
		{"vendor prefix any case", "COM.Anthropic.ClaudeForDesktop", "/private/tmp/e2e-home", false},
		{"empty bundle id", "", "/private/tmp/e2e-home", false},
		{"real home", "dev.paritydesk.stubapp", "/Users/test", false},
		{"real home with slash", "dev.paritydesk.stubapp", "/Users/test/", false},
		{"real home unclean", "dev.paritydesk.stubapp", "/Users/test/work/..", false},
		{"parent of real home", "dev.paritydesk.stubapp", "/Users", false},
		{"root", "dev.paritydesk.stubapp", "/", false},
		{"under real Library", "dev.paritydesk.stubapp", "/Users/test/Library/tmp", false},
		{"real Library", "dev.paritydesk.stubapp", "/Users/test/Library", false},
		{"relative home", "dev.paritydesk.stubapp", "tmp/home", false},
		{"empty home", "dev.paritydesk.stubapp", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckTarget(c.bundleID, c.home, real)
			if (err == nil) != c.ok {
				t.Fatalf("CheckTarget(%q, %q) = %v, want ok=%v", c.bundleID, c.home, err, c.ok)
			}
		})
	}
	if err := CheckTarget("dev.paritydesk.stubapp", "/private/tmp/e2e-home", ""); err == nil {
		t.Fatal("an unknown real home must be refused")
	}
}

func TestRealHomeIgnoresHOME(t *testing.T) {
	before, err := RealHome()
	if err != nil {
		t.Skipf("no user database entry: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	after, err := RealHome()
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("RealHome followed $HOME")
	}
	if after == os.Getenv("HOME") {
		t.Fatal("RealHome returned the temporary HOME")
	}
}
