package localstorage

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/kazupc/paritydesk/internal/jsonx"
)

// Two JSON wrappers occur in the app's Local Storage values:
//
//   - the "LSS" envelope of keys named "LSS-persisted.<name>":
//     {"value": <v>, "tabId": "...", "timestamp": <ms>}; the config mirror
//     (preferences.epitaxyPrefs in claude_desktop_config.json) holds the
//     bare <v> under "<name>".
//   - the persisted-store wrapper of keys such as "persisted.<store>" and
//     "dframe-store": {"state": <s>, "version": <n>}.
//
// Both are edited with jsonx so every other byte stays identical (H10).

// ErrNoEnvelope is returned when a value lacks the expected wrapper member.
var ErrNoEnvelope = errors.New("localstorage: value has no expected envelope member")

// EnvelopeValue returns the "value" member of an LSS envelope and its
// "timestamp" (0 if absent or not an integer).
func EnvelopeValue(text []byte) (json.RawMessage, int64, error) {
	v, err := jsonx.Get(text, jsonx.Path{"value"})
	if errors.Is(err, jsonx.ErrNotFound) {
		return nil, 0, ErrNoEnvelope
	}
	if err != nil {
		return nil, 0, err
	}
	var ts int64
	if raw, err := jsonx.Get(text, jsonx.Path{"timestamp"}); err == nil {
		ts, _ = strconv.ParseInt(string(raw), 10, 64)
	}
	return v, ts, nil
}

// SetEnvelopeValue replaces the "value" member of an LSS envelope and sets
// "timestamp" to ts. Other members are left byte-identical. paritydesk
// records every timestamp it writes so that it never wins a tie-break (D7).
func SetEnvelopeValue(text []byte, value json.RawMessage, ts int64) ([]byte, error) {
	out, err := jsonx.Set(text, jsonx.Path{"value"}, value)
	if err != nil {
		return nil, err
	}
	return jsonx.Set(out, jsonx.Path{"timestamp"}, json.RawMessage(strconv.FormatInt(ts, 10)))
}

// NewEnvelope builds a fresh LSS envelope for a key the target lacks.
func NewEnvelope(value json.RawMessage, ts int64) ([]byte, error) {
	if err := jsonx.Validate(value); err != nil {
		return nil, err
	}
	return jsonx.Marshal(struct {
		Value     json.RawMessage `json:"value"`
		TabID     string          `json:"tabId"`
		Timestamp int64           `json:"timestamp"`
	}{value, "", ts})
}

// PersistedState returns the "state" member and the "version" of a
// persisted-store wrapper. version is -1 if absent.
func PersistedState(text []byte) (json.RawMessage, int, error) {
	s, err := jsonx.Get(text, jsonx.Path{"state"})
	if errors.Is(err, jsonx.ErrNotFound) {
		return nil, 0, ErrNoEnvelope
	}
	if err != nil {
		return nil, 0, err
	}
	version := -1
	if raw, err := jsonx.Get(text, jsonx.Path{"version"}); err == nil {
		if n, err := strconv.Atoi(string(raw)); err == nil {
			version = n
		}
	}
	return s, version, nil
}
