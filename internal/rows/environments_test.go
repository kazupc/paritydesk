package rows

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
)

func envFields() map[string][]string {
	return map[string][]string{
		RoleName:     {"name"},
		RolePerField: {"name", "settings"},
		RoleSecret:   {"settings.env"},
	}
}

func preset(t fatalHelper, n int, name, settings string) model.Item {
	t.Helper()
	id := fakeUUID(n)
	return mk(t, presetPrefix+id, fmt.Sprintf(`{"id":%q,"name":%q,"settings":%s}`, id, name, settings), 0)
}

func started(t fatalHelper, session, env int) model.Item {
	t.Helper()
	return mk(t, startedPrefix+fakeSession(session), fmt.Sprintf("%q", fakeUUID(env)), 0)
}

func envPolicy() Policy {
	p := openPolicy()
	p.Fields = envFields()
	return p
}

func TestEnvironmentsMatching(t *testing.T) {
	row := model.RowEnvironments
	tests := []struct {
		name       string
		l, e       []model.Item
		remap      map[string]string
		wantKeys   []string
		absentKeys []string
		collision  []string
	}{
		{
			name:     "same id merges",
			l:        []model.Item{preset(t, 1, "dev", `{"a":1}`)},
			e:        []model.Item{preset(t, 1, "dev", `{"a":1}`)},
			wantKeys: []string{presetPrefix + fakeUUID(1)},
		},
		{
			name:       "unique name maps ids",
			l:          []model.Item{preset(t, 1, "dev", `{"a":1}`), started(t, 50, 1)},
			e:          []model.Item{preset(t, 2, "dev", `{"a":1}`)},
			remap:      map[string]string{fakeUUID(1): fakeUUID(2)},
			wantKeys:   []string{presetPrefix + fakeUUID(2), startedPrefix + fakeSession(50)},
			absentKeys: []string{presetPrefix + fakeUUID(1)},
		},
		{
			name:      "name collision keeps both",
			l:         []model.Item{preset(t, 1, "dev", `{"a":1}`), preset(t, 3, "dev", `{"a":2}`)},
			e:         []model.Item{preset(t, 2, "dev", `{"a":1}`)},
			wantKeys:  []string{presetPrefix + fakeUUID(1), presetPrefix + fakeUUID(2), presetPrefix + fakeUUID(3)},
			collision: []string{presetPrefix + fakeUUID(1), presetPrefix + fakeUUID(3)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := input(row, snap(row, tc.l...), snap(row), snap(row, tc.e...), snap(row, tc.e...), nil, envPolicy())
			in.Leaving.BaseValid = false
			out := mustMerge(t, row, in)
			if fmt.Sprint(out.EnvRemap) != fmt.Sprint(tc.remap) {
				t.Fatalf("EnvRemap = %v, want %v", out.EnvRemap, tc.remap)
			}
			for _, k := range tc.wantKeys {
				if _, ok := out.Desired.Items[k]; !ok {
					t.Fatalf("desired lacks %s", k)
				}
			}
			for _, k := range tc.absentKeys {
				if _, ok := out.Desired.Items[k]; ok {
					t.Fatalf("desired has %s", k)
				}
			}
			for _, k := range tc.collision {
				found := false
				for _, c := range out.Changes {
					found = found || (c.Key == k && c.Note == noteCollision)
				}
				if !found {
					t.Fatalf("collision on %s not reported: %+v", k, out.Changes)
				}
			}
			if tc.remap != nil {
				got := out.Desired.Items[startedPrefix+fakeSession(50)]
				if string(got.Value) != fmt.Sprintf("%q", fakeUUID(2)) {
					t.Fatalf("started value not remapped: %s", got.Value)
				}
				p := out.Desired.Items[presetPrefix+fakeUUID(2)]
				if field(t, p, "id") != fakeUUID(2) {
					t.Fatalf("preset id not rewritten: %s", p.Value)
				}
			}
		})
	}
}

func TestEnvironmentsSettingsPerField(t *testing.T) {
	row := model.RowEnvironments
	base := preset(t, 1, "dev", `{"a":1,"b":1}`)
	l := preset(t, 1, "dev", `{"a":2,"b":1}`)
	e := preset(t, 1, "dev", `{"a":1,"b":2}`)
	out := mustMerge(t, row, input(row, snap(row, l), snap(row, base), snap(row, e), snap(row, base), snap(row, base), envPolicy()))
	got := field(t, out.Desired.Items[base.Key], "settings")
	if fmt.Sprint(got) != fmt.Sprint(map[string]any{"a": float64(2), "b": float64(2)}) {
		t.Fatalf("settings = %v", got)
	}
	if len(out.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts %+v", out.Conflicts)
	}
}

func TestEnvironmentsLimits(t *testing.T) {
	row := model.RowEnvironments
	tests := []struct {
		name  string
		items func() []model.Item
		fail  bool
	}{
		{"at the count limit", func() []model.Item { return presets(t, MaxPresets, 0) }, false},
		{"over the count limit", func() []model.Item { return presets(t, MaxPresets+1, 0) }, true},
		{"over the size limit", func() []model.Item { return presets(t, 3, MaxPresetBytes/2) }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := input(row, snap(row, tc.items()...), snap(row), snap(row), snap(row), nil, envPolicy())
			_, err := Registry()[row].Merge(in)
			if tc.fail != errors.Is(err, ErrPresetLimit) {
				t.Fatalf("err = %v, want limit error %v", err, tc.fail)
			}
		})
	}
}

func presets(t fatalHelper, n, pad int) []model.Item {
	out := make([]model.Item, n)
	for i := range out {
		out[i] = preset(t, i+1, fmt.Sprintf("p%d", i), fmt.Sprintf(`{"pad":%q}`, strings.Repeat("x", pad)))
	}
	return out
}

func TestEnvironmentsSecrets(t *testing.T) {
	row := model.RowEnvironments
	secretPreset := func(n int, env string) model.Item {
		it := preset(t, n, "dev", fmt.Sprintf(`{"env":%s}`, env))
		it.Secret = true
		return it
	}
	base := secretPreset(1, `{"TOKEN":"sha256:aa"}`)
	changed := secretPreset(1, `{"TOKEN":"sha256:bb"}`)
	tests := []struct {
		name    string
		managed bool
		cross   bool
		allow   map[string]bool
		carried bool
	}{
		{"unmanaged edge carries", false, false, nil, true},
		{"managed edge blocks", true, false, nil, false},
		{"managed edge allowed per var", true, false, map[string]bool{"dev/TOKEN": true}, true},
		{"managed edge other var not enough", true, false, map[string]bool{"dev/OTHER": true}, false},
		{"secrets may cross", true, true, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pol := envPolicy()
			pol.EnteringManaged = tc.managed
			pol.SecretsMayCross = tc.cross
			pol.SecretsAllow = tc.allow
			out := mustMerge(t, row, input(row, snap(row, changed), snap(row, base), snap(row, base), snap(row, base), snap(row, base), pol))
			got := out.Desired.Items[base.Key]
			if (got.Hash == changed.Hash) != tc.carried {
				t.Fatalf("carried = %v, want %v", got.Hash == changed.Hash, tc.carried)
			}
			if !tc.carried && !hasChange(out, model.ChangeSkipped, base.Key) {
				t.Fatalf("blocked secret not reported: %+v", out.Changes)
			}
			if tc.carried && string(got.Raw) != string(changed.Raw) {
				t.Fatal("carried secret lost the winner's raw bytes")
			}
		})
	}
}
