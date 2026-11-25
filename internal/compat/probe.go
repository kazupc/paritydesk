package compat

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
)

// Probe details. They are constants: a detail never carries a value.
const (
	detailOK          = "ok"
	detailAbsent      = "absent"
	detailMissing     = "missing"
	detailNotRegular  = "not a regular file"
	detailNotDir      = "not a directory"
	detailWrongType   = "unexpected type"
	detailMalformed   = "malformed"
	detailNoWrapper   = "no persisted-store wrapper"
	detailNoVersion   = "no version"
	detailVersion     = "version mismatch"
	detailNoLS        = "Local Storage absent"
	detailUnsupported = "unsupported probe target"
	detailUnreadable  = "unreadable"
	detailUnknownKind = "unknown probe kind"
)

// maxProbeFile bounds what a probe reads.
const maxProbeFile = 64 << 20

// identityFile is read by a top-level span scan only (M4).
const identityFile = "config.json"

func expandTemplate(s string, r model.Replica) string {
	return strings.NewReplacer("{acct}", string(r.Account), "{org}", string(r.Org)).Replace(s)
}

func runProbe(p Probe, env ProbeEnv) (bool, string) {
	target := expandTemplate(p.Target, env.Replica)
	jp := make(jsonx.Path, len(p.Path))
	for i, s := range p.Path {
		jp[i] = expandTemplate(s, env.Replica)
	}
	switch p.Kind {
	case ProbeFile:
		return probeStat(env.Folder, target, false)
	case ProbeDir:
		return probeStat(env.Folder, target, true)
	case ProbeJSONShape:
		return probeJSONShape(env.Folder, target, jp, p.Type)
	case ProbeIdxVersion:
		return probeIdx(env.Folder, target, p.Version)
	case ProbeJSONVersion:
		return probeJSONVersion(env.Folder, target, jp, p.Version)
	case ProbeLSKey, ProbeLSShape, ProbePersistVersion:
		return probeLS(p.Kind, env, target, jp, p.Type, p.Version)
	}
	return false, detailUnknownKind
}

func probeStat(folder, rel string, dir bool) (bool, string) {
	fi, err := os.Lstat(filepath.Join(folder, filepath.FromSlash(rel)))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, detailMissing
	case err != nil:
		return false, detailUnreadable
	case dir && !fi.IsDir():
		return false, detailNotDir
	case !dir && !fi.Mode().IsRegular():
		return false, detailNotRegular
	}
	return true, detailOK
}

// readProbeFile reads a regular file; absent gives (nil, false, "").
func readProbeFile(p string) ([]byte, bool, string) {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, ""
	}
	if err != nil {
		return nil, false, detailUnreadable
	}
	if !fi.Mode().IsRegular() {
		return nil, false, detailNotRegular
	}
	if fi.Size() > maxProbeFile {
		return nil, false, detailUnreadable
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false, detailUnreadable
	}
	return b, true, ""
}

