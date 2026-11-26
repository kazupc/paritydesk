package orchestrator

import "github.com/kazupc/paritydesk/internal/i18n"

// langCheck adds the lang check, first in the list: the message language
// and where it came from; warn when PARITYDESK_LANG held an invalid value
// or when Japanese was chosen but the terminal forces English.
func (dr *doctor) langCheck(opts DoctorOptions) {
	res := opts.Lang
	if res.Lang == "" {
		res = i18n.Resolution{Lang: i18n.English, Source: i18n.SourceDefault}
	}
	switch {
	case res.EnvInvalid:
		dr.add("lang", "warn", i18n.Tx(i18n.DoctorLangEnvInvalid))
	case opts.LangForced:
		dr.add("lang", "warn", i18n.Tx(i18n.DoctorLangASCII))
	default:
		dr.add("lang", "ok", i18n.Tx(i18n.DoctorLang, string(res.Lang), string(res.Source)))
	}
}
