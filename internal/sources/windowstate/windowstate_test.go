package windowstate_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/windowstate"
	"github.com/kazupc/paritydesk/internal/testkit"
)

func rc(t *testing.T, folder string) sources.ReadContext {
	return sources.ReadContext{Folder: folder, Replica: testkit.Replica(1)}
}

func TestWindowState(t *testing.T) {
	root := testkit.NewDataRoot(t)
	src := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-s", Replica: testkit.Replica(1), WindowState: []byte(`{"w":1}`)})
	dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: testkit.Replica(1), WindowState: []byte(`{"w":2}`)})
	a := windowstate.WindowState{}
	srcRC := rc(t, src)
	des, err := a.Read(context.Background(), srcRC)
	if err != nil || des.Items[windowstate.FileName].Value == nil {
		t.Fatalf("read %v %v", des, err)
	}
	cur, _ := a.Read(context.Background(), rc(t, dst))
	if err := os.WriteFile(filepath.Join(src, windowstate.FileName), []byte(`{"w":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := a.Ops(context.Background(), sources.OpsContext{ReadContext: rc(t, dst), Source: &srcRC}, cur, des)
	if err != nil || len(res.Edits) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Kind != model.ChangeSkipped {
		t.Fatalf("changed source: %+v %v", res, err)
	}
	empty := sources.NewSnapshot(model.RowWindowState)
	res, err = a.Ops(context.Background(), sources.OpsContext{ReadContext: rc(t, dst)}, cur, empty)
	if err != nil || len(res.Edits) != 1 || !res.Edits[0].Destructive {
		t.Fatalf("removal: %+v %v", res.Edits, err)
	}
}
