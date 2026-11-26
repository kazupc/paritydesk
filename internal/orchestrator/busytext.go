package orchestrator

// English texts of the log-signal item (busy detection, account changes
// inside a folder, the Chrome pairing organization). The orchestrator
// imports no i18n yet, so each text is a named constant here; the comment
// names its future catalog id (the constant without "txt" is the future
// catalog constant). The i18n-ja item moves them into the catalog under
// exactly these ids. Every argument is a validated alias, a masked id, a
// count, a duration or a sanitized title, never raw log text.
const (
	// confirm.busy_line
	txtConfirmBusyLine = "%s (%s), working for %s"
	// confirm.busy_line_no_time
	txtConfirmBusyLineNoTime = "%s (%s)"
	// confirm.busy_untitled
	txtConfirmBusyUntitled = "untitled session"
	// confirm.busy_waited
	txtConfirmBusyWaited = "waited %s"
	// progress.p3.busy_wait
	txtProgressBusyWait = "waiting for %d session(s) to finish (Ctrl-C stops without changes)"

	// warn.busy_stale
	txtWarnBusyStale = "%d session(s) started a turn more than %s ago and show no end in Claude's log; they are not waited for, and Claude asks before it quits if one is still working"
	// warn.busy_preview
	txtWarnBusyPreview = "%d session(s) are still working; the switch waits up to %s for them before it quits Claude"
	// warn.busy_truncated
	txtWarnBusyTruncated = "Claude's log is large; sessions that started long ago may not be listed"
	// warn.busy_log_unreadable (not in spec 12.2; added by the review fixes)
	txtWarnBusyLogUnreadable = "Claude's log could not be read, so working sessions are not listed; only Claude's own quit dialog protects them"
	// warn.account_window
	txtWarnAccountWindow = "Claude's log since the last switch is larger than paritydesk reads; older account changes are not seen"
	// warn.account_change
	txtWarnAccountChange = "the folder of %s was signed in to another account (%s) in Claude since the last switch; run paritydesk doctor"
	// warn.in_folder_sign_in
	txtWarnInFolderSignIn = "the folder of %s holds the account of %s: it was signed in to that account in Claude; run paritydesk doctor"
	// warn.native_multi
	txtWarnNativeMulti = "Claude's own multi-account mode looks active in the folder of %s; the next switch only swaps folders"
	// warn.swap_only_native_log
	txtWarnSwapOnlyNativeLog = "swap-only mode: the folder of %s changed accounts %d times in Claude since the last switch, so the switch will not carry anything"
	// warn.swap_only_native_history
	txtWarnSwapOnlyNativeHistory = "swap-only mode: the folder of %s has held several accounts, so the switch will not carry anything"

	// err.sync.swap_only_native_log (the cause of exit 31 for sync)
	txtErrSyncSwapOnlyNativeLog = "sync is not possible in swap-only mode: the folder of %s changed accounts %d times in Claude since the last switch"
	// err.sync.swap_only_native_history (the cause of exit 31 for sync)
	txtErrSyncSwapOnlyNativeHistory = "sync is not possible in swap-only mode: the folder of %s has held several accounts"
	// err.resolve.in_folder_sign_in
	txtErrInFolderSignIn = "the folder of %s was signed in to the account of %s in Claude; nothing was changed. In Claude, switch back to the account of %s, then run the command again; see paritydesk doctor"

	// doctor.busy_detection.ok
	txtDoctorBusyDetectionOK = "working sessions are found in Claude's log (turn start, end, interrupt and stop lines, and Claude's keep-awake hold for Code sessions); Claude's own quit dialog stays the last guard"
	// doctor.busy_detection.record
	txtDoctorBusyDetectionRecord = "working Code sessions are detected from their records before Claude quits"
	// doctor.busy_detection.partial
	txtDoctorBusyDetectionPartial = "turn starts and ends are found in Claude's log, but not every way a turn can end; such a session can look busy for up to %s"
	// doctor.busy_detection.unavailable
	txtDoctorBusyDetectionUnavailable = "busy detection unavailable: this Claude version has no confirmed busy signal, so only Claude's own quit dialog protects a working Code session"
	// doctor.busy_now.app_stopped
	txtDoctorBusyNowAppStopped = "Claude is not running, so no session is working"
	// doctor.busy_now.none
	txtDoctorBusyNowNone = "Claude's log shows no session working now"
	// doctor.busy_now.some
	txtDoctorBusyNowSome = "Claude's log shows %d session(s) working now: %s (this includes the session running doctor, if any)"
	// doctor.busy_now.no_signals (not in spec 12.2; added by the review fixes)
	txtDoctorBusyNowNoSignals = "this Claude version has no busy signal in its log, so working sessions cannot be listed; only Claude's own quit dialog protects them"
	// doctor.busy_now.unreadable (not in spec 12.2; added by the review fixes)
	txtDoctorBusyNowUnreadable = "Claude's log could not be read, so working sessions cannot be listed; only Claude's own quit dialog protects them"
	// doctor.busy_now.records (not in spec 12.2; added by the review fixes)
	txtDoctorBusyNowRecords = "%d session(s) working now by their records: %s (this includes the session running doctor, if any)"
	// doctor.busy_now.stale
	txtDoctorBusyNowStale = "%d more session(s) started a turn long ago with no end in the log"
	// doctor.busy_now.truncated
	txtDoctorBusyNowTruncated = "Claude's log is large; sessions that started long ago may not be listed"
	// doctor.account_change.unavailable
	txtDoctorAccountChangeUnavailable = "this Claude version has no known log line for an account change; paritydesk relies on the accounts and markers it recorded for each folder"
	// doctor.account_change.none
	txtDoctorAccountChangeNone = "no account change inside a data folder in the recent Claude log"
	// doctor.account_change.one
	txtDoctorAccountChangeOne = "the folder of %s (account %s) was signed in to %s at %s (from the Claude log)"
	// doctor.account_change.unknown
	txtDoctorAccountChangeUnknown = "a folder that held account %s, which no profile registers, was signed in to %s at %s (from the Claude log)"
	// doctor.account_change.in_folder
	txtDoctorAccountChangeInFolder = "Claude holds the folder of %s, signed in to the account of %s; paritydesk refuses to run until Claude uses the account of %s in that folder again"
	// doctor.account_change.native_log
	txtDoctorAccountChangeNativeLog = "the folder of %s changed accounts %d times since the last switch: Claude's own multi-account mode looks active, so the next switch only swaps folders and carries nothing"
	// doctor.account_change.native_history
	txtDoctorAccountChangeNativeHistory = "the folder of %s has held several accounts (recorded history): switches only swap folders and carry nothing"
	// doctor.account_change.unknown_time (the time of a change whose line had none)
	txtDoctorAccountChangeUnknownTime = "an unknown time"
	// doctor.chrome_pairing_org.known
	txtDoctorChromePairingOrgKnown = "Claude in Chrome pairs with the organization active in Claude (%s, from the Claude log; an organization override set by an administrator would win) and pairs again when the account or organization changes; no other organization stays pinned. Use one Chrome profile per account"
	// doctor.chrome_pairing_org.unknown
	txtDoctorChromePairingOrgUnknown = "the active organization is not in Claude's log for this launch, so it cannot be shown; Claude in Chrome always pairs with the organization active in Claude"
	// doctor.chrome_pairing_org.state
	txtDoctorChromePairingOrgState = "the last local pairing state Claude logged since it started: %s"
	// doctor.chrome_bridge.pairing_disabled
	txtDoctorChromeBridgePairingDisabled = "Claude turned off Claude in Chrome's local pairing for its last launch because its data folder was not in the usual place; paritydesk never moves it, so another tool may have started Claude with a different folder"
	// doctor.version.deprecated_key
	txtDoctorVersionDeprecatedKey = "a compatibility manifest override sets %s, which paritydesk no longer uses; remove it from the override"
)
