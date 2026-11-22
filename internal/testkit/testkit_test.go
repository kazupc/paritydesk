package testkit_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/testkit"
)

var (
	uuidRE = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	fakeRE = regexp.MustCompile(`^00000000-0000-4000-8000-[0-9a-f]{12}$`)
	mailRE = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
)

func checkIDs(t *testing.T, where, text string) {
	t.Helper()
	for _, u := range uuidRE.FindAllString(text, -1) {
		if !fakeRE.MatchString(u) {
			t.Errorf("%s holds a non-fake uuid", where)
		}
	}
	if mailRE.MatchString(text) {
		t.Errorf("%s holds an email-like string", where)
	}
}

func TestFixtureLevelDBReadableAndFakeIDsOnly(t *testing.T) {
	root := testkit.NewDataRoot(t)
	folder := root.AddFolder(t, testkit.Rich("Claude", 1))
	dir := filepath.Join(folder, testkit.LevelDBRel)
	r, err := testkit.Opener(t).OpenSnapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n := 0
	err = r.Scan(localstorage.KeyPrefix(), func(k, v []byte) error {
		name, err := localstorage.ParseKey(k)
		if err != nil {
			return err
		}
		text, err := localstorage.DecodeText(v)
		if err != nil {
			return err
		}
		if jsonx.Validate([]byte(text)) != nil {
			t.Errorf("item %s is not JSON", name)
		}
		checkIDs(t, "LS item "+name, name+text)
		n++
		return nil
	})
	if err != nil || n < 6 {
		t.Fatalf("scanned %d items: %v", n, err)
	}
	if _, found, err := r.Get(localstorage.MetaKey); err != nil || !found {
		t.Fatal("META key missing")
	}
	if _, err := os.Stat(filepath.Join(dir, "LOCK")); err != nil {
		t.Fatal("fixture should be a real goleveldb directory")
	}
	if _, err := testkit.Opener(t).OpenWritable(context.Background(), dir); err == nil {
		t.Fatal("test opener must not open for writing")
	}
	for rel := range root.Tree(t) {
		b, err := os.ReadFile(filepath.Join(root.Dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		checkIDs(t, rel, rel+string(b))
	}
}

func TestCanaryOnlyInTokenFields(t *testing.T) {
	root := testkit.NewDataRoot(t)
	folder := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: testkit.Replica(1), WithToken: true})
	b, err := os.ReadFile(filepath.Join(folder, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, found, err := jsonx.TopLevelString(b, "oauth:tokenCacheV2")
	if err != nil || !found || tok != testkit.Canary {
		t.Fatalf("canary not planted: %v", err)
	}
	acct, _, _ := jsonx.TopLevelString(b, "lastKnownAccountUuid")
	if acct != string(testkit.Replica(1).Account) {
		t.Fatalf("account %s", acct)
	}
}

func TestTreeAndApply(t *testing.T) {
	root := testkit.NewDataRoot(t)
	folder := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: testkit.Replica(1), GroupOrder: []string{"a"}})
	before := root.Tree(t)
	cfg := "Claude/claude_desktop_config.json"
	if len(before[cfg]) != 64 {
		t.Fatalf("tree %v", before)
	}
	orig, _ := os.ReadFile(filepath.Join(folder, "claude_desktop_config.json"))
	testkit.Apply(t, folder, []plan.Edit{
		{Loc: plan.Location{File: "claude_desktop_config.json"}, Row: model.RowAppPrefs, Kind: plan.EditSet,
			Path: jsonx.Path{"preferences", "sidebarMode"}, Value: []byte(`"x"`), BaseHash: model.HashBytes(orig)},
		{Loc: plan.Location{LSKey: "new-item"}, Row: model.RowAppPrefs, Kind: plan.EditReplace, Value: []byte(`{"v":1}`)},
	})
	after := root.Tree(t)
	if after[cfg] == before[cfg] {
		t.Fatal("config not edited")
	}
	if got := testkit.ReadLS(t, folder)["new-item"]; got != `{"v":1}` {
		t.Fatalf("LS item %q", got)
	}
	ls := testkit.ReadLS(t, folder)
	if !strings.Contains(ls["LSS-persisted.code-projects-order."+string(testkit.Replica(1).Account)], `"timestamp":1700000000000`) {
		t.Fatal("envelope")
	}
}
