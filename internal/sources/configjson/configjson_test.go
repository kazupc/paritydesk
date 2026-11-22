package configjson

import (
	"errors"
	"strings"
	"testing"
)

const canary = "PARITYDESK-CANARY-TOKEN-7f3c"

func TestParse(t *testing.T) {
	doc := `{"oauth:tokenCacheV2":"` + canary + `","lastKnownAccountUuid":"00000000-0000-4000-8000-000000000001","x":[1,{"y":"z"}]}`
	id, err := parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if id.Account != "00000000-0000-4000-8000-000000000001" || !id.HasTokenCache {
		t.Fatalf("got %+v", id)
	}
}

func TestNoAccount(t *testing.T) {
	if _, err := parse([]byte(`{"a":1}`)); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("got %v", err)
	}
}

func TestErrorsNeverCarryContent(t *testing.T) {
	doc := `{"oauth:tokenCacheV2":"` + canary + `","lastKnownAccountUuid":"` + canary + `"}`
	_, err := parse([]byte(doc))
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("error must not carry file content: %v", err)
	}
	bad := `{"oauth:tokenCacheV2":"` + canary + `" "lastKnownAccountUuid":1}`
	_, err = parse([]byte(bad))
	if err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("syntax error must not carry file content: %v", err)
	}
}
