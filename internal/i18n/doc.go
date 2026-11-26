// Package i18n holds every user-facing message: English in catalog.go,
// Japanese in catalog_ja.go, keyed by the same ids.
//
// Each process has a process language (Current, used for every human line)
// and a display language (DisplayLang, used only for the additive
// display_lines of JSON documents). The CLI chooses them (Resolve): --lang,
// PARITYDESK_LANG, [ui] lang, the locale, [ui] fallback_lang, English.
//
// Messages are late-bound values (Text): structs keep the id and the
// arguments, JSON encodes the English text, and human output renders the
// process language at print time. A translation is used only when its
// format verbs are compatible with the English ones (Compatible).
//
// English only, by design: --json output and error codes, model-facing
// hook text (ids "hook." and "guard."), and the notifications of the
// detached job (EnglishOnly). No message may advise ending an account's
// session (policy tests check every catalog).
package i18n
