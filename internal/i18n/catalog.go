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
	// Scratch workspaces: plan notices (render.ScratchNotices) and the
	// run's progress warnings.
	ScratchOversize         Msg = "scratch.oversize"
	ScratchBudget           Msg = "scratch.budget"
	ScratchRootLink         Msg = "scratch.root_link"
	ScratchUnreadable       Msg = "scratch.unreadable"
	ScratchHeldBack         Msg = "scratch.held_back"
	ScratchConflictCopy     Msg = "scratch.conflict_copy"
	ScratchGitConflict      Msg = "scratch.git_conflict"
	ScratchGitDiverged      Msg = "scratch.git_diverged"
	ScratchTypeConflict     Msg = "scratch.type_conflict"
	ScratchLinkKept         Msg = "scratch.link_kept"
	ScratchNameCollision    Msg = "scratch.name_collision"
	ScratchReservedName     Msg = "scratch.reserved_name"
	ScratchUnsupportedName  Msg = "scratch.unsupported_name"
	ScratchSpecial          Msg = "scratch.special"
	ScratchBytesUnavailable Msg = "scratch.bytes_unavailable"
	ScratchDeletionsOff     Msg = "scratch.deletions_off"
	ScratchDeletionDeferred Msg = "scratch.deletion_deferred"
	ScratchQuarantined      Msg = "scratch.quarantined"
	ScratchSourceChanged    Msg = "scratch.source_changed"
	ScratchMissing          Msg = "scratch.missing"
	ScratchUnrecognized     Msg = "scratch.unrecognized"
	ScratchNoSpace          Msg = "scratch.no_space"
	ScratchManaged          Msg = "scratch.managed"
	ScratchPreviewLimit     Msg = "scratch.preview_limit"
	// Scratch workspaces: doctor and CLI.
	DoctorScratch            Msg = "doctor.scratch"
	DoctorScratchUnsupported Msg = "doctor.scratch_unsupported"
	DoctorScratchOrphans     Msg = "doctor.scratch_orphans"
	DoctorScratchLimits      Msg = "doctor.scratch_limits"
	DoctorScratchKept        Msg = "doctor.scratch_kept"
	CliOnlyScratch           Msg = "cli.only_scratch"
	BackupsScratchConfirm    Msg = "backups.scratch_confirm"
	BackupsScratchRemoved    Msg = "backups.scratch_removed"
	// Scratch workspaces: texts added after review (same anchor).
	ScratchReadOnly             Msg = "scratch.read_only"
	ScratchStatCacheUnsaved     Msg = "scratch.stat_cache_unsaved"
	DoctorScratchRootNotDir     Msg = "doctor.scratch_root_not_dir"
	DoctorScratchSizeMore       Msg = "doctor.scratch_size_more"
	DoctorScratchOrphansNone    Msg = "doctor.scratch_orphans_none"
	DoctorScratchOrphansUsed    Msg = "doctor.scratch_orphans_used"
	DoctorScratchOrphansNA      Msg = "doctor.scratch_orphans_na"
	DoctorScratchOrphansUnread  Msg = "doctor.scratch_orphans_unreadable"
	DoctorScratchOrphansMore    Msg = "doctor.scratch_orphans_more"
	DoctorScratchKeptNone       Msg = "doctor.scratch_kept_none"
	DoctorScratchKeptUnreadable Msg = "doctor.scratch_kept_unreadable"

	// v0.2 anchor: online
	HookOnlinePrompt             Msg = "hook.online_prompt"
	HookOnlineAuto               Msg = "hook.online_auto"
	GuardDenyFloor               Msg = "guard.deny_floor"
	GuardDenyUnplanned           Msg = "guard.deny_unplanned"
	GuardDenyNotYet              Msg = "guard.deny_not_yet"
	GuardDenyUnreadable          Msg = "guard.deny_unreadable"
	GuardDenyDuplicate           Msg = "guard.deny_duplicate"
	GuardDenyArtifactTool        Msg = "guard.deny_artifact_tool"
	OnlineArtifactsQueue         Msg = "online.artifacts_queue"
	OnlineQPinsHeader            Msg = "online.q_pins_header"
	OnlineQPinsText              Msg = "online.q_pins_text"
	OnlineQPinsApply             Msg = "online.q_pins_apply"
	OnlineQPinsApplyDesc         Msg = "online.q_pins_apply_desc"
	OnlineQPinsApplyAll          Msg = "online.q_pins_apply_all"
	OnlineQPinsAddOnly           Msg = "online.q_pins_add_only"
	OnlineQPinsAddOnlyDesc       Msg = "online.q_pins_add_only_desc"
	OnlineQPinsSkip              Msg = "online.q_pins_skip"
	OnlineQPinsSkipDesc          Msg = "online.q_pins_skip_desc"
	OnlineStatusNone             Msg = "online.status_none"
	OnlineStatusPending          Msg = "online.status_pending"
	OnlineStatusOther            Msg = "online.status_other"
	OnlineStatusStale            Msg = "online.status_stale"
	OnlineStatusReplicaChanged   Msg = "online.status_replica_changed"
	OnlineStatusDenials          Msg = "online.status_denials"
	OnlineStatusState            Msg = "online.status_state"
	OnlineNoHooks                Msg = "online.no_hooks"
	OnlineBusyElsewhere          Msg = "online.busy_elsewhere"
	OnlineLoopBound              Msg = "online.loop_bound"
	OnlineErrSupportDir          Msg = "online.err_support_dir"
	OnlineDoneSummary            Msg = "online.done_summary"
	OnlineDoneFailedHint         Msg = "online.done_failed_hint"
	OnlineDoneRootBusy           Msg = "online.done_root_busy"
	OnlineCancelled              Msg = "online.cancelled"
	OnlineErrBusy                Msg = "online.err_busy"
	OnlineErrReadOnly            Msg = "online.err_read_only"
	OnlineErrQueueInvalid        Msg = "online.err_queue_invalid"
	OnlineWarnPinsUnreadable     Msg = "online.warn_pins_unreadable"
	OnlineWarnPinsCapped         Msg = "online.warn_pins_capped"
	OnlineWarnBuildFailed        Msg = "online.warn_build_failed"
	OnlineWarnSidebarNotWritable Msg = "online.warn_sidebar_not_writable"
	OnlineRetried                Msg = "online.retried"
	OnlineCaptureDone            Msg = "online.capture_done"
	OnlineShort                  Msg = "online.short"
	PlanOnlineLine               Msg = "plan.online_line"
	PlanOnlinePinsUnreadable     Msg = "plan.online_pins_unreadable"
	SwitchOnlineNext             Msg = "switch.online_next"
	DoctorOnlineSupportDir       Msg = "doctor.online_support_dir"
	DoctorOnlineQueue            Msg = "doctor.online_queue"
	DoctorOnlineOff              Msg = "doctor.online_off"

	// v0.2 anchor: pins
	OnlineQPinsKeep                   Msg = "online.q_pins_keep"
	OnlineQPinsUnion                  Msg = "online.q_pins_union"
	OnlineQPinsBlocked                Msg = "online.q_pins_blocked"
	OnlineWarnPinsReason              Msg = "online.warn_pins_reason"
	OnlineWarnPinsFamilyBlocked       Msg = "online.warn_pins_family_blocked"
	OnlineWarnPinsHidden              Msg = "online.warn_pins_hidden"
	OnlineWarnPinsMirror              Msg = "online.warn_pins_mirror"
	OnlineRetryPinsRemovalLate        Msg = "online.retry_pins_removal_late"
	PlanOnlinePinsPreviewUnavailable  Msg = "plan.online_pins_preview_unavailable"
	DoctorOnlinePinsIDBCount          Msg = "doctor.online_pins_idb_count"
	DoctorOnlinePinsMirrorCount       Msg = "doctor.online_pins_mirror_count"
	DoctorOnlinePinsUnavailableReason Msg = "doctor.online_pins_unavailable_reason"
	DoctorOnlinePinsStale             Msg = "doctor.online_pins_stale"
	DoctorOnlinePinsMirrorDiffers     Msg = "doctor.online_pins_mirror_differs"
	DoctorOnlinePinsOff               Msg = "doctor.online_pins_off"
	PinsReasonDirAbsent               Msg = "pins.reason.dir_absent"
	PinsReasonKeyAbsent               Msg = "pins.reason.key_absent"
	PinsReasonAmbiguous               Msg = "pins.reason.ambiguous"
	PinsReasonUnsupported             Msg = "pins.reason.unsupported"
	PinsReasonBlobWrapped             Msg = "pins.reason.blob_wrapped"
	PinsReasonVersion                 Msg = "pins.reason.version"
	PinsReasonTorn                    Msg = "pins.reason.torn"
	PinsReasonUnclean                 Msg = "pins.reason.unclean"
	PinsReasonPending                 Msg = "pins.reason.pending"
	PinsReasonTooLarge                Msg = "pins.reason.too_large"
	PinsReasonBackend                 Msg = "pins.reason.backend"
	PinsReasonFormat                  Msg = "pins.reason.format"
	PinsReasonStaleStore              Msg = "pins.reason.stale_store"
	PinsReasonOther                   Msg = "pins.reason.other"

	// v0.2 anchor: artifacts

	// v0.2 anchor: legacy-adopt
	LegacyHeaderDry                  Msg = "legacy.header.dry"
	LegacyHeaderApply                Msg = "legacy.header.apply"
	LegacyFolderAdopt                Msg = "legacy.folder.adopt"
	LegacyFolderActive               Msg = "legacy.folder.active"
	LegacyFolderFinish               Msg = "legacy.folder.finish"
	LegacyFolderRegistered           Msg = "legacy.folder.registered"
	LegacyFolderSkip                 Msg = "legacy.folder.skip"
	LegacyOrgUnknown                 Msg = "legacy.org_unknown"
	LegacyReasonExcluded             Msg = "legacy.reason.excluded"
	LegacyReasonUnsupportedName      Msg = "legacy.reason.unsupported_name"
	LegacyReasonReserved             Msg = "legacy.reason.reserved"
	LegacyReasonUnreadable           Msg = "legacy.reason.unreadable"
	LegacyReasonForeign              Msg = "legacy.reason.foreign"
	LegacyReasonNoIdentity           Msg = "legacy.reason.no_identity"
	LegacyReasonDuplicate            Msg = "legacy.reason.duplicate"
	LegacyReasonNoAlias              Msg = "legacy.reason.no_alias"
	LegacyReasonNeedsAlias           Msg = "legacy.reason.needs_alias"
	LegacyReasonAliasTaken           Msg = "legacy.reason.alias_taken"
	LegacyReasonAliasCollision       Msg = "legacy.reason.alias_collision"
	LegacyReasonParkCollision        Msg = "legacy.reason.park_collision"
	LegacyLeftoverHeader             Msg = "legacy.leftover.header"
	LegacyLeftoverItem               Msg = "legacy.leftover.item"
	LegacyLeftoverUnique             Msg = "legacy.leftover.unique"
	LegacyKindOtherAccount           Msg = "legacy.kind.other_account"
	LegacyKindUnrecognized           Msg = "legacy.kind.unrecognized"
	LegacyKindMixedOrgStub           Msg = "legacy.kind.mixed_org_stub"
	LegacyKindMixedOrg               Msg = "legacy.kind.mixed_org"
	LegacyActionEligible             Msg = "legacy.action.eligible"
	LegacyActionReportOnly           Msg = "legacy.action.report_only"
	LegacyActionDeferred             Msg = "legacy.action.deferred"
	LegacyActionMoved                Msg = "legacy.action.moved"
	LegacyActionFailed               Msg = "legacy.action.failed"
	LegacyWhyUnrecognized            Msg = "legacy.why.unrecognized"
	LegacyWhyShortID                 Msg = "legacy.why.short_id"
	LegacyWhyShortIDUnknown          Msg = "legacy.why.short_id_unknown"
	LegacyWhyHoldsData               Msg = "legacy.why.holds_data"
	LegacyWhyUniqueSessions          Msg = "legacy.why.unique_sessions"
	LegacyWhySymlink                 Msg = "legacy.why.symlink"
	LegacyWhyTooLarge                Msg = "legacy.why.too_large"
	LegacyWhyScratch                 Msg = "legacy.why.scratch"
	LegacyWhyOrgUnknown              Msg = "legacy.why.org_unknown"
	LegacyWhyOtherVolume             Msg = "legacy.why.other_volume"
	LegacyWhyAppRunning              Msg = "legacy.why.app_running"
	LegacyWhyInUse                   Msg = "legacy.why.in_use"
	LegacyWhyFolderChanged           Msg = "legacy.why.folder_changed"
	LegacyWhyDenied                  Msg = "legacy.why.denied"
	LegacyWhyMoveFailed              Msg = "legacy.why.move_failed"
	LegacyLeftoverOffer              Msg = "legacy.leftover.offer"
	LegacySeedHeader                 Msg = "legacy.seed.header"
	LegacySeedRow                    Msg = "legacy.seed.row"
	LegacySeedRowSkipped             Msg = "legacy.seed.row_skipped"
	LegacySeedNone                   Msg = "legacy.seed.none"
	LegacySeedFirstSwitch            Msg = "legacy.seed.first_switch"
	LegacySeedReasonNoSeedFlag       Msg = "legacy.seed.reason.no_seed_flag"
	LegacySeedReasonNoManifest       Msg = "legacy.seed.reason.no_manifest"
	LegacySeedReasonTooFew           Msg = "legacy.seed.reason.too_few"
	LegacySeedReasonHasBases         Msg = "legacy.seed.reason.has_bases"
	LegacySeedReasonHasCanonical     Msg = "legacy.seed.reason.has_canonical"
	LegacySeedReasonRunOpen          Msg = "legacy.seed.reason.run_open"
	LegacySeedReasonHintMidSwitch    Msg = "legacy.seed.reason.hint_mid_switch"
	LegacySeedReasonDuplicateAccount Msg = "legacy.seed.reason.duplicate_account"
	LegacySeedReasonProbeFailed      Msg = "legacy.seed.reason.probe_failed"
	LegacySeedReasonOrgUnknown       Msg = "legacy.seed.reason.org_unknown"
	LegacySeedReasonUnreadable       Msg = "legacy.seed.reason.unreadable"
	LegacySeedReasonNoAdapter        Msg = "legacy.seed.reason.no_adapter"
	LegacyHintUsed                   Msg = "legacy.hint.used"
	LegacyHintStale                  Msg = "legacy.hint.stale"
	LegacyHintMidSwitch              Msg = "legacy.hint.mid_switch"
	LegacyHintDropped                Msg = "legacy.hint.dropped"
	LegacyWarnRunOpen                Msg = "legacy.warn.run_open"
	LegacyWarnPendingAdd             Msg = "legacy.warn.pending_add"
	LegacyWarnNoActive               Msg = "legacy.warn.no_active"
	LegacyWarnNoActiveIdentity       Msg = "legacy.warn.no_active_identity"
	LegacyWarnMarker                 Msg = "legacy.warn.marker"
	LegacyWarnForeign                Msg = "legacy.warn.foreign"
	LegacyWarnFolderChanged          Msg = "legacy.warn.folder_changed"
	LegacyWarnLeftoversUnreadable    Msg = "legacy.warn.leftovers_unreadable"
	LegacyWarnPrintedDiffers         Msg = "legacy.warn.printed_differs"
	LegacyPlanHash                   Msg = "legacy.plan_hash"
	LegacyPlanQuarantineTerminal     Msg = "legacy.plan_quarantine_terminal"
	LegacyDryRun                     Msg = "legacy.dry_run"
	LegacyNothing                    Msg = "legacy.nothing"
	LegacyAdopted                    Msg = "legacy.adopted"
	LegacyQuarantined                Msg = "legacy.quarantined"
	LegacyNextHeader                 Msg = "legacy.next.header"
	LegacyNextRetire                 Msg = "legacy.next.retire"
	LegacyNextSkill                  Msg = "legacy.next.skill"
	LegacyNextBackups                Msg = "legacy.next.backups"
	LegacyNextPlugin                 Msg = "legacy.next.plugin"
	LegacyNextPlan                   Msg = "legacy.next.plan"
	LegacyDestructive                Msg = "legacy.destructive"
	LegacyDestructiveDry             Msg = "legacy.destructive.dry"
	AskLegacyAliasActive             Msg = "ask.legacy_alias_active"
	AskLegacyAdopt                   Msg = "ask.legacy_adopt"
	AskLegacyAdoptQuarantine         Msg = "ask.legacy_adopt_quarantine"
	ErrorLegacyPlanRequired          Msg = "error.legacy_plan_required"
	ErrorLegacyPlanChanged           Msg = "error.legacy_plan_changed"
	ErrorLegacyPlanFlags             Msg = "error.legacy_plan_flags"
	ErrorLegacyQuarantineTerminal    Msg = "error.legacy_quarantine_terminal"
	PlanFirstSyncSeeded              Msg = "plan.first_sync_seeded"
	AskFirstSyncSeeded               Msg = "ask.first_sync_seeded"
	BackupsPinnedWarn                Msg = "backups.pinned_warn"
	RollbackQuarantineLine           Msg = "rollback.quarantine_line"

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
	ScratchOversize:         "Scratch workspace %s was not carried: it is over the limit of %d files or %s. Raise the limit in [scratch], or tidy the workspace.",
	ScratchBudget:           "%d scratch workspaces were not carried this time: together they exceed the per-switch limit (%s or %d files).",
	ScratchRootLink:         "Scratch workspace %s was not carried: its folder path contains a symbolic link.",
	ScratchUnreadable:       "Scratch workspace %s was not carried: some of its files could not be read.",
	ScratchHeldBack:         "%d dependency, build or version-control folders in scratch workspaces were not carried (for example node_modules). Reinstall or rebuild them on %s if a session needs them.",
	ScratchConflictCopy:     "%d files in scratch workspaces changed on both accounts. The newer copy is kept and the other is saved next to it with \"(conflict from <account> <date>)\" in its name.",
	ScratchGitConflict:      "The .git folder of %d scratch workspaces changed on both accounts. Each account keeps its own copy; neither is carried until they match again.",
	ScratchGitDiverged:      "The .git folder of %d scratch workspaces still differs between the accounts and is not carried. To carry one copy, make both equal (for example through a shared remote), or rename the .git folder away on the account that should receive the other copy.",
	ScratchTypeConflict:     "%d paths are a file on one account and a folder on the other. %s's version was kept unchanged.",
	ScratchLinkKept:         "%d symbolic links differ between the accounts or point outside their workspace; they were left as they are.",
	ScratchNameCollision:    "%d files were not carried because %s already has a file whose name differs only in letter case or accents.",
	ScratchReservedName:     "%d files were not carried because their names match a name the app reserves.",
	ScratchUnsupportedName:  "%d files were not carried because their names cannot be copied safely (a backslash, or a path that is too long).",
	ScratchSpecial:          "%d sockets, pipes or device files in scratch workspaces were not carried.",
	ScratchBytesUnavailable: "%d files could not be carried because neither account's folder has their current contents.",
	ScratchDeletionsOff:     "Files removed in scratch workspaces are not removed on %s (scratch.propagate_deletions is off), so they may come back.",
	ScratchDeletionDeferred: "%d files removed in scratch workspaces after the preview are left on %s for now; the removal is carried on a later switch.",
	ScratchQuarantined:      "%d files removed on %s will be moved from %s's scratch workspaces into this switch's backup generation; paritydesk rollback puts them back, and paritydesk never deletes them by itself.",
	ScratchSourceChanged:    "%d files in scratch workspaces changed while switching and were left as they were on %s; they are carried on the next switch.",
	ScratchMissing:          "%d sessions name a scratch workspace that neither account's folder has; those sessions may not find their folder.",
	ScratchUnrecognized:     "%d scratch workspace paths in session records are not in this data root and were not carried.",
	ScratchNoSpace:          "Scratch workspaces were not carried: copying them may need %s and only %s is free.",
	ScratchManaged:          "Scratch workspaces are not carried between a managed account and another account (scratch.cross_managed is off).",
	ScratchPreviewLimit:     "Sessions carried from another account's scratch workspace open here, but file previews and Browser pane previews of their files work only on the account that created them.",

	DoctorScratch:            "Scratch workspaces: %d of this account, %d carried from other accounts (%s).",
	DoctorScratchUnsupported: "Scratch workspaces are not carried with this app version (the compatibility manifest does not cover them).",
	DoctorScratchOrphans:     "%d carried scratch workspaces (%s) are no longer used by any session here. paritydesk never removes them; delete them yourself when you no longer need them.",
	DoctorScratchLimits:      "Sessions that started in another account's scratch workspace open on this account, but file previews and Browser pane previews for them work only on the account that created them.",
	DoctorScratchKept:        "%d sets of scratch files removed by earlier switches are kept (%s) in %s. Remove them with: paritydesk backups prune --scratch",
	CliOnlyScratch:           "scratch workspaces are carried with sessions: use --only sessions (add --skip scratch to leave them out).",
	BackupsScratchConfirm:    "Permanently delete %d kept sets of scratch files (%s)? This cannot be undone. [y/N] ",
	BackupsScratchRemoved:    "Removed %d kept sets of scratch files (%s).",

	ScratchReadOnly:             "Scratch workspace %s was not carried: one of its folders is read-only for you, so files cannot be written into it.",
	ScratchStatCacheUnsaved:     "the scratch stat cache could not be saved; the next switch hashes scratch files again",
	DoctorScratchRootNotDir:     "the scratch workspace root of the active folder is present but not a directory; scratch workspaces are skipped",
	DoctorScratchSizeMore:       "more than %s",
	DoctorScratchOrphansNone:    "no carried scratch workspace is unused",
	DoctorScratchOrphansUsed:    "every carried scratch workspace is used by a session",
	DoctorScratchOrphansNA:      "not applicable: scratch workspaces are not carried here",
	DoctorScratchOrphansUnread:  "the session records of the active folder could not be read",
	DoctorScratchOrphansMore:    "and %d more",
	DoctorScratchKeptNone:       "no scratch files are kept",
	DoctorScratchKeptUnreadable: "the kept scratch files could not be listed",

	// v0.2 anchor: online
	HookOnlinePrompt:             "paritydesk: some changes for this profile can only be made through Claude's own tools. Tell the user and offer to run /paritydesk:reconcile.",
	HookOnlineAuto:               "paritydesk: some changes for this profile can only be made through Claude's own tools. Run /paritydesk:reconcile before other work, unless the user asked for something else.",
	GuardDenyFloor:               "paritydesk: this tool is blocked while paritydesk applies changes in this session. Nothing was changed.",
	GuardDenyUnplanned:           "paritydesk: only the calls paritydesk listed may run now, with exactly the listed input. Run paritydesk online next for the list, or paritydesk online cancel in Terminal to stop.",
	GuardDenyNotYet:              "paritydesk: this call is listed but not ready yet. Run paritydesk online next first.",
	GuardDenyUnreadable:          "paritydesk could not check this call, so it was blocked. Run paritydesk online cancel in Terminal to stop applying changes.",
	GuardDenyDuplicate:           "paritydesk: this exact call is already running once. Wait for it, then run paritydesk online next.",
	GuardDenyArtifactTool:        "paritydesk is syncing artifacts in this session; Claude's own artifact tool is paused until it finishes. To stop the sync, run paritydesk online cancel in Terminal.",
	OnlineArtifactsQueue:         "This queue belongs to /paritydesk:artifacts.",
	OnlineQPinsHeader:            "Pins",
	OnlineQPinsText:              "To match %s, paritydesk will pin %d and unpin %d session(s) here. Claude may ask you to approve each change to another session (up to %d).",
	OnlineQPinsApply:             "Apply pin changes",
	OnlineQPinsApplyDesc:         "Claude may ask before each change to another session",
	OnlineQPinsApplyAll:          "Apply all pin changes",
	OnlineQPinsAddOnly:           "Pin only, keep current pins",
	OnlineQPinsAddOnlyDesc:       "Unusually many unpins; keep every pin you have now",
	OnlineQPinsSkip:              "Skip pins this time",
	OnlineQPinsSkipDesc:          "Pins stay as they are; the next switch offers this again",
	OnlineStatusNone:             "No changes wait to be applied in Claude.",
	OnlineStatusPending:          "%d pin(s), %d unpin(s) and %d sidebar setting(s) wait to be applied in Claude for %s.",
	OnlineStatusOther:            "%d other change(s) wait as well.",
	OnlineStatusStale:            "The waiting changes belonged to an earlier switch and were dropped. Nothing in Claude was changed.",
	OnlineStatusReplicaChanged:   "Claude is signed in to another account or organization than the one these changes are for, so they were dropped. Nothing in Claude was changed.",
	OnlineStatusDenials:          "paritydesk blocked %d tool call(s) while it applied changes.",
	OnlineStatusState:            "Changes in Claude for %s: %s (%d done, %d skipped, %d not applied).",
	OnlineNoHooks:                "paritydesk's hooks did not run in this session, so nothing was changed. Check that the paritydesk plugin is installed and hooks are enabled, then run /paritydesk:reconcile again.",
	OnlineBusyElsewhere:          "Another Claude session is applying these changes right now. Nothing was changed here.",
	OnlineLoopBound:              "The changes were not finished in this session; the next session start offers them again.",
	OnlineErrSupportDir:          "Changes inside Claude need paritydesk's default folder; they are not applied while --app-support-dir is set.",
	OnlineDoneSummary:            "Applied %d of %d change(s) in Claude; %d skipped, %d not applied.",
	OnlineDoneFailedHint:         "To try the changes that were not applied once more, run /paritydesk:reconcile again after paritydesk online retry, or drop them with paritydesk online cancel.",
	OnlineDoneRootBusy:           "paritydesk is busy with another run; it records the applied changes with the next paritydesk verify or switch.",
	OnlineCancelled:              "Dropped the waiting changes. Nothing in Claude was changed.",
	OnlineErrBusy:                "Another paritydesk command is updating the waiting changes. Try again in a moment.",
	OnlineErrReadOnly:            "Read-only mode: waiting changes are not updated.",
	OnlineErrQueueInvalid:        "The file with the waiting changes is not valid. Run paritydesk online cancel to drop it; nothing in Claude changes.",
	OnlineWarnPinsUnreadable:     "Pins were not carried this time: Claude's pin list in %s could not be read.",
	OnlineWarnPinsCapped:         "Only the first %d pin changes were queued; the next switch offers the rest.",
	OnlineWarnBuildFailed:        "Some changes inside Claude could not be planned this time; the next switch tries again.",
	OnlineWarnSidebarNotWritable: "%d sidebar section(s) have settings Claude's tools cannot set; those settings were not queued.",
	OnlineRetried:                "%d change(s) will be offered again by /paritydesk:reconcile.",
	OnlineCaptureDone:            "The capture is closed.",
	OnlineShort:                  "online:%d",
	PlanOnlineLine:               "  online        %d pin(s), %d unpin(s), %d sidebar setting(s) (applied in Claude after the switch)",
	PlanOnlinePinsUnreadable:     "  pins          not carried this time (Claude's pin list could not be read)",
	SwitchOnlineNext:             "Some changes can only be made inside Claude: open a Code session and run /paritydesk:reconcile.",
	DoctorOnlineSupportDir:       "Changes inside Claude are off: --app-support-dir is not the default folder",
	DoctorOnlineQueue:            "Waiting changes in Claude: %s",
	DoctorOnlineOff:              "Changes inside Claude are off (online.reconcile = \"off\", or this Claude version has no online support)",

	// v0.2 anchor: pins
	OnlineQPinsKeep:                   "Unpinning a session can also unpin the sessions under it; paritydesk then pins %d of them again.",
	OnlineQPinsUnion:                  "This is the first time pins are carried between these profiles: the pins of both are combined, and nothing is unpinned.",
	OnlineQPinsBlocked:                "%d session(s) stay pinned here this time; see the warning in the plan.",
	OnlineWarnPinsReason:              "Reason: %s.",
	OnlineWarnPinsFamilyBlocked:       "%d pinned session(s) were not unpinned: unpinning them would also unpin sessions under them that paritydesk cannot pin again (archived or scheduled). Unpinning them yourself in Claude unpins those too.",
	OnlineWarnPinsHidden:              "%d pinned session(s) were not unpinned because their sidebar section is hidden; Claude refuses that change. Show the section or unpin them yourself.",
	OnlineWarnPinsMirror:              "Pins in %s were read from Claude's backup list, because the main list cannot be read.",
	OnlineRetryPinsRemovalLate:        "An unpin was not retried: sessions were already pinned again after it, and retrying could unpin them. It is offered again at the next switch.",
	PlanOnlinePinsPreviewUnavailable:  "  pins          not shown now (Claude is saving them); read again after Claude quits",
	DoctorOnlinePinsIDBCount:          "Pins: read from Claude's IndexedDB (%d pinned session(s))",
	DoctorOnlinePinsMirrorCount:       "Pins: read from Claude's backup list, because the main list cannot be read (%s) (%d pinned session(s))",
	DoctorOnlinePinsUnavailableReason: "Pins: Claude's pin list cannot be read (%s); pins are not carried",
	DoctorOnlinePinsStale:             "Pins: Claude no longer saves pins where paritydesk reads them; pins are not carried. Update paritydesk; `paritydesk rebase` clears this check.",
	DoctorOnlinePinsMirrorDiffers:     "Pins: Claude's backup list differs from its main list; paritydesk uses the main list",
	DoctorOnlinePinsOff:               "Pins: not carried (turned off)",
	PinsReasonDirAbsent:               "the pin list was not found",
	PinsReasonKeyAbsent:               "the pin list was not found",
	PinsReasonAmbiguous:               "more than one pin list was found",
	PinsReasonUnsupported:             "it is stored in a format paritydesk does not know",
	PinsReasonBlobWrapped:             "its value is stored as a file paritydesk does not read",
	PinsReasonVersion:                 "its format version is not known to paritydesk",
	PinsReasonTorn:                    "it changed while it was being read",
	PinsReasonUnclean:                 "Claude did not close it cleanly last time, or the file is damaged",
	PinsReasonPending:                 "Claude had not finished saving it",
	PinsReasonTooLarge:                "it is larger than paritydesk reads",
	PinsReasonBackend:                 "Claude stores it in a kind of database paritydesk does not read",
	PinsReasonFormat:                  "its contents are not in the expected shape",
	PinsReasonStaleStore:              "Claude no longer saves pins where paritydesk reads them",
	PinsReasonOther:                   "it could not be read",

	// v0.2 anchor: artifacts

	// v0.2 anchor: legacy-adopt
	LegacyHeaderDry:                  "Folders next to Claude (dry run; nothing is changed):",
	LegacyHeaderApply:                "Adopting folders next to Claude:",
	LegacyFolderAdopt:                "  %s -> profile %s (account %s, org %s)",
	LegacyFolderActive:               "  %s (in use) -> profile %s (account %s, org %s)",
	LegacyFolderFinish:               "  %s -> profile %s (finishing an earlier adopt)",
	LegacyFolderRegistered:           "  %s is already profile %s",
	LegacyFolderSkip:                 "  %s is left out: %s",
	LegacyOrgUnknown:                 "unknown",
	LegacyReasonExcluded:             "excluded with --legacy-exclude",
	LegacyReasonUnsupportedName:      "paritydesk cannot use this folder name; rename it to Claude-<letters, digits, . _ -> while Claude is not using it",
	LegacyReasonReserved:             "the name is reserved",
	LegacyReasonUnreadable:           "paritydesk could not read it (check its permissions)",
	LegacyReasonForeign:              "it belongs to another account-switching tool",
	LegacyReasonNoIdentity:           "it holds no signed-in account (%s)",
	LegacyReasonDuplicate:            "it holds the same account as %s; pass --legacy-exclude with the folder you no longer use",
	LegacyReasonNoAlias:              "no alias can be made from its name; pass --legacy-alias %s=<alias>",
	LegacyReasonNeedsAlias:           "it needs an alias; pass --legacy-alias Claude=<alias>",
	LegacyReasonAliasTaken:           "alias %s is already used; pass --legacy-alias with another alias",
	LegacyReasonAliasCollision:       "another folder gives the same alias %s; pass --legacy-alias for one of them",
	LegacyReasonParkCollision:        "parking it would need the name %s, which another folder has; pass --legacy-alias with another alias",
	LegacyLeftoverHeader:             "Directories of other accounts inside a folder:",
	LegacyLeftoverItem:               "  %s/%s: %s (%d files, %s) - %s",
	LegacyLeftoverUnique:             "    %d of its entries are not in %s; they may exist nowhere else",
	LegacyKindOtherAccount:           "data of another account (%s)",
	LegacyKindUnrecognized:           "paritydesk cannot tell what this is; it may belong to this folder",
	LegacyKindMixedOrgStub:           "an empty directory of this account under another account's organization",
	LegacyKindMixedOrg:               "this account under another account's organization, with data",
	LegacyActionEligible:             "can be moved aside",
	LegacyActionReportOnly:           "kept: %s",
	LegacyActionDeferred:             "kept for now: %s",
	LegacyActionMoved:                "moved into backup generation %d",
	LegacyActionFailed:               "could not be moved; it is still in place",
	LegacyWhyUnrecognized:            "do not delete it by hand",
	LegacyWhyShortID:                 "it may be a short name the app uses (possibly %s); do not delete it by hand",
	LegacyWhyShortIDUnknown:          "it may be a short name the app uses; do not delete it by hand",
	LegacyWhyHoldsData:               "it holds data",
	LegacyWhyUniqueSessions:          "bring its sessions home first (see docs/migration-from-legacy.md)",
	LegacyWhySymlink:                 "it is a link",
	LegacyWhyTooLarge:                "it is too large to fingerprint",
	LegacyWhyScratch:                 "sessions may still use it",
	LegacyWhyOrgUnknown:              "this folder's organization is unknown",
	LegacyWhyOtherVolume:             "the backup folder is on another volume",
	LegacyWhyAppRunning:              "quit Claude, then run again",
	LegacyWhyInUse:                   "a process is using the folder",
	LegacyWhyFolderChanged:           "the folder changed during the run",
	LegacyWhyDenied:                  "paritydesk never moves this path or something inside it",
	LegacyWhyMoveFailed:              "an earlier move from this folder failed",
	LegacyLeftoverOffer:              "Run again in Terminal with --apply --quarantine to move %d of them into a backup generation. Nothing is deleted; paritydesk rollback <generation> puts them back.",
	LegacySeedHeader:                 "Sync bases from values every folder already agrees on:",
	LegacySeedRow:                    "  %s: %d agree, %d differ",
	LegacySeedRowSkipped:             "  %s: not seeded (%s)",
	LegacySeedNone:                   "No sync bases are seeded (%s). The first switch merges everything and deletes nothing.",
	LegacySeedFirstSwitch:            "The first switch still asks you to confirm. Where the folders differ, nothing is deleted and the copy changed last wins.",
	LegacySeedReasonNoSeedFlag:       "--legacy-no-seed was given",
	LegacySeedReasonNoManifest:       "Claude's version is not covered",
	LegacySeedReasonTooFew:           "fewer than two profiles",
	LegacySeedReasonHasBases:         "a profile already has sync bases",
	LegacySeedReasonHasCanonical:     "paritydesk already holds shared sync state",
	LegacySeedReasonRunOpen:          "a run is open",
	LegacySeedReasonHintMidSwitch:    "the earlier script's switch did not finish",
	LegacySeedReasonDuplicateAccount: "two folders hold the same account",
	LegacySeedReasonProbeFailed:      "a shape probe failed",
	LegacySeedReasonOrgUnknown:       "organization unknown",
	LegacySeedReasonUnreadable:       "the values could not be read",
	LegacySeedReasonNoAdapter:        "no adapter",
	LegacyHintUsed:                   "Hint from the earlier script's state file: the account in Claude becomes profile %s.",
	LegacyHintStale:                  "The earlier script's state file does not match the folders; it was ignored.",
	LegacyHintMidSwitch:              "The earlier script's state file says a switch did not finish. The folders' own contents decide, and no sync bases are seeded; check the folders before --apply.",
	LegacyHintDropped:                "Some values in the earlier script's state file were not usable and were ignored.",
	LegacyWarnRunOpen:                "A paritydesk run is open; run paritydesk recover before --apply.",
	LegacyWarnPendingAdd:             "A profile add is waiting for its sign-in; run paritydesk recover before --apply.",
	LegacyWarnNoActive:               "Claude is missing; finish or undo the earlier script's switch first, so one folder is named Claude.",
	LegacyWarnNoActiveIdentity:       "Claude holds no signed-in account; open Claude, sign in, quit it, then run this again.",
	LegacyWarnMarker:                 "The marker of %s could not be written; its first switch merges without deleting.",
	LegacyWarnForeign:                "Another account-switching tool was detected: %s. --apply stops until it is removed.",
	LegacyWarnFolderChanged:          "%s changed during the run; nothing more was moved from it.",
	LegacyWarnLeftoversUnreadable:    "The directories inside %s could not be listed; none of them is reported or moved.",
	LegacyWarnPrintedDiffers:         "The command printed for use without a terminal cannot read the earlier script's state file, so it does not keep the parked folder name that file gives Claude. To keep it, run --apply in Terminal with the same --legacy-state.",
	LegacyPlanHash:                   "Plan %s. Without a terminal, apply it with: %s",
	LegacyPlanQuarantineTerminal:     "Moving leftovers needs Terminal: run this dry run's command there with --apply --quarantine added (no --legacy-plan); it asks before it changes anything.",
	LegacyDryRun:                     "This was a dry run. Run again with --apply to register these profiles.",
	LegacyNothing:                    "No folder next to Claude needs adopting.",
	LegacyAdopted:                    "Registered %d profile(s): %s.",
	LegacyQuarantined:                "Moved %d of %d directories from %s into backup generation %d. It is kept until you remove it; paritydesk rollback %d puts them back.",
	LegacyNextHeader:                 "Next steps:",
	LegacyNextRetire:                 "  1. Stop using the earlier switch script: remove its alias, launcher, menu item or scheduled job, so only paritydesk moves these folders. Keep its file until your first paritydesk switch works.",
	LegacyNextSkill:                  "  2. Remove any Claude Code skill, command or hook that called the earlier script.",
	LegacyNextBackups:                "  3. If the earlier script kept copies of your folders, they hold your data unencrypted: restrict them with chmod 700, or delete them once paritydesk works for you.",
	LegacyNextPlugin:                 "  4. Install the Claude Code plugin: /plugin marketplace add kazupc/paritydesk, then /plugin install paritydesk@paritydesk.",
	LegacyNextPlan:                   "  5. Preview the first switch with paritydesk plan.",
	LegacyDestructive:                "Data folders: %d directories moved into a backup generation; nothing is deleted.",
	LegacyDestructiveDry:             "Data folders: %d directories would be moved into a backup generation; nothing is deleted.",
	AskLegacyAliasActive:             "Alias for the account in Claude now (a-z, 0-9, -): ",
	AskLegacyAdopt:                   "Register these profiles? [y/N] ",
	AskLegacyAdoptQuarantine:         "Register these profiles and move %d directories into a backup generation? [y/N] ",
	ErrorLegacyPlanRequired:          "--apply without a terminal needs --legacy-plan <hash> from a dry run",
	ErrorLegacyPlanChanged:           "the folders changed since the dry run; run the dry run again",
	ErrorLegacyPlanFlags:             "the flags differ from the dry run's; use the command the dry run printed",
	ErrorLegacyQuarantineTerminal:    "--quarantine moves directories and needs a terminal; run it in Terminal",
	PlanFirstSyncSeeded:              "First switch after adopting folders: values the folders agreed on sync normally; everything else is merged and nothing of it is deleted.",
	AskFirstSyncSeeded:               "This is the first switch after adopting folders from an earlier script. Continue? [y/N] ",
	BackupsPinnedWarn:                "%d generation(s) hold directories moved aside by profile adopt --from-legacy; removing them deletes those directories for good.",
	RollbackQuarantineLine:           "Put back %d directories that profile adopt --from-legacy moved aside from %s; directories recreated since are left as they are.",

	// v0.2 anchor: i18n-ja

}
