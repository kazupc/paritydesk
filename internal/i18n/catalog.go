package i18n

// Message ids. Every user-facing string of v0.1 is listed here; the
// English text is in catalog below. Arguments are validated values only
// (aliases, counts, run ids, sanitized paths), never raw app data.
const (
	// Plan summary (render.PlanLines).
	PlanHeader       Msg = "plan.header"
	PlanHeaderApprox Msg = "plan.header.approx"
	PlanFirstSync    Msg = "plan.first_sync"
	PlanNoChanges    Msg = "plan.no_changes"
	PlanCategory     Msg = "plan.category"
	CountAdded       Msg = "count.added"
	CountUpdated     Msg = "count.updated"
	CountDeleted     Msg = "count.deleted"
	CountRevived     Msg = "count.revived"
	CountMoved       Msg = "count.moved"
	CountConflicts   Msg = "count.conflicts"
	DestructiveNone  Msg = "plan.destructive.none"
	DestructiveSome  Msg = "plan.destructive.some"
	DestructivePart  Msg = "plan.destructive.part"

	// Errors (render.Printer.Error).
	ErrHeadline Msg = "error.headline"
	ErrDetail   Msg = "error.detail"
	ErrSafe     Msg = "error.safe"
	ErrNext     Msg = "error.next"
	ErrMore     Msg = "error.more"
	Warning     Msg = "warning"

	// SessionStart hook lines (one fixed line, no arguments).
	HookRecover    Msg = "hook.recover"
	HookPendingAdd Msg = "hook.pending_add"
	HookSignIn     Msg = "hook.sign_in"
	HookDrift      Msg = "hook.drift"

	// Confirmations.
	AskPlan           Msg = "ask.plan"
	AskNewDestructive Msg = "ask.new_destructive"
	AskBaseInvalid    Msg = "ask.base_invalid"
	AskBusy           Msg = "ask.busy"
	AskOutsideSwitch  Msg = "ask.outside_switch"
	AskRecover        Msg = "ask.recover"
	AskRollback       Msg = "ask.rollback"
	AskAddBind        Msg = "ask.add_bind"
	AskPurge          Msg = "ask.purge"
	AskUninstall      Msg = "ask.uninstall"
	AskAlias          Msg = "ask.alias"
	AskManaged        Msg = "ask.managed"
	ConfirmNoTTY      Msg = "confirm.no_tty"
	Declined          Msg = "confirm.declined"
	ReadOnlyRefused   Msg = "readonly.refused"

	// Command results.
	StatusActive       Msg = "status.active"
	StatusNoActive     Msg = "status.no_active"
	StatusPrevious     Msg = "status.previous"
	StatusAppRunning   Msg = "status.app_running"
	StatusNeedsRecover Msg = "status.needs_recover"
	StatusUpdate       Msg = "status.update_staged"
	StatusGenerations  Msg = "status.generations"
	StatusDrift        Msg = "status.drift"
	StatusSignIn       Msg = "status.sign_in"
	StatusPendingAdd   Msg = "status.pending_add"
	ColAlias           Msg = "col.alias"
	ColFolder          Msg = "col.folder"
	ColActive          Msg = "col.active"
	ColAccount         Msg = "col.account"
	ColManaged         Msg = "col.managed"
	ColBase            Msg = "col.base"
	ColProblem         Msg = "col.problem"
	ColCheck           Msg = "col.check"
	ColStatus          Msg = "col.status"
	ColDetail          Msg = "col.detail"
	ColN               Msg = "col.n"
	ColRun             Msg = "col.run"
	ColCreated         Msg = "col.created"
	ColEntries         Msg = "col.entries"
	ColLevelDB         Msg = "col.leveldb"
	ColComplete        Msg = "col.complete"
	ColSize            Msg = "col.size"
	ColKind            Msg = "col.kind"
	ColRow             Msg = "col.row"
	ColItem            Msg = "col.item"
	ColLines           Msg = "col.lines"
	ColModified        Msg = "col.modified"
	ColFrom            Msg = "col.from"
	ColTo              Msg = "col.to"
	ColCode            Msg = "col.code"
	Yes                Msg = "word.yes"
	No                 Msg = "word.no"
	Valid              Msg = "word.valid"
	Invalid            Msg = "word.invalid"

	ProfileAdopted Msg = "profile.adopted"
	ProfileAdded   Msg = "profile.added"
	ProfileRenamed Msg = "profile.renamed"
	ProfileRemoved Msg = "profile.removed"
	ProfileNone    Msg = "profile.none"
	ChromeSetup    Msg = "chrome.setup"

	SwitchDone        Msg = "switch.done"
	SwitchDetached    Msg = "switch.detached"
	SwitchQuitPending Msg = "switch.quit_pending"
	SwitchMode        Msg = "switch.mode"
	SwitchGeneration  Msg = "switch.generation"
	SwitchJobResult   Msg = "switch.job_result"
	SyncDone          Msg = "sync.done"
	PlanSaved         Msg = "plan.saved"
	PlanNotSaved      Msg = "plan.not_saved"

	VerifyOK       Msg = "verify.ok"
	VerifyDrift    Msg = "verify.drift"
	VerifyReplaced Msg = "verify.replaced"
	VerifySignIn   Msg = "verify.sign_in"
	VerifyFocused  Msg = "verify.focused"
	VerifyBridge   Msg = "verify.bridge"

	RecoverAction Msg = "recover.action"
	RecoverActive Msg = "recover.active"
	RecoverNone   Msg = "recover.none"

	RollbackDone     Msg = "rollback.done"
	RollbackConflict Msg = "rollback.conflict"
	RebaseDone       Msg = "rebase.done"

	DoctorHookBinary   Msg = "doctor.hook_binary"
	DoctorBundleDone   Msg = "doctor.bundle_done"
	DoctorBundleExists Msg = "doctor.bundle_exists"
	DoctorBundleInData Msg = "doctor.bundle_in_data"
	InitDone           Msg = "init.done"

	GenerationsNone Msg = "generations.none"
	GenerationShow  Msg = "generation.show"
	BackupsPruned   Msg = "backups.pruned"
	BackupsPurged   Msg = "backups.purged"

	ConfigPath  Msg = "config.path"
	ConfigValid Msg = "config.valid"
	ConfigNone  Msg = "config.none"

	LogNone     Msg = "log.none"
	HistoryNone Msg = "history.none"

	UninstallRemoved  Msg = "uninstall.removed"
	UninstallKept     Msg = "uninstall.kept"
	UninstallNothing  Msg = "uninstall.nothing"
	UninstallMoveHint Msg = "uninstall.move_hint"
	UninstallMove     Msg = "uninstall.move"
	UninstallPlugin   Msg = "uninstall.plugin"
	UninstallFolders  Msg = "uninstall.folders"
	UninstallNoStatus Msg = "uninstall.no_status"

	Progress Msg = "progress"

	// v0.2 anchor: busy
	AskBusyStill       Msg = "ask.busy_still"
	PlanBusyHeader     Msg = "plan.busy_header"
	PlanBusyLine       Msg = "plan.busy_line"
	PlanBusyLineNoTime Msg = "plan.busy_line_no_time"
	PlanBusyStale      Msg = "plan.busy_stale"
	PlanBusyTitles     Msg = "plan.busy_titles"
	PlanBusyTitled     Msg = "plan.busy_titled"

	// v0.2 anchor: fdscan
	DoctorFDScanNative       Msg = "doctor.fdscan_native"
	DoctorFDScanLsofConfig   Msg = "doctor.fdscan_lsof_config"
	DoctorFDScanFallback     Msg = "doctor.fdscan_fallback"
	DoctorFDScanNativeFailed Msg = "doctor.fdscan_native_failed"
	DoctorFDScanNone         Msg = "doctor.fdscan_none"
	DoctorFDScanLsofTest     Msg = "doctor.fdscan_lsof_test"
	DoctorFDScanBlind        Msg = "doctor.fdscan_blind"
	DoctorFDScanPartial      Msg = "doctor.fdscan_partial"

	// v0.2 anchor: sidebar-offline
	VerifyUnverified Msg = "verify.unverified"

	// v0.2 anchor: scratch

	// v0.2 anchor: online

	// v0.2 anchor: pins

	// v0.2 anchor: artifacts

	// v0.2 anchor: legacy-adopt

	// v0.2 anchor: i18n-ja

)

