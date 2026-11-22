// Package runner starts a switch as a detached launchd one-shot job when
// paritydesk is invoked from inside the app's process tree (a Code session
// or the plugin), so the run survives the app quitting (D20; spike S3
// passed: a gui/<uid> job survived three quit and relaunch cycles).
//
// The plist lives in the 0700 run/ directory, names the absolute, verified
// binary path and the arguments built by orchestrator.SwitchOptions.JobArgs,
// and uses a unique label. The job writes its result file, removes its
// plist and exits; it never boots itself out (Reap does that before the
// next submit). It never assumes --yes for destructive differences: it
// compares its own plan against the plan the user confirmed (PlanStore).
package runner
