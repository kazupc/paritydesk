package pins

import (
	"encoding/json"
	"regexp"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/sources"
)

// Where a mirror list came from.
const (
	mirrorBucket = "bucket"
	mirrorLS     = "ls"
)

// Mirror is the app's own mirror of the local pin list: the preferences
// bucket of claude_desktop_config.json and a Local Storage item. The app
// seeds an empty store from it at launch (bucket first); paritydesk only
// reads it.
type Mirror struct {
	IDs    []string // pattern-matching ids, in stored order, deduplicated
	From   string   // "bucket" | "ls"
	LSTime int64    // the Local Storage envelope's timestamp in ms (0 when absent)
	LSIDs  []string // the Local Storage list, nil if absent
}

// ReadMirror reads the mirror: bucket first, then Local Storage. ok is
// false when neither holds a parseable list. A missing file, key or
// Local Storage reader makes that part absent.
func ReadMirror(rc sources.ReadContext, spec *compat.IDBSpec) (Mirror, bool, error) {
	var m Mirror
	if spec == nil {
		return m, false, nil
	}
	re, err := regexp.Compile(spec.IDPattern)
	if err != nil {
		return m, false, err
	}
	bucket, bucketOK := readBucket(rc, spec, re)
	lsIDs, lsTime, lsOK := readLS(rc, spec, re)
	if lsOK {
		m.LSIDs, m.LSTime = lsIDs, lsTime
		if m.LSIDs == nil {
			m.LSIDs = []string{}
		}
	}
	switch {
	case bucketOK:
		m.IDs, m.From = bucket, mirrorBucket
	case lsOK:
		m.IDs, m.From = m.LSIDs, mirrorLS
	default:
		return m, false, nil
	}
	if m.IDs == nil {
		m.IDs = []string{}
	}
	return m, true, nil
}

// readBucket reads the list at mirror_pref_path of the config file.
func readBucket(rc sources.ReadContext, spec *compat.IDBSpec, re *regexp.Regexp) ([]string, bool) {
	doc, err := sources.LoadFile(rc.Folder, sources.DesktopConfigFile)
	if err != nil {
		return nil, false
	}
	raw, found, err := doc.Get(jsonx.Path(spec.MirrorPrefPath))
	if err != nil || !found {
		return nil, false
	}
	return idList(raw, re)
}

// readLS reads the Local Storage mirror (an LSS envelope).
func readLS(rc sources.ReadContext, spec *compat.IDBSpec, re *regexp.Regexp) ([]string, int64, bool) {
	if rc.LS == nil {
		return nil, 0, false
	}
	doc, err := sources.LoadLS(rc.LS, spec.MirrorLSKey)
	if err != nil {
		return nil, 0, false
	}
	raw, found, err := doc.Get(jsonx.Path(spec.MirrorLSIDsPath))
	if err != nil || !found {
		return nil, 0, false
	}
	ids, ok := idList(raw, re)
	if !ok {
		return nil, 0, false
	}
	var ts int64
	if t, found, err := doc.Get(jsonx.Path(spec.MirrorLSTimePath)); err == nil && found {
		var n json.Number
		if json.Unmarshal(t, &n) == nil {
			if i, err := n.Int64(); err == nil {
				ts = i
			}
		}
	}
	return ids, ts, true
}

// idList parses a JSON array and keeps the pattern-matching strings.
func idList(raw json.RawMessage, re *regexp.Regexp) ([]string, bool) {
	v, err := decodeOne(raw)
	if err != nil {
		return nil, false
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	ids, _ := filterIDs(arr, re)
	return ids, true
}