// catalog is the English text of every message id above.
var catalog = map[Msg]string{
	PlanHeader:       "Plan: %s to %s",
	PlanHeaderApprox: "Plan: %s to %s (approximate until Claude quits)",
	PlanFirstSync:    "First sync: the data of both profiles is merged and nothing is deleted.",
	PlanNoChanges:    "No changes; the switch only quits, swaps folders and relaunches.",
	PlanCategory:     "  %-13s %s",
	CountAdded:       "+%d new",
	CountUpdated:     "~%d updated",
	CountDeleted:     "-%d deleted",
	CountRevived:     "%d revived",
	CountMoved:       "%d moved",
	CountConflicts:   "%d conflicts",
	DestructiveNone:  "DESTRUCTIVE: none",
	DestructiveSome:  "DESTRUCTIVE: %d deletions (%s)",
	DestructivePart:  "%s %d",

	ErrHeadline: "paritydesk: %s (%s)",
	ErrDetail:   "  %s",
	ErrSafe:     "  Safe now: %s",
	ErrNext:     "  Next: %s",
	ErrMore:     "  More: docs/if-something-goes-wrong.md#%s",
	Warning:     "warning: %s",

	HookRecover:    "paritydesk: a profile switch was interrupted. Run `paritydesk recover` in Terminal before switching again.",
	HookPendingAdd: "paritydesk: a new profile is waiting for its first sign-in in Claude. Run `paritydesk status` in Terminal for the next step.",
	HookSignIn:     "paritydesk: the active profile needs a sign-in with its own account. Run `paritydesk status` in Terminal for details.",
	HookDrift:      "paritydesk: after the last switch, Claude replaced some synced values. Run `paritydesk verify` in Terminal for details.",

	AskPlan:           "Apply this plan? [y/N] ",
	AskNewDestructive: "The final plan deletes more than the preview showed. Continue? [y/N] ",
	AskBaseInvalid:    "This is a first sync: both profiles are merged and nothing is deleted. Continue? [y/N] ",
	AskBusy:           "These sessions are still working. Wait for them to finish, then quit Claude? [y/N] ",
	AskOutsideSwitch:  "Claude was switched outside paritydesk. Sync the active profile now? [y/N] ",
	AskRecover:        "Make this change to finish the interrupted run? [y/N] ",
	AskRollback:       "Restore this generation? Newer changes are kept as conflicts. [y/N] ",
	AskAddBind:        "Bind the account now in Claude to the pending profile? [y/N] ",
	AskPurge:          "Remove every backup generation? This cannot be undone. [y/N] ",
	AskUninstall:      "Remove paritydesk's state? Every data folder is kept. [y/N] ",
	AskAlias:          "Alias for the account in Claude now (a-z, 0-9, -): ",
	AskManaged:        "Is the account of %s managed by an employer or client? Preset secrets never cross a managed account's boundary. [y/N] ",
	ConfirmNoTTY:      "no terminal to ask; run the command in Terminal, or pass --yes",
	Declined:          "declined; nothing was changed",
	ReadOnlyRefused:   "this command writes and cannot run in read-only mode",

	StatusActive:       "Active profile: %s",
	StatusNoActive:     "No profile is active.",
	StatusPrevious:     "Previous profile: %s",
	StatusAppRunning:   "Claude is running.",
	StatusNeedsRecover: "A run was interrupted: run paritydesk recover.",
	StatusUpdate:       "A Claude update is staged and installs on the next quit.",
	StatusGenerations:  "Backup generations: %d",
	StatusDrift:        "The last verify found drift in: %s",
	StatusSignIn:       "The active profile needs a sign-in with its own account.",
	StatusPendingAdd:   "A profile add is waiting for its first sign-in.",
	ColAlias:           "ALIAS",
	ColFolder:          "FOLDER",
	ColActive:          "ACTIVE",
	ColAccount:         "ACCOUNT",
	ColManaged:         "MANAGED",
	ColBase:            "BASE",
	ColProblem:         "PROBLEM",
	ColCheck:           "CHECK",
	ColStatus:          "STATUS",
	ColDetail:          "DETAIL",
	ColN:               "N",
	ColRun:             "RUN",
	ColCreated:         "CREATED",
	ColEntries:         "ENTRIES",
	ColLevelDB:         "LEVELDB",
	ColComplete:        "COMPLETE",
	ColSize:            "SIZE",
	ColKind:            "KIND",
	ColRow:             "ROW",
	ColItem:            "ITEM",
	ColLines:           "LINES",
	ColModified:        "MODIFIED",
	ColFrom:            "FROM",
	ColTo:              "TO",
	ColCode:            "CODE",
	Yes:                "yes",
	No:                 "no",
	Valid:              "valid",
	Invalid:            "invalid",

	ProfileAdopted: "Profile %s registered.",
	ProfileAdded:   "Profile %s added. Sign in to the new account in Claude; every other account stays signed in.",
	ProfileRenamed: "Profile %s renamed to %s.",
	ProfileRemoved: "Profile %s unregistered; its folder was kept.",
	ProfileNone:    "No profiles yet: run paritydesk init.",
	ChromeSetup:    "Claude in Chrome follows the Chrome profile it is signed in to: use one Chrome profile per Claude account.",

	SwitchDone:        "Switched from %s to %s.",
	SwitchDetached:    "The switch runs as background job %s. Claude quits once no session is working.",
	SwitchQuitPending: "Claude has not quit yet; your answer in Claude decides. Nothing was changed.",
	SwitchMode:        "Mode: %s",
	SwitchGeneration:  "Backup generation: %d",
	SwitchJobResult:   "The background job's result file could not be written; paritydesk doctor cleans up the job later.",
	SyncDone:          "Synced profile %s.",
	PlanSaved:         "Plan saved for --confirmed-plan.",
	PlanNotSaved:      "Read-only mode: the plan was not saved.",

	VerifyOK:       "Verify: nothing unexpected after launch.",
	VerifyDrift:    "Verify: Claude replaced values in: %s",
	VerifyReplaced: "Verify: Claude or you changed these since the switch; nothing to do: %s",
	VerifySignIn:   "Verify: the active profile needs a sign-in with its own account.",
	VerifyFocused:  "Verify: the invoking session was reopened.",
	VerifyBridge:   "Verify: Claude in Chrome connected after the launch.",

	RecoverAction: "  %s",
	RecoverActive: "Active profile: %s",
	RecoverNone:   "Nothing to recover.",

	RollbackDone:     "Restored generation %d: %d entries.",
	RollbackConflict: "Kept newer value (conflict): %s",
	RebaseDone:       "Every sync base was dropped; the next switch is a first sync.",

	DoctorHookBinary:   "Recorded this binary for the plugin hook: %s",
	DoctorBundleDone:   "Support archive written: %s (mode 0600). Read it before you attach it to an issue.",
	DoctorBundleExists: "The file already exists; choose a new name.",
	DoctorBundleInData: "The support archive must not be written inside a Claude data folder.",
	InitDone:           "paritydesk is set up; the account in Claude is profile %s.",

	GenerationsNone: "No backup generations.",
	GenerationShow:  "Generation %d (run %s, profile %s, created %s, complete: %s)",
	BackupsPruned:   "Backups pruned (keep %d generations).",
	BackupsPurged:   "Every backup generation was removed.",

	ConfigPath:  "%s",
	ConfigValid: "Config is valid: %s",
	ConfigNone:  "No config file; showing the defaults. Run paritydesk init to create it.",

	LogNone:     "No runs have been logged yet.",
	HistoryNone: "No runs have been logged yet.",

	UninstallRemoved:  "Removed: %s",
	UninstallKept:     "Kept: %s",
	UninstallNothing:  "Nothing to remove.",
	UninstallMoveHint: "No profile is active. With Claude quit, this one command makes a parked profile active again:",
	UninstallMove:     "  mv %s %s",
	UninstallPlugin:   "To remove the Claude Code plugin, uninstall paritydesk from Claude Code's plugin manager.",
	UninstallFolders:  "Every data folder was kept; every account stays signed in.",
	UninstallNoStatus: "The active profile could not be read; check that Claude starts on the right account before reinstalling.",

	Progress: "%s %-4s %s",

	// v0.2 anchor: busy
	AskBusyStill:       "These sessions still look busy. Quit Claude now anyway? Claude asks before it quits a working session. [y/N] ",
	PlanBusyHeader:     "Still working (%d): the switch waits up to %s, and Claude asks before it quits a working session",
	PlanBusyLine:       "%s, working for %s",
	PlanBusyLineNoTime: "%s, working",
	PlanBusyStale:      "%d more session(s) look stuck and are not waited for",
	PlanBusyTitles:     "Working sessions:",
	PlanBusyTitled:     "%s (%s)",

	// v0.2 anchor: fdscan
	DoctorFDScanNative:       "Open files: checked with macOS's process information interface (libproc).",
	DoctorFDScanLsofConfig:   `Open files: checked with lsof, as app.open_file_scan = "lsof" asks.`,
	DoctorFDScanFallback:     "Open files: the native check is not available here, so lsof is used. Switches still work; please report this with paritydesk doctor --bundle.",
	DoctorFDScanNativeFailed: `Open files: the native check is not available here and app.open_file_scan is "native", so switches stop before writing anything. Set it to "auto".`,
	DoctorFDScanNone:         "Open files: neither the native check nor /usr/sbin/lsof is available, so paritydesk cannot confirm that Claude closed its files; switches stop before writing anything.",
	DoctorFDScanLsofTest:     "Open files: checked with lsof (test build).",
	DoctorFDScanBlind:        "Open files: paritydesk cannot read other processes' open files here (is it running inside a sandbox?), so switches stop before writing anything. Run paritydesk from Terminal.",
	DoctorFDScanPartial:      "Open files: %d process(es) could not be read and %d only in part, so lsof is used for those checks. Switches still work.",

	// v0.2 anchor: sidebar-offline
	VerifyUnverified: "Verify: these could not be read back while Claude runs; they are checked at the next switch: %s",

	// v0.2 anchor: scratch

	// v0.2 anchor: online

	// v0.2 anchor: pins

	// v0.2 anchor: artifacts

	// v0.2 anchor: legacy-adopt

	// v0.2 anchor: i18n-ja

}
