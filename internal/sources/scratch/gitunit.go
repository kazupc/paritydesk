package scratch

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/kazupc/paritydesk/internal/model"
)

// UnitItem builds a git unit's item from its members (every item whose
// key lies below unit): the digest is the sha256 over the sorted member
// lines "<rel below the unit>\t<k>\t<sha256|target|”>\t<perm octal>\n",
// n is the member count and the stamp is the newest member mtime.
func UnitItem(unit string, mode uint32, items map[string]model.Item) (model.Item, error) {
	var lines []string
	var newest int64
	n := 0
	for k, it := range items {
		rel, ok := strings.CutPrefix(k, unit)
		if !ok || rel == "" {
			continue
		}
		v, ok := Decode(it)
		if !ok {
			return model.Item{}, fmt.Errorf("scratch: unit member %q has no scratch value", k)
		}
		content := ""
		switch v.K {
		case KindFile:
			content = v.SHA256
		case KindLink:
			content = v.Target
		}
		lines = append(lines, rel+"\t"+v.K+"\t"+content+"\t"+strconv.FormatUint(uint64(v.Mode), 8)+"\n")
		n++
		newest = max(newest, it.Stamp.AppTime)
	}
	sort.Strings(lines)
	digest := model.HashBytes([]byte(strings.Join(lines, "")))
	return newValueItem(unit, Value{K: KindGit, Mode: mode, Digest: digest, N: n}, model.Stamp{AppTime: newest})
}

// DivergedItem is the canonical marker of a git unit both accounts
// changed: neither copy is carried until they match again.
func DivergedItem(unit string) (model.Item, error) {
	return newValueItem(unit, Value{K: KindGit, Diverged: true}, model.Stamp{})
}

// IsDiverged reports whether it is the diverged marker.
func IsDiverged(it model.Item) bool {
	v, ok := Decode(it)
	return ok && v.K == KindGit && v.Diverged
}

// newValueItem encodes v canonically (only the members its kind uses).
func newValueItem(key string, v Value, st model.Stamp) (model.Item, error) {
	raw, err := encodeValue(v)
	if err != nil {
		return model.Item{}, err
	}
	return model.NewItem(key, raw, nil, st, false)
}

// encodeValue writes exactly the members of v's kind, so equal content
// gives equal bytes on every side.
func encodeValue(v Value) ([]byte, error) {
	var b strings.Builder
	b.WriteString(`{"k":` + strconv.Quote(v.K))
	switch v.K {
	case KindRoot:
		b.WriteString(`,"mode":` + strconv.FormatUint(uint64(v.Mode), 10))
		if v.Over != "" {
			b.WriteString(`,"over":` + strconv.Quote(v.Over))
		}
	case KindDir:
		b.WriteString(`,"mode":` + strconv.FormatUint(uint64(v.Mode), 10))
	case KindGit:
		if v.Diverged {
			b.WriteString(`,"diverged":true`)
		} else {
			b.WriteString(`,"mode":` + strconv.FormatUint(uint64(v.Mode), 10))
			b.WriteString(`,"digest":` + strconv.Quote(v.Digest))
			b.WriteString(`,"n":` + strconv.Itoa(v.N))
		}
	case KindFile:
		b.WriteString(`,"sha256":` + strconv.Quote(v.SHA256))
		b.WriteString(`,"size":` + strconv.FormatInt(v.Size, 10))
		b.WriteString(`,"mode":` + strconv.FormatUint(uint64(v.Mode), 10))
	case KindLink:
		b.WriteString(`,"target":` + jsonString(v.Target))
	default:
		return nil, fmt.Errorf("scratch: unknown item kind %q", v.K)
	}
	b.WriteString("}")
	return []byte(b.String()), nil
}

// jsonString quotes s as a JSON string (strconv.Quote escapes differ
// from JSON for some runes, so links go through encoding/json rules).
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// RootItem, DirItem, FileItem and LinkItem build the items of one read.
func RootItem(ws string, mode uint32, over string) (model.Item, error) {
	return newValueItem(RootKey(ws), Value{K: KindRoot, Mode: mode, Over: over}, model.Stamp{})
}

// DirItem builds a directory item.
func DirItem(key string, mode uint32) (model.Item, error) {
	return newValueItem(key, Value{K: KindDir, Mode: mode}, model.Stamp{})
}

// FileItem builds a file item.
func FileItem(key, sha string, size int64, mode uint32, mtimeMs int64) (model.Item, error) {
	return newValueItem(key, Value{K: KindFile, SHA256: sha, Size: size, Mode: mode}, model.Stamp{AppTime: mtimeMs})
}

// LinkItem builds a symlink item.
func LinkItem(key, target string, mtimeMs int64) (model.Item, error) {
	return newValueItem(key, Value{K: KindLink, Target: target}, model.Stamp{AppTime: mtimeMs})
}
