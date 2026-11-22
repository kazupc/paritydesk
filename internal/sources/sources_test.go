package sources_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/testkit"
)

func TestDiscoverReplicas(t *testing.T) {
	root := testkit.NewDataRoot(t)
	folder := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: testkit.Replica(1)})
	for _, junk := range []string{"not-a-uuid/" + testkit.FakeUUID(2), testkit.FakeUUID(1) + "/junk", testkit.FakeUUID(3) + "/" + testkit.FakeUUID(4)} {
		if err := os.MkdirAll(filepath.Join(folder, sources.SessionsRoot, filepath.FromSlash(junk)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := sources.DiscoverReplicas(folder)
	if err != nil || len(got) != 2 || string(got[0].Org) != testkit.FakeUUID(4) || got[1] != testkit.Replica(1) {
		t.Fatalf("replicas %v %v", got, err)
	}
	none, err := sources.DiscoverReplicas(t.TempDir())
	if err != nil || len(none) != 0 {
		t.Fatalf("empty folder: %v %v", none, err)
	}
}

func TestReadFileRefusesNonRegular(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sources.ReadFile(dir, "link.json"); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("symlink read: %v", err)
	}
	if _, present, err := sources.ReadFile(dir, "missing.json"); present || err != nil {
		t.Fatal("missing file")
	}
}

func TestBuilderCreatesParentsOnce(t *testing.T) {
	doc := sources.Doc{Loc: plan.Location{File: "x.json"}, Present: true, Stored: []byte(`{"a":{}}`), Text: []byte(`{"a":{}}`)}
	b := sources.NewBuilder(doc, "row")
	for i := 0; i < 2; i++ {
		if err := b.Ensure(jsonx.Path{"a", "b", "c"}, "i"); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, e := range b.Edits() {
		got = append(got, strings.Join(e.Path, "/")+"="+string(e.Value))
	}
	if strings.Join(got, ",") != "a/b={},a/b/c={}" {
		t.Fatalf("edits %v", got)
	}
	bad := sources.Doc{Loc: plan.Location{File: "x.json"}, Present: true, Stored: []byte(`{"a":1}`), Text: []byte(`{"a":1}`)}
	if err := sources.NewBuilder(bad, "row").Ensure(jsonx.Path{"a", "b"}, ""); err == nil {
		t.Fatal("non-object parent accepted")
	}
}

func TestHashSecrets(t *testing.T) {
	raw := []byte(`{"env":{"A":"` + testkit.Canary + `"},"settings":{"env":{"B":1}},"x":2}`)
	v, secret, err := sources.HashSecrets(raw, []string{"env", "settings.env", "missing.path"}, "test")
	if err != nil || !secret || strings.Contains(string(v), testkit.Canary) || strings.Count(string(v), "sha256:") != 2 {
		t.Fatalf("%s %v %v", v, secret, err)
	}
	same, secret, err := sources.HashSecrets([]byte(`{"x":1}`), []string{"env"}, "test")
	if err != nil || secret || string(same) != `{"x":1}` {
		t.Fatalf("no secrets: %s %v %v", same, secret, err)
	}
}