// matches returns the files a target names; the last segment may contain
// glob characters.
func matches(folder, rel string) ([]string, string) {
	dir, pat := path.Split(rel)
	if !strings.ContainsAny(pat, "*?[") {
		return []string{filepath.Join(folder, filepath.FromSlash(rel))}, ""
	}
	ents, err := os.ReadDir(filepath.Join(folder, filepath.FromSlash(dir)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ""
	}
	if err != nil {
		return nil, detailUnreadable
	}
	var out []string
	for _, e := range ents {
		if ok, _ := path.Match(pat, e.Name()); ok {
			out = append(out, filepath.Join(folder, filepath.FromSlash(dir), e.Name()))
		}
	}
	return out, ""
}

func probeJSONShape(folder, target string, jp jsonx.Path, typ string) (bool, string) {
	if path.Base(target) == identityFile {
		return probeIdentity(filepath.Join(folder, filepath.FromSlash(target)), jp, typ)
	}
	files, bad := matches(folder, target)
	if bad != "" {
		return false, bad
	}
	// Session records are many and large: check them on all CPUs. The
	// first failure in file order is reported, as a serial loop would.
	details := make([]string, len(files))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(runtime.NumCPU(), len(files)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				details[i] = shapeOfFile(files[i], jp, typ)
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for _, d := range details {
		if d != "" {
			return false, d
		}
	}
	return true, detailOK
}

// shapeOfFile returns "" when f is absent or has the shape, else why not.
func shapeOfFile(f string, jp jsonx.Path, typ string) string {
	b, present, bad := readProbeFile(f)
	if bad != "" {
		return bad
	}
	if !present {
		return ""
	}
	if ok, d := shapeAt(b, jp, typ); !ok {
		return d
	}
	return ""
}

// probeIdentity checks one top-level member of config.json without
// decoding any other value. The buffer is cleared before returning.
func probeIdentity(p string, jp jsonx.Path, typ string) (bool, string) {
	if len(jp) != 1 || (typ != "string" && typ != "") {
		return false, detailUnsupported
	}
	b, present, bad := readProbeFile(p)
	defer clear(b)
	if bad != "" {
		return false, bad
	}
	if !present {
		return true, detailAbsent
	}
	if typ == "" {
		ok, err := jsonx.HasTopLevelKey(b, jp[0])
		if err != nil {
			return false, detailMalformed
		}
		if !ok {
			return false, detailMissing
		}
		return true, detailOK
	}
	s, found, err := jsonx.TopLevelString(b, jp[0])
	switch {
	case errors.Is(err, jsonx.ErrNotString):
		return false, detailWrongType
	case err != nil:
		return false, detailMalformed
	case !found || s == "":
		return false, detailMissing
	}
	return true, detailOK
}

// shapeAt checks that the value at jp (when present) has type typ.
func shapeAt(doc []byte, jp jsonx.Path, typ string) (bool, string) {
	if err := jsonx.Validate(doc); err != nil {
		return false, detailMalformed
	}
	v := json.RawMessage(doc)
	if len(jp) > 0 {
		got, err := jsonx.Get(doc, jp)
		switch {
		case errors.Is(err, jsonx.ErrNotFound):
			return true, detailAbsent
		case err != nil:
			return false, detailWrongType
		}
		v = got
	}
	if !hasType(v, typ) {
		return false, detailWrongType
	}
	return true, detailOK
}

func hasType(v json.RawMessage, typ string) bool {
	t := bytes.TrimSpace(v)
	if len(t) == 0 {
		return false
	}
	switch typ {
	case "":
		return true
	case "object":
		return t[0] == '{'
	case "array":
		return t[0] == '['
	case "string":
		return t[0] == '"'
	case "bool":
		return t[0] == 't' || t[0] == 'f'
	case "number":
		return t[0] == '-' || (t[0] >= '0' && t[0] <= '9')
	}
	return false
}

func probeIdx(folder, target string, version int) (bool, string) {
	if version == 0 {
		version = 1
	}
	b, present, bad := readProbeFile(filepath.Join(folder, filepath.FromSlash(target)))
	if bad != "" {
		return false, bad
	}
	if !present {
		return true, detailAbsent
	}
	var idx struct {
		V        *int              `json:"v"`
		Archived []json.RawMessage `json:"archived"`
	}
	if jsonx.Validate(b) != nil || json.Unmarshal(b, &idx) != nil {
		return false, detailMalformed
	}
	if idx.V == nil || *idx.V != version {
		return false, detailVersion
	}
	return true, detailOK
}

// probeJSONVersion checks that the number at jp in a file equals version.
// An absent file passes; a present file without the member, or with
// another value, fails. Only the member is reported, never its value.
func probeJSONVersion(folder, target string, jp jsonx.Path, version int) (bool, string) {
	b, present, bad := readProbeFile(filepath.Join(folder, filepath.FromSlash(target)))
	if bad != "" {
		return false, bad
	}
	if !present {
		return true, detailAbsent
	}
	if jsonx.Validate(b) != nil {
		return false, detailMalformed
	}
	raw, err := jsonx.Get(b, jp)
	switch {
	case errors.Is(err, jsonx.ErrNotFound):
		return false, detailNoVersion
	case err != nil:
		return false, detailWrongType
	}
	var n json.Number
	if !hasType(raw, "number") || json.Unmarshal(raw, &n) != nil {
		return false, detailWrongType
	}
	if v, err := n.Int64(); err != nil || v != int64(version) {
		return false, detailVersion
	}
	return true, detailOK
}

func probeLS(kind ProbeKind, env ProbeEnv, name string, jp jsonx.Path, typ string, version int) (bool, string) {
	if env.LS == nil {
		if kind == ProbeLSKey {
			return false, detailNoLS
		}
		return true, detailAbsent
	}
	key, err := localstorage.Key(name)
	if err != nil {
		return false, detailUnsupported
	}
	raw, found, err := env.LS.Get(key)
	switch {
	case err != nil:
		return false, detailUnreadable
	case !found && kind == ProbeLSKey:
		return false, detailMissing
	case !found:
		return true, detailAbsent
	case kind == ProbeLSKey:
		return true, detailOK
	}
	text, err := localstorage.DecodeText(raw)
	if err != nil {
		return false, detailMalformed
	}
	if kind == ProbeLSShape {
		return shapeAt([]byte(text), jp, typ)
	}
	_, v, err := localstorage.PersistedState([]byte(text))
	switch {
	case err != nil:
		return false, detailNoWrapper
	case v < 0:
		return false, detailNoVersion
	case version > 0 && v != version:
		return false, detailVersion
	}
	return true, detailOK
}
