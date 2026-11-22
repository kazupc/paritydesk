// Package app observes and controls the desktop app without patching it,
// without debugging switches and without signals (design D14, D15, D20,
// ADR 0003).
//
//   - Processes are listed with sysctl (KERN_PROC_ALL, KERN_PROCARGS2), any
//     install path, both --user-data-dir forms. Helpers that live in the
//     bundle but are not the app (Chrome's native messaging host, which is
//     a child of Chrome; the updater; the crash handler) are excluded from
//     the app's process set.
//   - Open files under a folder are found with a fixed-argv lsof (a native
//     libproc scan is a later optimization).
//   - The LevelDB LOCK is probed with fcntl F_GETLK.
//   - The app is quit only with the quit Apple Event to its bundle id. A
//     veto ("Claude is still working") is waited on up to the timeout; a
//     timeout aborts with nothing written. paritydesk never sends SIGTERM or
//     SIGKILL to the app.
//   - The app is launched with open -b, and a session is reopened with a
//     claude:// link rebuilt from a validated session id.
//   - main.log is read from a byte offset for the manifest's signal lines.
//
// exec.go is the only file in the module that runs processes.
package app
