// Package configjson reads the identity of a data folder from the app's
// config.json without decoding anything else in it.
//
// config.json also holds the app's encrypted token cache. paritydesk
// treats the file as credential-bearing (M4):
//
//   - it is read by a span scan: only the top-level "lastKnownAccountUuid"
//     string is decoded, and the presence of the token-cache key is checked
//     without reading its value;
//   - it is never written, never copied into state, generations or logs;
//   - the read buffer is zeroed before returning;
//   - errors carry byte offsets only.
package configjson
