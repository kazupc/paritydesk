package plan

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

const (
	scrRoot = "scratch-workspaces"
	scrAcct = "00000000-0000-4000-8000-00000000000a"
	scrOrg  = "00000000-0000-4000-8000-00000000000b"
	scrWS   = scrRoot + "/" + scrAcct + "/" + scrOrg + "/scratch-2026-01-02-0a0b0c"
	scrKey  = scrAcct + "/" + scrOrg + "/scratch-2026-01-02-0a0b0c"
)

var scrLeafRE = regexp.MustCompile(`^scratch-[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9a-f]{6}$`)

func h64(c string) string { return strings.Repeat(c, 64) }

func scrCopy(rel, pre, post string) Edit {
	return Edit{Loc: Location{File: scrWS + "/" + rel}, Row: model.RowScratch, Item: scrKey + "/" + rel, Kind: EditCopy,
		Mode: 0o644, BaseHash: pre, Copy: &CopySource{Side: SideLeaving, Path: scrWS + "/" + rel, Hash: post, Size: 10, MTimeNs: 1}}
}

func TestComposeScratchWithoutReading(t *testing.T) {
	edits := []Edit{
		{Loc: Location{File: scrRoot}, Row: model.RowScratch, Kind: EditMkdir, Mode: 0o755},
		{Loc: Location{File: scrWS + "/d"}, Row: model.RowScratch, Item: scrKey + "/d/", Kind: EditMkdir, Mode: 0o700},
		scrCopy("f", h64("1"), h64("2")),
		{Loc: Location{File: scrWS + "/ln"}, Row: model.RowScratch, Item: scrKey + "/ln", Kind: EditSymlink, Link: "f"},
		{Loc: Location{File: scrWS + "/old"}, Row: model.RowScratch, Item: scrKey + "/old", Kind: EditRemove, BaseHash: h64("3"), Destructive: true},
	}
	src := func(Location) ([]byte, bool, error) { return nil, false, errors.New("must not read") }
	ops, err := Compose(edits, src)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		kind      Kind
		pre, post string
	}{
		{OpMkdir, "", DirHash(0o755)}, {OpMkdir, "", DirHash(0o700)}, {OpCopyFile, h64("1"), h64("2")},
		{OpSymlink, "", LinkHash("f")}, {OpQuarantine, h64("3"), ""},
	}
	if len(ops) != len(want) {
		t.Fatalf("ops %+v", ops)
	}
	for i, w := range want {
		if ops[i].Kind != w.kind || ops[i].PreHash != w.pre || ops[i].PostHash != w.post {
			t.Errorf("op %d = %+v, want %+v", i, ops[i], w)
		}
	}
	if !ops[4].Destructive || ops[3].Link != "f" || ops[2].Copy == nil || ops[2].Copy.Hash != h64("2") {
		t.Errorf("op fields %+v", ops)
	}
	// Two edits on one location: Internal.
	_, err = Compose([]Edit{scrCopy("f", "", h64("2")), scrCopy("f", "", h64("4"))}, src)
	if pderr.CodeOf(err) != pderr.Internal {
		t.Errorf("two edits on one location: %v", err)
	}
}

func TestPlanHashWithoutScratchKindsUnchanged(t *testing.T) {
	// The golden hash of a plan without scratch kinds: the new members
	// are omitted from the hashed form.
	p := &Plan{Version: Version, From: "work", Target: Target{Alias: "home"},
		Ops:     []Op{{Kind: OpWriteFile, Row: model.RowSessions, Path: "x.json", PreHash: "", PostHash: h64("a")}},
		Changes: []model.Change{{Row: model.RowSessions, Kind: model.ChangeAdded, Key: "k"}}}
	h := hashed{Version: p.Version, From: p.From, To: p.Target.Alias, Ops: []hashedOp{{Kind: OpWriteFile, Row: model.RowSessions, Path: "x.json", Post: h64("a")}}, Changes: p.Changes}
	b, _ := json.Marshal(h)
	if strings.Contains(string(b), `"c":`) || strings.Contains(string(b), `"l":`) {
		t.Fatalf("hashed form names the scratch members: %s", b)
	}
	// Golden: the hash this plan had before the scratch kinds existed.
	if got := p.Hash(); got != "2596627a5ccf0ae4d295a7616e04696c6ce86c53138cfbf78d09025455bdb43f" || got != model.HashBytes(b) {
		t.Fatalf("plan hash changed: %s", got)
	}
	q := *p
	q.Ops = []Op{{Kind: OpCopyFile, Row: model.RowScratch, Path: scrWS + "/f", PostHash: h64("a"), Copy: &CopySource{Side: SideLeaving, Path: scrWS + "/f", Hash: h64("a")}}}
	r := q
	r.Ops = []Op{{Kind: OpCopyFile, Row: model.RowScratch, Path: scrWS + "/f", PostHash: h64("a"), Copy: &CopySource{Side: SideEntering, Path: scrWS + "/g", Hash: h64("a")}}}
	if q.Hash() == r.Hash() {
		t.Error("the copy source is not covered by the plan hash")
	}
}

