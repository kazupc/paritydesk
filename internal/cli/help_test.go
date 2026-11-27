package cli

import (
	"flag"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/i18n"
)

// C1: every short text and flag usage is a catalog id, with the naming
// rule flag.<name> (flag.<command path>.<name> only for a different text).
func TestHelpTextsAreMessages(t *testing.T) {
	texts := map[string]map[string]bool{} // flag name -> English texts
	var walk func(c *command, path []string)
	walk = func(c *command, path []string) {
		if c.short == "" || !i18n.Has(c.short) {
			t.Errorf("%v: short %q is not a catalog id", path, c.short)
		}
		fs := flag.NewFlagSet("x", flag.ContinueOnError)
		(&globalFlags{}).register(fs)
		if c.flags != nil {
			c.flags(fs)
		}
		fs.VisitAll(func(f *flag.Flag) {
			id := i18n.Msg(f.Usage)
			if !i18n.Has(id) {
				t.Errorf("%v --%s: usage %q is not a catalog id", path, f.Name, f.Usage)
				return
			}
			if texts[f.Name] == nil {
				texts[f.Name] = map[string]bool{}
			}
			texts[f.Name][i18n.T(id)] = true
			want := "flag." + f.Name
			if string(id) != want && string(id) != "flag."+strings.Join(path, ".")+"."+f.Name {
				t.Errorf("%v --%s: id %s is neither %s nor qualified by the command path", path, f.Name, id, want)
			}
		})
		for _, s := range c.sub {
			walk(s, append(append([]string(nil), path...), s.name))
		}
	}
	walk(root(), nil)
	for id := range i18n.Catalog() {
		s := string(id)
		if !strings.HasPrefix(s, "flag.") {
			continue
		}
		parts := strings.Split(s, ".")
		name := parts[len(parts)-1]
		if texts[name] == nil {
			t.Errorf("%s names no registered flag", s)
			continue
		}
		if !i18n.Has(i18n.Msg("flag." + name)) {
			t.Errorf("flag.%s is missing", name)
		}
		if len(parts) > 2 && len(texts[name]) < 2 {
			t.Errorf("%s is qualified, but every command registers --%s with the same text", s, name)
		}
	}
}
