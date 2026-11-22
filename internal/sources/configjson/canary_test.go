package configjson

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/jsonx"
)

const testAccount = "00000000-0000-4000-8000-000000000001"

// canaryDocs plant the canary in every member except the account id,
// including in ways that break parsing next to or inside the canary.
var canaryDocs = map[string]string{
	"valid":            `{"oauth:tokenCacheV2":{"t":"` + canary + `"},"lastKnownAccountUuid":"` + testAccount + `","other":"` + canary + `"}`,
	"syntax after":     `{"oauth:tokenCacheV2":"` + canary + `" "lastKnownAccountUuid":"` + testAccount + `"}`,
	"unterminated":     `{"lastKnownAccountUuid":"` + testAccount + `","oauth:tokenCacheV2":"` + canary,
	"bad escape":       `{"lastKnownAccountUuid":"` + testAccount + `","oauth:tokenCacheV2":"` + canary + `\q"}`,
	"control char":     `{"lastKnownAccountUuid":"` + testAccount + `","x":"` + canary + "\x01" + `"}`,
	"account canary":   `{"oauth:tokenCacheV2":"` + canary + `","lastKnownAccountUuid":"` + canary + `"}`,
	"account object":   `{"oauth:tokenCacheV2":"` + canary + `","lastKnownAccountUuid":{"v":"` + canary + `"}}`,
	"account number":   `{"oauth:tokenCacheV2":"` + canary + `","lastKnownAccountUuid":12}`,
	"duplicate token":  `{"oauth:tokenCacheV2":"` + canary + `","lastKnownAccountUuid":"` + testAccount + `","oauth:tokenCacheV2":"` + canary + `"}`,
	"duplicate other":  `{"x":"` + canary + `","lastKnownAccountUuid":"` + testAccount + `","x":"` + canary + `"}`,
	"duplicate acct":   `{"lastKnownAccountUuid":"` + testAccount + `","t":"` + canary + `","lastKnownAccountUuid":"` + testAccount + `"}`,
	"not an object":    `["` + canary + `"]`,
	"trailing data":    `{"lastKnownAccountUuid":"` + testAccount + `"} "` + canary + `"`,
	"canary key":       `{"` + canary + `":1,"lastKnownAccountUuid":"` + testAccount + `",}`,
	"bad unicode":      `{"lastKnownAccountUuid":"` + testAccount + `","t":"` + canary + `\u12"}`,
	"no account":       `{"oauth:tokenCacheV2":"` + canary + `"}`,
	"truncated nested": `{"lastKnownAccountUuid":"` + testAccount + `","t":[{"a":"` + canary + `"},`,
}

func TestCanaryNeverLeaks(t *testing.T) {
	for name, doc := range canaryDocs {
		t.Run(name, func(t *testing.T) {
			id, err := parse([]byte(doc))
			if name == "valid" {
				if err != nil || string(id.Account) != testAccount || !id.HasTokenCache {
					t.Fatalf("got %+v %v", id, err)
				}
			} else if err == nil {
				t.Fatalf("accepted: %+v", id)
			}
			for _, s := range []string{fmt.Sprint(err), fmt.Sprintf("%+v", id), fmt.Sprintf("%#v", err)} {
				if strings.Contains(s, canary) {
					t.Fatalf("canary leaked: %s", s)
				}
			}
			// The same holds through the file reader.
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			_, ferr := ReadIdentity(dir)
			if (ferr == nil) != (err == nil) || strings.Contains(fmt.Sprint(ferr), canary) {
				t.Fatalf("ReadIdentity: %v", ferr)
			}
		})
	}
}

func TestDuplicateTopLevelKeysRefused(t *testing.T) {
	for _, name := range []string{"duplicate token", "duplicate other", "duplicate acct"} {
		if _, err := parse([]byte(canaryDocs[name])); !errors.Is(err, jsonx.ErrDuplicateKey) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestReadIdentityRefusesUnsafeFiles(t *testing.T) {
	valid := []byte(canaryDocs["valid"])
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		check func(error) bool
	}{
		{"symlink", func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "real.json")
			mustWrite(t, target, valid)
			if err := os.Symlink(target, filepath.Join(dir, FileName)); err != nil {
				t.Fatal(err)
			}
		}, nonNil},
		{"directory", func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, FileName), 0o700); err != nil {
				t.Fatal(err)
			}
		}, nonNil},
		{"too large", func(t *testing.T, dir string) {
			p := filepath.Join(dir, FileName)
			mustWrite(t, p, valid)
			if err := os.Truncate(p, maxSize+1); err != nil {
				t.Fatal(err)
			}
		}, func(err error) bool { return err != nil && strings.Contains(err.Error(), "too large") }},
		{"duplicate keys", func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, FileName), []byte(canaryDocs["duplicate other"]))
		}, func(err error) bool { return errors.Is(err, jsonx.ErrDuplicateKey) }},
		{"missing", func(*testing.T, string) {}, func(err error) bool { return errors.Is(err, fs.ErrNotExist) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			id, err := ReadIdentity(dir)
			if !tc.check(err) {
				t.Fatalf("got %+v %v", id, err)
			}
			if strings.Contains(fmt.Sprint(err), canary) {
				t.Fatalf("canary leaked: %v", err)
			}
		})
	}
}

func TestReadIdentityAtSizeLimit(t *testing.T) {
	dir := t.TempDir()
	doc := []byte(`{"lastKnownAccountUuid":"` + testAccount + `"}`)
	pad := make([]byte, maxSize-len(doc))
	for i := range pad {
		pad[i] = ' '
	}
	mustWrite(t, filepath.Join(dir, FileName), append(doc, pad...))
	id, err := ReadIdentity(dir)
	if err != nil || string(id.Account) != testAccount || id.HasTokenCache {
		t.Fatalf("got %+v %v", id, err)
	}
}

func nonNil(err error) bool { return err != nil }

func mustWrite(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