// scrValid is a well-formed scratch plan.
func scrValid() ValidateInput {
	return ValidateInput{
		Plan: &Plan{Version: Version,
			Ops: []Op{
				{Kind: OpMkdir, Row: model.RowScratch, Path: scrRoot, Mode: 0o755, PostHash: DirHash(0o755)},
				{Kind: OpMkdir, Row: model.RowScratch, Path: scrRoot + "/" + scrAcct, Mode: 0o755, PostHash: DirHash(0o755)},
				{Kind: OpCopyFile, Row: model.RowScratch, Path: scrWS + "/notes.tmp", Mode: 0o644, PostHash: h64("2"),
					Copy: &CopySource{Side: SideLeaving, Path: scrWS + "/notes.tmp", Hash: h64("2"), Size: 10}, Items: []string{scrKey + "/notes.tmp"}},
				{Kind: OpSymlink, Row: model.RowScratch, Path: scrWS + "/d/ln", Link: "../f", PostHash: LinkHash("../f")},
				{Kind: OpQuarantine, Row: model.RowScratch, Path: scrWS + "/old", PreHash: h64("3"), Destructive: true, Items: []string{scrKey + "/old"}},
			},
			Changes: []model.Change{{Row: model.RowScratch, Kind: model.ChangeDeleted, Key: scrKey + "/old"}},
		},
		Before:      map[model.Category]int{model.CatScratch: 100},
		Limits:      Limits{MaxDeleteFraction: 0.2, MaxDeleteMin: 2, PerCategory: map[model.Category]CategoryLimits{model.CatScratch: {MaxDeleteFraction: 0.5, MaxDeleteMin: 50}}},
		ScratchRoot: scrRoot, ScratchLeaf: scrLeafRE, MaxCopyBytes: 100,
		TmpRecoveryGlobs: []string{"local_*.json.tmp"},
	}
}

func TestValidateScratch(t *testing.T) {
	if err := Validate(scrValid()); err != nil {
		t.Fatalf("well-formed scratch plan refused: %v", err)
	}
	op := func(i int, f func(*Op)) func(*ValidateInput) {
		return func(in *ValidateInput) { f(&in.Plan.Ops[i]) }
	}
	tests := []struct {
		name string
		mod  func(*ValidateInput)
	}{
		{"scratch kind on another row", op(2, func(o *Op) { o.Row = model.RowSessions })},
		{"write-file on row scratch", op(2, func(o *Op) {
			o.Kind, o.Content, o.PostHash, o.Copy = OpWriteFile, []byte("x"), hashOf([]byte("x")), nil
		})},
		{"outside the root", op(2, func(o *Op) { o.Path = "elsewhere/x" })},
		{"bad account segment", op(2, func(o *Op) { o.Path = scrRoot + "/nope/" + scrOrg + "/scratch-2026-01-02-0a0b0c/x" })},
		{"bad leaf", op(2, func(o *Op) { o.Path = scrRoot + "/" + scrAcct + "/" + scrOrg + "/other/x" })},
		{"copy above a workspace", op(1, func(o *Op) {
			o.Kind, o.PostHash, o.Copy = OpCopyFile, h64("2"), &CopySource{Side: SideLeaving, Path: scrWS + "/x", Hash: h64("2")}
		})},
		{"copy without a side", op(2, func(o *Op) { o.Copy.Side = "" })},
		{"copy source outside", op(2, func(o *Op) { o.Copy.Path = "claude_desktop_config.json" })},
		{"copy post-hash", op(2, func(o *Op) { o.PostHash = h64("9") })},
		{"copy with content", op(2, func(o *Op) { o.Content = []byte("x") })},
		{"mkdir with pre-hash", op(0, func(o *Op) { o.PreHash = h64("1") })},
		{"mkdir without mode", op(0, func(o *Op) { o.Mode, o.PostHash = 0, DirHash(0) })},
		{"mkdir hash", op(0, func(o *Op) { o.PostHash = DirHash(0o700) })},
		{"absolute link", op(3, func(o *Op) { o.Link, o.PostHash = "/etc/passwd", LinkHash("/etc/passwd") })},
		{"escaping link", op(3, func(o *Op) { o.Link, o.PostHash = "../../../../x", LinkHash("../../../../x") })},
		{"link hash", op(3, func(o *Op) { o.PostHash = LinkHash("other") })},
		{"paritydesk temp name", op(2, func(o *Op) { o.Path = scrWS + "/.paritydesk-tmp-x"; o.Copy.Path = o.Path })},
		{"app tmp recovery name", op(2, func(o *Op) { o.Path = scrWS + "/local_1.json.tmp"; o.Copy.Path = o.Path })},
		{"copy bytes over the bound", func(in *ValidateInput) { in.MaxCopyBytes = 5 }},
		{"another row under the root", withOp(writeOp(model.RowSessions, scrWS+"/x", "x"))},
		{"no scratch row in the manifest", func(in *ValidateInput) { in.ScratchRoot = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := scrValid()
			tc.mod(&in)
			if err := Validate(in); pderr.CodeOf(err) != pderr.Internal {
				t.Fatalf("not refused as Internal: %v", err)
			}
		})
	}
}

