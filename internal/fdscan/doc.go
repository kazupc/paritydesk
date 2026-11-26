// Package fdscan lists, per process, every place a process holds a path:
// numbered descriptors, bound Unix-domain sockets, fileports, the working
// and root directory, per-thread working directories and mapped files. It
// is the native backend of paritydesk's quiescence check (design D15);
// the fixed-argv lsof in internal/app stays as the fallback.
//
// What is read: macOS's process information interface (libproc):
// proc_listallpids, proc_pidinfo, proc_pidfdinfo and proc_pidfileportinfo
// in /usr/lib/libSystem.B.dylib, and kern.proc.pid through sysctl for the
// start time, the user ids and the zombie state of each process. The same
// interface /usr/sbin/lsof uses, with the caller's privileges (lsof is
// not setuid either).
//
// Why no cgo: releases are built with CGO_ENABLED=0. The four functions
// are called through assembly trampolines and package syscall's libc call
// helper, the way golang.org/x/sys/unix calls libSystem
// (libproc_darwin.go and its .s files, the only files of the module that
// import symbols from libSystem). The build tag pd_nofdscan compiles a
// stub instead (as do all platforms other than macOS); the caller then
// uses lsof.
//
// The meaning contract: for the same kernel state, a scan reports the
// same (pid, path, access) set as `lsof -nP -w -F pfan` filtered by the
// same matcher, with one deliberate difference: a process of the caller's
// own user that could not be read completely is never reported as
// holding nothing. Such a scan fails with ErrPartial, or with ErrBlind
// when the failure is systemic (a sandbox or policy denies process
// information), instead of returning a result. errno is never decisive:
// libproc returns 0 for most failures and Go does not see errno then, so
// failures are classified with kern.proc.pid and by listing a table
// again.
//
// Before the first scan, a self-check reads the calling process only and
// compares the answers with descriptors it opened itself, so a changed
// structure layout (ErrLayout) or a denied interface (ErrBlind) is found
// before any other process is read.
//
// Privacy: every path a process holds passes through memory once, is
// compared by Options.Match and dropped unless it matches. Only matching
// records are returned; Stats holds counts only. Nothing is logged.
package fdscan
