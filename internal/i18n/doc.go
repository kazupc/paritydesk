// Package i18n holds every user-facing message, in English, keyed by
// message ids (catalog.go).
//
// paritydesk prints English only. The plugin's skills talk to the user in
// the language the user writes in and translate what paritydesk prints.
//
// Messages are late-bound values (Text): structs keep the id and the
// arguments, and the text is rendered when it is printed or encoded, so
// arguments can still be sanitized or redacted (MapStrings). No message
// may advise ending an account's session (policy tests check the
// catalog).
package i18n
