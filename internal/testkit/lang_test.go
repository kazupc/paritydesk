package testkit

import (
	"testing"

	"github.com/kazupc/paritydesk/internal/i18n"
)

func TestLangRestores(t *testing.T) {
	t.Run("ja", func(t *testing.T) {
		Lang(t, i18n.Japanese)
		if i18n.Current() != i18n.Japanese || i18n.DisplayLang() != i18n.Japanese {
			t.Fatal("not set")
		}
	})
	if i18n.Current() != i18n.English || i18n.DisplayLang() != i18n.English {
		t.Fatal("not restored")
	}
}
