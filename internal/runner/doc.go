// Package runner starts a switch as a detached launchd one-shot job when
// paritydesk is invoked from inside the app's process tree (a Code session
// or the plugin), so the run survives the app quitting (D20; spike S3
// passed: a gui/<uid> job survived three quit and relaunch cycles).
//
// The plist lives in the 0700 run/ directory, names the absolute, verified
// binary path and fixed arguments, uses a unique label, and is removed
// after the job finishes. The job writes a result file and posts a
// notification; it never assumes --yes for destructive differences.
package runner