func TestScratchBreaker(t *testing.T) {
	plan := func(n int, member bool) ValidateInput {
		in := scrValid()
		in.Plan.Ops, in.Plan.Changes = nil, nil
		for i := 0; i < n; i++ {
			rel := "f" + itoa3(i)
			if member {
				rel = ".git/objects/" + rel
			}
			in.Plan.Changes = append(in.Plan.Changes, model.Change{Row: model.RowScratch, Kind: model.ChangeDeleted, Key: scrKey + "/" + rel})
		}
		in.Before = map[model.Category]int{model.CatScratch: 100}
		in.BreakerExempt = func(c model.Change) bool { return strings.Contains(c.Key, "/.git/") }
		return in
	}
	if err := Validate(plan(51, false)); pderr.CodeOf(err) != pderr.DeletionBreaker {
		t.Errorf("51 of 100: %v", err)
	}
	if err := Validate(plan(49, false)); err != nil {
		t.Errorf("49 of 100: %v", err)
	}
	small := plan(40, false)
	small.Before[model.CatScratch] = 50
	if err := Validate(small); err != nil {
		t.Errorf("40 of 50 under the minimum of 50: %v", err)
	}
	if err := Validate(plan(500, true)); err != nil {
		t.Errorf("500 unit members counted: %v", err)
	}
	allowed := plan(60, false)
	allowed.Limits.AllowDeletions = 60
	if err := Validate(allowed); err != nil {
		t.Errorf("--allow-deletions 60: %v", err)
	}
}

func itoa3(i int) string {
	return string([]byte{byte('0' + i/100), byte('0' + i/10%10), byte('0' + i%10)})
}

func TestSoftCopy(t *testing.T) {
	cp := func(side, path string) Op {
		return Op{Kind: OpCopyFile, Row: model.RowScratch, Path: path, Copy: &CopySource{Side: side, Path: path}}
	}
	tests := []struct {
		op   Op
		want bool
	}{
		{cp(SideLeaving, scrWS+"/a/b.txt"), true},
		{cp(SideEntering, scrWS+"/a/b.txt"), false},
		{cp(SideLeaving, scrWS+"/.git/HEAD"), false},
		{cp(SideLeaving, scrWS+"/sub/.GIT/objects/x"), false},
		{cp(SideLeaving, scrWS+"/.git"), true}, // a file named .git
		{Op{Kind: OpWriteFile, Row: model.RowScratch, Path: scrWS + "/x"}, false},
		{Op{Kind: OpCopyFile, Row: model.RowSessions, Path: scrWS + "/x", Copy: &CopySource{Side: SideLeaving}}, false},
	}
	for i, tc := range tests {
		if got := SoftCopy(tc.op); got != tc.want {
			t.Errorf("%d: SoftCopy(%s) = %v", i, tc.op.Path, got)
		}
	}
	if !CleanRel("a/b") || CleanRel("a/../b") || CleanRel(`a\b`) {
		t.Error("CleanRel")
	}
	if DirHash(0o755) == DirHash(0o700) || LinkHash("a") == LinkHash("b") || DirHash(0o755) == LinkHash("755") {
		t.Error("hashes collide")
	}
}

func FuzzComposeScratch(f *testing.F) {
	f.Add("f", "1", "2", uint8(0))
	f.Add("d", "", "", uint8(1))
	f.Fuzz(func(t *testing.T, rel, pre, post string, kind uint8) {
		e := scrCopy(rel, pre, post)
		switch kind % 4 {
		case 1:
			e.Kind, e.Copy = EditMkdir, nil
		case 2:
			e.Kind, e.Copy, e.Link = EditSymlink, nil, post
		case 3:
			e.Kind, e.Copy = EditRemove, nil
		}
		ops, err := Compose([]Edit{e}, func(Location) ([]byte, bool, error) { return nil, false, errors.New("read") })
		if err != nil {
			return
		}
		if len(ops) != 1 || ops[0].PreHash != pre {
			t.Fatalf("ops %+v", ops)
		}
	})
}
