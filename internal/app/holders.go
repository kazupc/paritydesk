package app

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strconv"
)

// Writes reports whether the holder blocks quiescence: a write or
// read-write descriptor, a working directory or a mapped executable under
// the watched prefix. Read-only descriptors (Spotlight, backup daemons)
// are warnings only (D15).
func (h Holder) Writes() bool { return h.Access != "r" }

// lsofHolders finds holders with the fixed lsof template and filters the
// records by path prefix in Go, so no path reaches the command line.
type lsofHolders struct {
	run   Runner
	procs ProcessTable // names the holders' commands; may be nil
}

// Holders lists processes with a descriptor, cwd or mapped file at or
// under one of prefixes.
func (h lsofHolders) Holders(ctx context.Context, prefixes []string) ([]Holder, error) {
	out, err := h.run.Run(ctx, TmplLsof)
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("app: list open files: %w", err)
	}
	holders := parseLsof(out, newPathMatcher(prefixes).Match)
	if len(holders) == 0 || h.procs == nil {
		return holders, nil
	}
	procs, err := h.procs.Processes(ctx)
	if err != nil {
		return holders, nil // names are cosmetic
	}
	names := make(map[int]string, len(procs))
	for _, p := range procs {
		names[p.PID] = filepath.Base(p.Exe)
	}
	for i := range holders {
		holders[i].Command = names[holders[i].PID]
	}
	return holders, nil
}

// parseLsof parses `lsof -F pfan` output: a 'p' line starts a process, an
// 'f' line starts a file, 'a' gives the access mode of a numbered
// descriptor and 'n' the name. Only files whose name match accepts (the
// shared pathMatcher) are returned.
func parseLsof(out []byte, match func(string) bool) []Holder {
	var (
		holders []Holder
		pid     int
		fd      string
		access  string
	)
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		val := line[1:]
		switch line[0] {
		case 'p':
			n, err := strconv.Atoi(val)
			if err != nil {
				n = 0
			}
			pid, fd, access = n, "", ""
		case 'f':
			fd, access = val, ""
		case 'a':
			access = val
		case 'n':
			if pid <= 0 || fd == "" {
				continue
			}
			mode, ok := lsofAccess(fd, access)
			if !ok || !match(val) {
				continue
			}
			holders = append(holders, Holder{PID: pid, Path: val, Access: mode})
		}
	}
	return holders
}

// lsofAccess maps an lsof descriptor and access field to Holder.Access.
// Working, root and thread working directories are "cwd"; unknown access
// on a numbered descriptor counts as read-write; lsof's error records
// (NOFD) are skipped.
func lsofAccess(fd, access string) (string, bool) {
	switch fd {
	case "cwd", "rtd", "twd":
		return "cwd", true
	case "txt", "mem":
		return "txt", true
	case "NOFD":
		return "", false
	}
	switch access {
	case "r", "w", "u":
		return access, true
	}
	return "u", true
}
