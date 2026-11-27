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

	AskBusyStill       Msg = "ask.busy_still"
	PlanBusyHeader     Msg = "plan.busy_header"
	PlanBusyLine       Msg = "plan.busy_line"
	PlanBusyLineNoTime Msg = "plan.busy_line_no_time"
	PlanBusyStale      Msg = "plan.busy_stale"
	PlanBusyTitles     Msg = "plan.busy_titles"
	PlanBusyTitled     Msg = "plan.busy_titled"

	DoctorFDScanNative       Msg = "doctor.fdscan_native"
	DoctorFDScanLsofConfig   Msg = "doctor.fdscan_lsof_config"
	DoctorFDScanFallback     Msg = "doctor.fdscan_fallback"
	DoctorFDScanNativeFailed Msg = "doctor.fdscan_native_failed"
	DoctorFDScanNone         Msg = "doctor.fdscan_none"
	DoctorFDScanLsofTest     Msg = "doctor.fdscan_lsof_test"
	DoctorFDScanBlind        Msg = "doctor.fdscan_blind"
	DoctorFDScanPartial      Msg = "doctor.fdscan_partial"

	VerifyUnverified Msg = "verify.unverified"

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

	HookOnlinePrompt             Msg = "hook.online_prompt"
	HookOnlineAuto               Msg = "hook.online_auto"
	GuardDenyFloor               Msg = "guard.deny_floor"
	GuardDenyUnplanned           Msg = "guard.deny_unplanned"
	GuardDenyNotYet              Msg = "guard.deny_not_yet"
	GuardDenyUnreadable          Msg = "guard.deny_unreadable"
	GuardDenyDuplicate           Msg = "guard.deny_duplicate"
	GuardDenyArtifactTool        Msg = "guard.deny_artifact_tool"
	GuardDenyUnplannedArtifacts  Msg = "guard.deny_unplanned_artifacts"
	GuardDenyNotYetArtifacts     Msg = "guard.deny_not_yet_artifacts"
	GuardDenyDuplicateArtifacts  Msg = "guard.deny_duplicate_artifacts"
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

	ArtifactsOff                  Msg = "artifacts.off"
	ArtifactsErrOutsideApp        Msg = "artifacts.err_outside_app"
	ArtifactsErrTerminalOnly      Msg = "artifacts.err_terminal_only"
	ArtifactsErrToolsMissing      Msg = "artifacts.err_tools_missing"
	ArtifactsErrReadOnly          Msg = "artifacts.err_read_only"
	ArtifactsErrBusy              Msg = "artifacts.err_busy"
	ArtifactsErrManifestInvalid   Msg = "artifacts.err_manifest_invalid"
	ArtifactsErrBlobCorrupt       Msg = "artifacts.err_blob_corrupt"
	ArtifactsErrRoundInvalid      Msg = "artifacts.err_round_invalid"
	ArtifactsErrNeedsYes          Msg = "artifacts.err_needs_yes"
	ArtifactsErrUnknownPair       Msg = "artifacts.err_unknown_pair"
	ArtifactsErrLegacyPath        Msg = "artifacts.err_legacy_path"
	ArtifactsErrLegacyFormat      Msg = "artifacts.err_legacy_format"
	ArtifactsReconcileFirst       Msg = "artifacts.reconcile_first"
	ArtifactsReplicaUnknown       Msg = "artifacts.replica_unknown"
	ArtifactsReplicaChanged       Msg = "artifacts.replica_changed"
	ArtifactsInventoryForeign     Msg = "artifacts.inventory_foreign"
	ArtifactsRoundsCapped         Msg = "artifacts.rounds_capped"
	ArtifactsPaused               Msg = "artifacts.paused"
	ArtifactsLoopCapped           Msg = "artifacts.loop_capped"
	ArtifactsPublishUnavailable   Msg = "artifacts.publish_unavailable"
	ArtifactsGrantDeclined        Msg = "artifacts.grant_declined"
	ArtifactsGateClosed           Msg = "artifacts.gate_closed"
	ArtifactsOtherOrgHint         Msg = "artifacts.other_org_hint"
	ArtifactsForgetChainActive    Msg = "artifacts.forget_chain_active"
	ArtifactsQHeader              Msg = "artifacts.q_header"
	ArtifactsQText                Msg = "artifacts.q_text"
	ArtifactsQApply               Msg = "artifacts.q_apply"
	ArtifactsQApplyDesc           Msg = "artifacts.q_apply_desc"
	ArtifactsQSkip                Msg = "artifacts.q_skip"
	ArtifactsQSkipDesc            Msg = "artifacts.q_skip_desc"
	ArtifactsDoneSummary          Msg = "artifacts.done_summary"
	ArtifactsDoneDetails          Msg = "artifacts.done_details"
	ArtifactsDoneDeleted          Msg = "artifacts.done_deleted"
	ArtifactsDoneIgnored          Msg = "artifacts.done_ignored"
	ArtifactsDoneRootBusy         Msg = "artifacts.done_root_busy"
	ArtifactsWarnInventorySuspect Msg = "artifacts.warn_inventory_suspect"
	ArtifactsWarnPairCollision    Msg = "artifacts.warn_pair_collision"
	ArtifactsStatusSummary        Msg = "artifacts.status_summary"
	ArtifactsStatusNone           Msg = "artifacts.status_none"
	ArtifactsAskCapabilities      Msg = "artifacts.ask_capabilities"
	ArtifactsAskShared            Msg = "artifacts.ask_shared"
	ArtifactsAskRoundtrip         Msg = "artifacts.ask_roundtrip"
	ArtifactsAskForget            Msg = "artifacts.ask_forget"
	ArtifactsAskRehome            Msg = "artifacts.ask_rehome"
	ArtifactsAskRestore           Msg = "artifacts.ask_restore"
	ArtifactsAskCreate            Msg = "artifacts.ask_create"
	ArtifactsAskResume            Msg = "artifacts.ask_resume"
	ArtifactsAskImport            Msg = "artifacts.ask_import"
	ArtifactsAllowed              Msg = "artifacts.allowed"
	ArtifactsAllowedPublishOff    Msg = "artifacts.allowed_publish_off"
	ArtifactsSkippedPair          Msg = "artifacts.skipped_pair"
	ArtifactsForgotten            Msg = "artifacts.forgotten"
	ArtifactsIngested             Msg = "artifacts.ingested"
	ArtifactsNothingToIngest      Msg = "artifacts.nothing_to_ingest"
	ArtifactsImportSummary        Msg = "artifacts.import_summary"
	ArtifactsImportMapHint        Msg = "artifacts.import_map_hint"
	ArtifactsImportChainActive    Msg = "artifacts.import_chain_active"
	ArtifactsImportApplied        Msg = "artifacts.import_applied"
	OnlineArtifactsNext           Msg = "online.artifacts_next"
	PlanArtifactsDue              Msg = "plan.artifacts_due"
	PlanArtifactsUnsynced         Msg = "plan.artifacts_unsynced"
	HookArtifactsDue              Msg = "hook.artifacts_due"
	DoctorArtifactsOK             Msg = "doctor.artifacts_ok"
	DoctorArtifactsBad            Msg = "doctor.artifacts_bad"
	DoctorArtifactsOff            Msg = "doctor.artifacts_off"
	ArtifactsOffReconcile         Msg = "artifacts.off_reconcile"
	ArtifactsPlanSummary          Msg = "artifacts.plan_summary"
	ArtifactsNoTitle              Msg = "artifacts.no_title"
	ArtifactsRenameFailed         Msg = "artifacts.rename_failed"
	DoctorArtifactsUnreadable     Msg = "doctor.artifacts_manifest_unreadable"
	DoctorArtifactsInvalid        Msg = "doctor.artifacts_manifest_invalid"
	DoctorArtifactsBlobsMissing   Msg = "doctor.artifacts_blobs_missing"
	DoctorArtifactsGrantEachSync  Msg = "doctor.artifacts_publishing_grant"
	DoctorArtifactsCaptureOnly    Msg = "doctor.artifacts_publishing_off"

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

	// Separators.
	WordListSep Msg = "word.list_sep"

	// Notifications of the detached job.
	NotifySwitched Msg = "notify.switched"
	NotifyFinished Msg = "notify.finished"
	NotifyCheck    Msg = "notify.check"
	NotifyStopped  Msg = "notify.stopped"
	// Log signals (busy detection, account changes, Chrome pairing org); moved from orchestrator/busytext.go.
	ConfirmBusyLine                   Msg = "confirm.busy_line"
	ConfirmBusyLineNoTime             Msg = "confirm.busy_line_no_time"
	ConfirmBusyUntitled               Msg = "confirm.busy_untitled"
	ConfirmBusyWaited                 Msg = "confirm.busy_waited"
	ProgressP3BusyWait                Msg = "progress.p3.busy_wait"
	WarnBusyStale                     Msg = "warn.busy_stale"
	WarnBusyPreview                   Msg = "warn.busy_preview"
	WarnBusyTruncated                 Msg = "warn.busy_truncated"
	WarnBusyLogUnreadable             Msg = "warn.busy_log_unreadable"
	WarnAccountWindow                 Msg = "warn.account_window"
	WarnAccountChange                 Msg = "warn.account_change"
	WarnInFolderSignIn                Msg = "warn.in_folder_sign_in"
	WarnNativeMulti                   Msg = "warn.native_multi"
	WarnSwapOnlyNativeLog             Msg = "warn.swap_only_native_log"
	WarnSwapOnlyNativeHistory         Msg = "warn.swap_only_native_history"
	ErrSyncSwapOnlyNativeLog          Msg = "err.sync.swap_only_native_log"
	ErrSyncSwapOnlyNativeHistory      Msg = "err.sync.swap_only_native_history"
	ErrResolveInFolderSignIn          Msg = "err.resolve.in_folder_sign_in"
	DoctorBusyDetectionOK             Msg = "doctor.busy_detection.ok"
	DoctorBusyDetectionRecord         Msg = "doctor.busy_detection.record"
	DoctorBusyDetectionPartial        Msg = "doctor.busy_detection.partial"
	DoctorBusyDetectionUnavailable    Msg = "doctor.busy_detection.unavailable"
	DoctorBusyNowAppStopped           Msg = "doctor.busy_now.app_stopped"
	DoctorBusyNowNone                 Msg = "doctor.busy_now.none"
	DoctorBusyNowSome                 Msg = "doctor.busy_now.some"
	DoctorBusyNowNoSignals            Msg = "doctor.busy_now.no_signals"
	DoctorBusyNowUnreadable           Msg = "doctor.busy_now.unreadable"
	DoctorBusyNowRecords              Msg = "doctor.busy_now.records"
	DoctorBusyNowStale                Msg = "doctor.busy_now.stale"
	DoctorBusyNowTruncated            Msg = "doctor.busy_now.truncated"
	DoctorAccountChangeUnavailable    Msg = "doctor.account_change.unavailable"
	DoctorAccountChangeNone           Msg = "doctor.account_change.none"
	DoctorAccountChangeOne            Msg = "doctor.account_change.one"
	DoctorAccountChangeUnknown        Msg = "doctor.account_change.unknown"
	DoctorAccountChangeInFolder       Msg = "doctor.account_change.in_folder"
	DoctorAccountChangeNativeLog      Msg = "doctor.account_change.native_log"
	DoctorAccountChangeNativeHistory  Msg = "doctor.account_change.native_history"
	DoctorAccountChangeUnknownTime    Msg = "doctor.account_change.unknown_time"
	DoctorChromePairingOrgKnown       Msg = "doctor.chrome_pairing_org.known"
	DoctorChromePairingOrgUnknown     Msg = "doctor.chrome_pairing_org.unknown"
	DoctorChromePairingOrgState       Msg = "doctor.chrome_pairing_org.state"
	DoctorChromeBridgePairingDisabled Msg = "doctor.chrome_bridge.pairing_disabled"
	DoctorVersionDeprecatedKey        Msg = "doctor.version.deprecated_key"
	// Orchestrator progress, warnings, doctor details and recover actions (migrated literals).
	ProgressP5FinalPlan                Msg = "progress.p5.final_plan"
	ProgressP6BackingUp                Msg = "progress.p6.backing_up"
	ProgressP7Applying                 Msg = "progress.p7.applying"
	WarnEnteringInUseNotRestored       Msg = "warn.entering_in_use_not_restored"
	WarnEnteringNotFullyRestored       Msg = "warn.entering_not_fully_restored"
	ProgressP8SwappingFolders          Msg = "progress.p8.swapping_folders"
	WarnPostSwapCheckNotRun            Msg = "warn.post_swap_check_not_run"
	ProgressP9Parking                  Msg = "progress.p9.parking"
	WarnLeavingMarkerNotWritten        Msg = "warn.leaving_marker_not_written"
	WarnRunInterrupted                 Msg = "warn.run_interrupted"
	ProfileProblemFolderMissing        Msg = "profile.problem.folder_missing"
	WarnFoldersUnreadable              Msg = "warn.folders_unreadable"
	WarnProfilesMismatch               Msg = "warn.profiles_mismatch"
	DoctorStateUnreadable              Msg = "doctor.state.unreadable"
	DoctorFoldersUnreadable            Msg = "doctor.folders.unreadable"
	DoctorVersionNotFound              Msg = "doctor.version.not_found"
	DoctorForeignToolsNone             Msg = "doctor.foreign_tools.none"
	DoctorStateInterrupted             Msg = "doctor.state.interrupted"
	DoctorStateOK                      Msg = "doctor.state.ok"
	DoctorOrgUnknown                   Msg = "doctor.org.unknown"
	DoctorUnclassifiedNone             Msg = "doctor.unclassified.none"
	DoctorAutomationNotChecked         Msg = "doctor.automation.not_checked"
	DoctorAutomationMissing            Msg = "doctor.automation.missing"
	DoctorAutomationGranted            Msg = "doctor.automation.granted"
	DoctorUpdateUnreadable             Msg = "doctor.update.unreadable"
	DoctorUpdateStaged                 Msg = "doctor.update.staged"
	DoctorUpdateNone                   Msg = "doctor.update.none"
	DoctorChromeBridgeUnreadable       Msg = "doctor.chrome_bridge.unreadable"
	DoctorChromeBridgeConnected        Msg = "doctor.chrome_bridge.connected"
	DoctorManagedAll                   Msg = "doctor.managed.all"
	DoctorRunnerNone                   Msg = "doctor.runner.none"
	DoctorRunnerNoStale                Msg = "doctor.runner.no_stale"
	ProgressP0AutomationMissing        Msg = "progress.p0.automation_missing"
	ProgressP0AutomationGranted        Msg = "progress.p0.automation_granted"
	ProgressP11LaunchFailedSignIn      Msg = "progress.p11.launch_failed_sign_in"
	ProgressP10ObjectsNotRemoved       Msg = "progress.p10.objects_not_removed"
	ProgressP11RelaunchedClaude        Msg = "progress.p11.relaunched_claude"
	ProgressP12NotSettled              Msg = "progress.p12.not_settled"
	WarnAccountChangedOutside          Msg = "warn.account_changed_outside"
	ProgressP10Recording               Msg = "progress.p10.recording"
	WarnEnteringNotReread              Msg = "warn.entering_not_reread"
	WarnObjectsNotRemoved              Msg = "warn.objects_not_removed"
	WarnGenerationsNotPruned           Msg = "warn.generations_not_pruned"
	ProgressP11Launching               Msg = "progress.p11.launching"
	ProgressP11NotLaunched             Msg = "progress.p11.not_launched"
	ProgressP11NotRunningBefore        Msg = "progress.p11.not_running_before"
	WarnLaunchNoProfile                Msg = "warn.launch_no_profile"
	WarnLaunchFailed                   Msg = "warn.launch_failed"
	WarnInvokingNotInProfile           Msg = "warn.invoking_not_in_profile"
	WarnInvokingNotReopened            Msg = "warn.invoking_not_reopened"
	ProgressP12BackgroundCheck         Msg = "progress.p12.background_check"
	ProgressP12Verifying               Msg = "progress.p12.verifying"
	ProgressP10EmptyGenerationKept     Msg = "progress.p10.empty_generation_kept"
	ProgressP10OnlineNotQueued         Msg = "progress.p10.online_not_queued"
	WarnOnlineLeaseNotRemoved          Msg = "warn.online_lease_not_removed"
	ProgressP2AcksUnreadable           Msg = "progress.p2.acks_unreadable"
	RecoverNothing                     Msg = "recover.nothing"
	RecoverRelaunched                  Msg = "recover.relaunched"
	RecoverPutBack                     Msg = "recover.put_back"
	RecoverRemovedGeneration           Msg = "recover.removed_generation"
	RecoverMovedEntering               Msg = "recover.moved_entering"
	RecoverEnteringBasesInvalid        Msg = "recover.entering_bases_invalid"
	RecoverOnlineRequeued              Msg = "recover.online_requeued"
	RecoverLaunchedForSignIn           Msg = "recover.launched_for_sign_in"
	RecoverLeftPendingAdd              Msg = "recover.left_pending_add"
	RecoverNoJournal                   Msg = "recover.no_journal"
	ProgressP11RelaunchNoProfile       Msg = "progress.p11.relaunch_no_profile"
	ProgressP11LaunchFailed            Msg = "progress.p11.launch_failed"
	ProgressP0Checking                 Msg = "progress.p0.checking"
	WarnUpdateUnreadable               Msg = "warn.update_unreadable"
	WarnUpdateStaged                   Msg = "warn.update_staged"
	ProgressP0UpdateWait               Msg = "progress.p0.update_wait"
	WarnDegraded                       Msg = "warn.degraded"
	WarnAppleEventTimeout              Msg = "warn.apple_event_timeout"
	WarnInvokingNotFound               Msg = "warn.invoking_not_found"
	ProgressP2Planning                 Msg = "progress.p2.planning"
	ProgressP3Quitting                 Msg = "progress.p3.quitting"
	WarnInvokingTurnNotEnded           Msg = "warn.invoking_turn_not_ended"
	ProgressP3QuitDialog               Msg = "progress.p3.quit_dialog"
	ProgressP3LateQuitWatch            Msg = "progress.p3.late_quit_watch"
	WarnReadOnlyHolder                 Msg = "warn.read_only_holder"
	ProgressP4Waiting                  Msg = "progress.p4.waiting"
	ProgressP2ReadOnly                 Msg = "progress.p2.read_only"
	ProgressP0Detached                 Msg = "progress.p0.detached"
	WordCommaSep                       Msg = "word.comma_sep"
	WordList                           Msg = "word.list"
	WarnSwapOnly                       Msg = "warn.swap_only"
	ConfirmOutsideSwitchLine           Msg = "confirm.outside_switch_line"
	ProgressP11Relaunched              Msg = "progress.p11.relaunched"
	ProgressP0Updated                  Msg = "progress.p0.updated"
	ConfirmDeletionLine                Msg = "confirm.deletion_line"
	ErrPreflightSessionsBusy           Msg = "err.preflight.sessions_busy"
	ErrQuitSessionsBusy                Msg = "err.quit.sessions_busy"
	WarnPendingAdd                     Msg = "warn.pending_add"
	DoctorForeignToolsFound            Msg = "doctor.foreign_tools.found"
	DoctorStatePendingAdd              Msg = "doctor.state.pending_add"
	DoctorVersionTested                Msg = "doctor.version.tested"
	DoctorVersionUntested              Msg = "doctor.version.untested"
	DoctorSkippedRows                  Msg = "doctor.skipped_rows"
	DoctorOrgActive                    Msg = "doctor.org.active"
	DoctorProbesLSUnreadable           Msg = "doctor.probes.ls_unreadable"
	DoctorProbesNotRun                 Msg = "doctor.probes.not_run"
	DoctorProbesFailed                 Msg = "doctor.probes.failed"
	DoctorProbesPassed                 Msg = "doctor.probes.passed"
	DoctorIdentityNone                 Msg = "doctor.identity.none"
	DoctorIdentityOK                   Msg = "doctor.identity.ok"
	DoctorIdentityUnregistered         Msg = "doctor.identity.unregistered"
	DoctorMarkerNone                   Msg = "doctor.marker.none"
	DoctorMarkerOther                  Msg = "doctor.marker.other"
	DoctorMarkerOK                     Msg = "doctor.marker.ok"
	DoctorUnclassifiedSome             Msg = "doctor.unclassified.some"
	DoctorChromeBridgeNotUsed          Msg = "doctor.chrome_bridge.not_used"
	DoctorManagedHint                  Msg = "doctor.managed.hint"
	DoctorRunnerStale                  Msg = "doctor.runner.stale"
	ProgressP0ChromeNote               Msg = "progress.p0.chrome_note"
	ProgressP0InitAdd                  Msg = "progress.p0.init_add"
	ProgressP0UnregisteredFolder       Msg = "progress.p0.unregistered_folder"
	ProgressP0ForeignTool              Msg = "progress.p0.foreign_tool"
	ProgressP11AddSignIn               Msg = "progress.p11.add_sign_in"
	ProgressP10Removed                 Msg = "progress.p10.removed"
	ConfirmRollbackLine                Msg = "confirm.rollback_line"
	ConfirmRollbackPropagate           Msg = "confirm.rollback_propagate"
	DoctorAccountChangeToProfile       Msg = "doctor.account_change.to_profile"
	WordSentences                      Msg = "word.sentences"
	ProfileWhyNoState                  Msg = "profile.why.no_state"
	ProfileWhyFolderMissing            Msg = "profile.why.folder_missing"
	ProfileWhyIdentityUnknown          Msg = "profile.why.identity_unknown"
	ProfileWhyNoMarker                 Msg = "profile.why.no_marker"
	ProfileWhyMarkerAlias              Msg = "profile.why.marker_alias"
	ProfileWhyMarkerReplica            Msg = "profile.why.marker_replica"
	ProfileWhyMarkerBase               Msg = "profile.why.marker_base"
	ProfileWhyFolderID                 Msg = "profile.why.folder_id"
	ProfileWhyReplicaChanged           Msg = "profile.why.replica_changed"
	ErrResolveMismatch                 Msg = "err.resolve.mismatch"
	WarnRowNotReadBack                 Msg = "warn.row_not_read_back"
	ProgressP10Registered              Msg = "progress.p10.registered"
	RecoverLeftoversMoved              Msg = "recover.leftovers_moved"
	ProgressP2BaseUnreadable           Msg = "progress.p2.base_unreadable"
	ProgressP2PresetsDiffer            Msg = "progress.p2.presets_differ"
	ProgressP2StaleValues              Msg = "progress.p2.stale_values"
	ProgressP2EnteringUnreadable       Msg = "progress.p2.entering_unreadable"
	ProgressP12LSUnreadable            Msg = "progress.p12.ls_unreadable"
	ProgressP12LSRowUnreadable         Msg = "progress.p12.ls_row_unreadable"
	RecoverMovedLeaving                Msg = "recover.moved_leaving"
	RecoverConflictsLeft               Msg = "recover.conflicts_left"
	RecoverRollbackFinished            Msg = "recover.rollback_finished"
	RecoverPutBackAdd                  Msg = "recover.put_back_add"
	RecoverAddWaits                    Msg = "recover.add_waits"
	ConfirmAddBindLine                 Msg = "confirm.add_bind_line"
	RecoverAddDropped                  Msg = "recover.add_dropped"
	RecoverRegistered                  Msg = "recover.registered"
	ConfirmFoldersMismatch             Msg = "confirm.folders_mismatch"
	ErrRecoverMismatchClaude           Msg = "err.recover.mismatch_claude"
	ConfirmMoveFolder                  Msg = "confirm.move_folder"
	RecoverMoved                       Msg = "recover.moved"
	ErrRecoverMismatchNoMove           Msg = "err.recover.mismatch_no_move"
	ConfirmNoJournal                   Msg = "confirm.no_journal"
	ConfirmMoveRecent                  Msg = "confirm.move_recent"
	RecoverFolderUnidentified          Msg = "recover.folder_unidentified"
	RecoverFolderAccount               Msg = "recover.folder_account"
	RecoverFolderUnregistered          Msg = "recover.folder_unregistered"
	RecoverNoFolders                   Msg = "recover.no_folders"
	RecoverRolledBack                  Msg = "recover.rolled_back"
	RecoverCompleted                   Msg = "recover.completed"
	RecoverClosedLeftoversUnmoved      Msg = "recover.closed_leftovers_unmoved"
	RecoverClosedLeftoversGenRemoved   Msg = "recover.closed_leftovers_gen_removed"
	RecoverClosedLeftovers             Msg = "recover.closed_leftovers"
	RecoverClosedSwitch                Msg = "recover.closed_switch"
	RecoverClosedRunUnchanged          Msg = "recover.closed_run_unchanged"
	RecoverClosedSync                  Msg = "recover.closed_sync"
	RecoverRestoredActive              Msg = "recover.restored_active"
	RecoverClosedRollbackUnrestored    Msg = "recover.closed_rollback_unrestored"
	RecoverClosedRollback              Msg = "recover.closed_rollback"
	RecoverClosedAddUnmoved            Msg = "recover.closed_add_unmoved"
	RecoverClosedAdd                   Msg = "recover.closed_add"
	RecoverClosedRun                   Msg = "recover.closed_run"
	ErrInterrupted                     Msg = "err.interrupted"
	ErrArtifactsUnknownQuestion        Msg = "err.artifacts.unknown_question"
	ErrArtifactsUnknownDecision        Msg = "err.artifacts.unknown_decision"
	ErrArtifactsProfileNamesProfile    Msg = "err.artifacts.profile_names_profile"
	ErrArtifactsCopyNotOtherOrg        Msg = "err.artifacts.copy_not_other_org"
	ErrAppUpdateNotInstalled           Msg = "err.app.update_not_installed"
	ErrAppTurnNotEnded                 Msg = "err.app.turn_not_ended"
	ErrPlansHashInvalid                Msg = "err.plans.hash_invalid"
	ErrPlansNotFound                   Msg = "err.plans.not_found"
	ErrPlansUnreadable                 Msg = "err.plans.unreadable"
	ErrPlansHashMismatch               Msg = "err.plans.hash_mismatch"
	ErrInterruptedFirst                Msg = "err.interrupted_first"
	ErrPendingAddFirst                 Msg = "err.pending_add_first"
	ErrLogNoLog                        Msg = "err.log.no_log"
	ErrStateLockBusy                   Msg = "err.state.lock_busy"
	ErrOnlineUnknownAnswer             Msg = "err.online.unknown_answer"
	ErrOnlineNoQuestion                Msg = "err.online.no_question"
	ErrPreflightInterrupted            Msg = "err.preflight.interrupted"
	ErrPendingAdd                      Msg = "err.pending_add"
	ErrResolveNoActiveProfile          Msg = "err.resolve.no_active_profile"
	ErrSwitchNoOtherProfile            Msg = "err.switch.no_other_profile"
	ErrCompatRefused                   Msg = "err.compat.refused"
	ErrCompatUntested                  Msg = "err.compat.untested"
	ErrCompatSyncSwapOnly              Msg = "err.compat.sync_swap_only"
	ErrPreflightDiskSpace              Msg = "err.preflight.disk_space"
	ErrPreflightAutomationMissing      Msg = "err.preflight.automation_missing"
	ErrConfirmedPlanFirstSync          Msg = "err.confirmed_plan.first_sync"
	ErrPlanFirstSync                   Msg = "err.plan.first_sync"
	ErrQuiescenceInUse                 Msg = "err.quiescence.in_use"
	ErrLegacyDegraded                  Msg = "err.legacy.degraded"
	ErrSwitchDetachedNeedsConfirmation Msg = "err.switch.detached_needs_confirmation"
	ErrSyncInsideClaude                Msg = "err.sync.inside_claude"
	ErrRollbackReadOnly                Msg = "err.rollback.read_only"
	ErrRunOpen                         Msg = "err.run_open"
	ErrRollbackNoGeneration            Msg = "err.rollback.no_generation"
	ErrRollbackNotQuit                 Msg = "err.rollback.not_quit"
	ErrRebaseReadOnly                  Msg = "err.rebase.read_only"
	ErrOnlineDoneReadOnly              Msg = "err.online.done_read_only"
	ErrConfirmNoTTY                    Msg = "err.confirm.no_tty"
	ErrConfirmDeclined                 Msg = "err.confirm.declined"
	ErrStateOtherDataRoot              Msg = "err.state.other_data_root"
	ErrSwitchSwapOnly                  Msg = "err.switch.swap_only"
	ErrSwitchDegraded                  Msg = "err.switch.degraded"
	ErrHarvestAppChanged               Msg = "err.harvest.app_changed"
	ErrQuiescenceOpened                Msg = "err.quiescence.opened"
	ErrSwapInterrupted                 Msg = "err.swap.interrupted"
	ErrSwapProcessStarted              Msg = "err.swap.process_started"
	ErrSwapFoldersOpened               Msg = "err.swap.folders_opened"
	ErrSwapBackFailed                  Msg = "err.swap.back_failed"
	ErrSwapUndone                      Msg = "err.swap.undone"
	ErrVerifyNoActive                  Msg = "err.verify.no_active"
	ErrVerifyNoManifest                Msg = "err.verify.no_manifest"
	ErrVerifyDrift                     Msg = "err.verify.drift"
	ErrRecoverReadOnly                 Msg = "err.recover.read_only"
	ErrRecoverAppNotFound              Msg = "err.recover.app_not_found"
	ErrRecoverQuitFirst                Msg = "err.recover.quit_first"
	ErrRecoverInUse                    Msg = "err.recover.in_use"
	ErrRecoverNoJournal                Msg = "err.recover.no_journal"
	ErrInitNoAccount                   Msg = "err.init.no_account"
	ErrProfileRemoveActive             Msg = "err.profile_remove.active"
	ErrApplyInUse                      Msg = "err.apply.in_use"
	ErrApplyChanged                    Msg = "err.apply.changed"
	ErrApplyCopySourceChanged          Msg = "err.apply.copy_source_changed"
	ErrOpenFilesNativeUnavailable      Msg = "err.open_files.native_unavailable"
	ErrValidateBreaker                 Msg = "err.validate.breaker"
	ErrValidateBreakerEmpty            Msg = "err.validate.breaker_empty"
	ErrValidateWorktreeRisk            Msg = "err.validate.worktree_risk"
	ErrForeignTool                     Msg = "err.foreign_tool"
	ErrLegacyAliasNotFolder            Msg = "err.legacy.alias_not_folder"
	ErrLegacyExcludeNotFolder          Msg = "err.legacy.exclude_not_folder"
	ErrLegacyState                     Msg = "err.legacy.state"
	ErrSwitchNoPrevious                Msg = "err.switch.no_previous"
	ErrSwitchUnknownProfile            Msg = "err.switch.unknown_profile"
	ErrSwitchAlreadyActive             Msg = "err.switch.already_active"
	ErrSwitchFolderMissing             Msg = "err.switch.folder_missing"
	ErrPreflightParkTaken              Msg = "err.preflight.park_taken"
	ErrConfirmedPlanNewDestructive     Msg = "err.confirmed_plan.new_destructive"
	ErrRollbackNoProfile               Msg = "err.rollback.no_profile"
	ErrRollbackOtherAccount            Msg = "err.rollback.other_account"
	ErrRollbackFolderAccount           Msg = "err.rollback.folder_account"
	ErrRollbackInUse                   Msg = "err.rollback.in_use"
	ErrHarvestNewDestructive           Msg = "err.harvest.new_destructive"
	ErrParkTaken                       Msg = "err.park.taken"
	ErrVerifyIdentity                  Msg = "err.verify.identity"
	ErrVerifySignIn                    Msg = "err.verify.sign_in"
	ErrVerifyDriftNotRecorded          Msg = "err.verify.drift_not_recorded"
	ErrRecoverParkTaken                Msg = "err.recover.park_taken"
	ErrRecoverNameTaken                Msg = "err.recover.name_taken"
	ErrReadOnlyWrites                  Msg = "err.read_only_writes"
	ErrProfileExists                   Msg = "err.profile.exists"
	ErrInitRegistered                  Msg = "err.init.registered"
	ErrProfileAdoptNotFound            Msg = "err.profile_adopt.not_found"
	ErrProfileAdoptNoAccount           Msg = "err.profile_adopt.no_account"
	ErrProfileAdoptRegistered          Msg = "err.profile_adopt.registered"
	ErrProfileAddTaken                 Msg = "err.profile_add.taken"
	// Command-line errors (usageError).
	UsageProfileFlag           Msg = "usage.profile_flag"
	UsageProfileNoneActive     Msg = "usage.profile_none_active"
	UsageAllowOneOf            Msg = "usage.allow_one_of"
	UsageAllowRoundtripNoID    Msg = "usage.allow_roundtrip_no_id"
	UsageAllowNeedsID          Msg = "usage.allow_needs_id"
	UsageImportLegacyFrom      Msg = "usage.import_legacy_from"
	UsageMapSyntax             Msg = "usage.map_syntax"
	UsageUnavailableSyntax     Msg = "usage.unavailable_syntax"
	UsageRefusedSyntax         Msg = "usage.refused_syntax"
	UsageQuestionAnswer        Msg = "usage.question_answer"
	UsageMissingCommand        Msg = "usage.missing_command"
	UsageUnknownCommand        Msg = "usage.unknown_command"
	UsageSyntax                Msg = "usage.syntax"
	UsageUnknownShell          Msg = "usage.unknown_shell"
	UsageOutOfRange            Msg = "usage.out_of_range"
	UsageFlagValue             Msg = "usage.flag_value"
	UsageFaultAfter            Msg = "usage.fault_after"
	UsageFaultPausePhase       Msg = "usage.fault_pause_phase"
	UsageFaultPauseRefused     Msg = "usage.fault_pause_refused"
	UsageFaultUnknown          Msg = "usage.fault_unknown"
	UsageCategoryArtifacts     Msg = "usage.category_artifacts"
	UsageUnknownCategory       Msg = "usage.unknown_category"
	UsageCategoryTwice         Msg = "usage.category_twice"
	UsageCategoryOnlyAndSkip   Msg = "usage.category_only_and_skip"
	UsageBadAlias              Msg = "usage.bad_alias"
	UsageBadGeneration         Msg = "usage.bad_generation"
	UsageSessionUuid           Msg = "usage.session_uuid"
	UsageNegative              Msg = "usage.negative"
	UsagePlanHash              Msg = "usage.plan_hash"
	UsageAbsolutePath          Msg = "usage.absolute_path"
	UsageLegacyAliasSyntax     Msg = "usage.legacy_alias_syntax"
	UsageLegacyAliasTwice      Msg = "usage.legacy_alias_twice"
	UsageLegacyExcludeSyntax   Msg = "usage.legacy_exclude_syntax"
	UsageLegacyPlanHash        Msg = "usage.legacy_plan_hash"
	UsageManagedBoth           Msg = "usage.managed_both"
	UsageLegacyManagedBoth     Msg = "usage.legacy_managed_both"
	UsageLegacyManagedRequired Msg = "usage.legacy_managed_required"
	UsageLegacyManagedUnknown  Msg = "usage.legacy_managed_unknown"
	UsageLegacyAliasRequired   Msg = "usage.legacy_alias_required"
	UsageOlderThan             Msg = "usage.older_than"
	UsageRunFlag               Msg = "usage.run_flag"
	UsageSessionFlag           Msg = "usage.session_flag"
	UsageTokenFlag             Msg = "usage.token_flag"
	UsageBadRunID              Msg = "usage.bad_run_id"
	UsageLegacyNoArgs          Msg = "usage.legacy_no_args"
	UsageNeedsFlag             Msg = "usage.needs_flag"
	UsageBadFolder             Msg = "usage.bad_folder"
	UsageManagedRequired       Msg = "usage.managed_required"
	UsageAliasRequired         Msg = "usage.alias_required"
	UsageLabelTooLong          Msg = "usage.label_too_long"
	UsageFlag                  Msg = "usage.flag"
	// Help: command short texts (help.<path>) and the help frame.
	HelpUsage                 Msg = "help.usage"
	HelpCommands              Msg = "help.commands"
	HelpFlags                 Msg = "help.flags"
	HelpGlobalFlags           Msg = "help.global_flags"
	HelpDefault               Msg = "help.default"
	HelpRoot                  Msg = "help.root"
	HelpInit                  Msg = "help.init"
	HelpProfile               Msg = "help.profile"
	HelpProfileList           Msg = "help.profile.list"
	HelpProfileAdopt          Msg = "help.profile.adopt"
	HelpProfileAdd            Msg = "help.profile.add"
	HelpProfileRename         Msg = "help.profile.rename"
	HelpProfileRemove         Msg = "help.profile.remove"
	HelpStatus                Msg = "help.status"
	HelpPlan                  Msg = "help.plan"
	HelpSwitch                Msg = "help.switch"
	HelpSync                  Msg = "help.sync"
	HelpVerify                Msg = "help.verify"
	HelpRecover               Msg = "help.recover"
	HelpRollback              Msg = "help.rollback"
	HelpRebase                Msg = "help.rebase"
	HelpGenerations           Msg = "help.generations"
	HelpGenerationsList       Msg = "help.generations.list"
	HelpGenerationsShow       Msg = "help.generations.show"
	HelpBackups               Msg = "help.backups"
	HelpBackupsPrune          Msg = "help.backups.prune"
	HelpBackupsPurge          Msg = "help.backups.purge"
	HelpDoctor                Msg = "help.doctor"
	HelpLog                   Msg = "help.log"
	HelpHistory               Msg = "help.history"
	HelpConfig                Msg = "help.config"
	HelpConfigPath            Msg = "help.config.path"
	HelpConfigShow            Msg = "help.config.show"
	HelpConfigValidate        Msg = "help.config.validate"
	HelpOnline                Msg = "help.online"
	HelpOnlineStatus          Msg = "help.online.status"
	HelpOnlineCancel          Msg = "help.online.cancel"
	HelpOnlineCapture         Msg = "help.online.capture"
	HelpOnlineNext            Msg = "help.online.next"
	HelpOnlineDecide          Msg = "help.online.decide"
	HelpOnlineRetry           Msg = "help.online.retry"
	HelpOnlineDone            Msg = "help.online.done"
	HelpArtifacts             Msg = "help.artifacts"
	HelpArtifactsStatus       Msg = "help.artifacts.status"
	HelpArtifactsPlan         Msg = "help.artifacts.plan"
	HelpArtifactsIngest       Msg = "help.artifacts.ingest"
	HelpArtifactsAllow        Msg = "help.artifacts.allow"
	HelpArtifactsSkip         Msg = "help.artifacts.skip"
	HelpArtifactsForget       Msg = "help.artifacts.forget"
	HelpArtifactsImportLegacy Msg = "help.artifacts.import-legacy"
	HelpArtifactsNext         Msg = "help.artifacts.next"
	HelpArtifactsDecide       Msg = "help.artifacts.decide"
	HelpArtifactsPause        Msg = "help.artifacts.pause"
	HelpArtifactsDone         Msg = "help.artifacts.done"
	HelpUninstall             Msg = "help.uninstall"
	HelpHook                  Msg = "help.hook"
	HelpHookSessionStart      Msg = "help.hook.session-start"
	HelpHookPreToolUse        Msg = "help.hook.pre-tool-use"
	HelpHookPostToolUse       Msg = "help.hook.post-tool-use"
	HelpCompletion            Msg = "help.completion"
	HelpAliases               Msg = "help.__aliases"
	HelpHelp                  Msg = "help.help"
	HelpVersion               Msg = "help.version"
	// Help: flag usage texts (flag.<name>; flag.<command path>.<name> for a different text).
	FlagAppSupportDir                 Msg = "flag.app-support-dir"
	FlagASCII                         Msg = "flag.ascii"
	FlagConfig                        Msg = "flag.config"
	FlagJSON                          Msg = "flag.json"
	FlagNoColor                       Msg = "flag.no-color"
	FlagPlain                         Msg = "flag.plain"
	FlagReadOnly                      Msg = "flag.read-only"
	FlagStateDir                      Msg = "flag.state-dir"
	FlagAlias                         Msg = "flag.alias"
	FlagLabel                         Msg = "flag.label"
	FlagManaged                       Msg = "flag.managed"
	FlagUnmanaged                     Msg = "flag.unmanaged"
	FlagProfileAdoptApply             Msg = "flag.profile.adopt.apply"
	FlagFromLegacy                    Msg = "flag.from-legacy"
	FlagLegacyAlias                   Msg = "flag.legacy-alias"
	FlagLegacyExclude                 Msg = "flag.legacy-exclude"
	FlagLegacyManaged                 Msg = "flag.legacy-managed"
	FlagLegacyNoSeed                  Msg = "flag.legacy-no-seed"
	FlagLegacyPlan                    Msg = "flag.legacy-plan"
	FlagLegacyState                   Msg = "flag.legacy-state"
	FlagQuarantine                    Msg = "flag.quarantine"
	FlagShort                         Msg = "flag.short"
	FlagInvokingSession               Msg = "flag.invoking-session"
	FlagOnly                          Msg = "flag.only"
	FlagSkip                          Msg = "flag.skip"
	FlagAcceptWorktreeRisk            Msg = "flag.accept-worktree-risk"
	FlagAllowDeletions                Msg = "flag.allow-deletions"
	FlagConfirmFirstSync              Msg = "flag.confirm-first-sync"
	FlagConfirmedPlan                 Msg = "flag.confirmed-plan"
	FlagDetach                        Msg = "flag.detach"
	FlagFromRunner                    Msg = "flag.from-runner"
	FlagJSONProgress                  Msg = "flag.json-progress"
	FlagLogInode                      Msg = "flag.log-inode"
	FlagLogOffset                     Msg = "flag.log-offset"
	FlagNoLaunch                      Msg = "flag.no-launch"
	FlagNoReopen                      Msg = "flag.no-reopen"
	FlagReopenSession                 Msg = "flag.reopen-session"
	FlagSwitchYes                     Msg = "flag.switch.yes"
	FlagYes                           Msg = "flag.yes"
	FlagAfterRun                      Msg = "flag.after-run"
	FlagRollback                      Msg = "flag.rollback"
	FlagPropagate                     Msg = "flag.propagate"
	FlagOlderThan                     Msg = "flag.older-than"
	FlagScratch                       Msg = "flag.scratch"
	FlagBundle                        Msg = "flag.bundle"
	FlagBundleLogs                    Msg = "flag.bundle-logs"
	FlagGrantAutomation               Msg = "flag.grant-automation"
	FlagRun                           Msg = "flag.run"
	FlagFinish                        Msg = "flag.finish"
	FlagSession                       Msg = "flag.session"
	FlagToken                         Msg = "flag.token"
	FlagUnavailable                   Msg = "flag.unavailable"
	FlagWait                          Msg = "flag.wait"
	FlagAnswer                        Msg = "flag.answer"
	FlagQuestion                      Msg = "flag.question"
	FlagAll                           Msg = "flag.all"
	FlagProfile                       Msg = "flag.profile"
	FlagTitles                        Msg = "flag.titles"
	FlagCapabilities                  Msg = "flag.capabilities"
	FlagCreate                        Msg = "flag.create"
	FlagPublic                        Msg = "flag.public"
	FlagRehome                        Msg = "flag.rehome"
	FlagRestore                       Msg = "flag.restore"
	FlagResume                        Msg = "flag.resume"
	FlagRoundtrip                     Msg = "flag.roundtrip"
	FlagShared                        Msg = "flag.shared"
	FlagApply                         Msg = "flag.apply"
	FlagFrom                          Msg = "flag.from"
	FlagMap                           Msg = "flag.map"
	FlagRefused                       Msg = "flag.refused"
	FlagKeepArtifacts                 Msg = "flag.keep-artifacts"
	FlagKeepBackups                   Msg = "flag.keep-backups"
	FlagKeepState                     Msg = "flag.keep-state"
	FlagHookSessionStartPluginVersion Msg = "flag.hook.session-start.plugin-version"
	FlagPluginVersion                 Msg = "flag.plugin-version"
	// Config validation (config.invalid.*).
	ConfigInvalid                         Msg = "config.invalid"
	ConfigInvalidVersion                  Msg = "config.invalid.version"
	ConfigInvalidAppBundleID              Msg = "config.invalid.app_bundle_id"
	ConfigInvalidAppDataRoot              Msg = "config.invalid.app_data_root"
	ConfigInvalidAppQuitTimeout           Msg = "config.invalid.app_quit_timeout"
	ConfigInvalidAppLateQuitWatch         Msg = "config.invalid.app_late_quit_watch"
	ConfigInvalidAppOpenFileScan          Msg = "config.invalid.app_open_file_scan"
	ConfigInvalidDurationRange            Msg = "config.invalid.duration_range"
	ConfigInvalidOnlineReconcile          Msg = "config.invalid.online_reconcile"
	ConfigInvalidOnlineGuard              Msg = "config.invalid.online_guard"
	ConfigInvalidArtifactsVerify          Msg = "config.invalid.artifacts_verify"
	ConfigInvalidArtifactsHistoryKeep     Msg = "config.invalid.artifacts_history_keep"
	ConfigInvalidIntRange                 Msg = "config.invalid.int_range"
	ConfigInvalidScratchMaxWorkspaceBytes Msg = "config.invalid.scratch_max_workspace_bytes"
	ConfigInvalidScratchMaxDeleteFraction Msg = "config.invalid.scratch_max_delete_fraction"
	ConfigInvalidScratchMaxDeleteMin      Msg = "config.invalid.scratch_max_delete_min"
	ConfigInvalidScratchExclude           Msg = "config.invalid.scratch_exclude"
	ConfigInvalidScratchCacheDirs         Msg = "config.invalid.scratch_cache_dirs"
	ConfigInvalidSize                     Msg = "config.invalid.size"
	ConfigInvalidSizeRange                Msg = "config.invalid.size_range"
	ConfigInvalidProfileAlias             Msg = "config.invalid.profile_alias"
	ConfigInvalidProfileAliasTwice        Msg = "config.invalid.profile_alias_twice"
	ConfigInvalidProfileLabel             Msg = "config.invalid.profile_label"
	ConfigInvalidProfileFolderDefault     Msg = "config.invalid.profile_folder_default"
	ConfigInvalidProfileFolderPattern     Msg = "config.invalid.profile_folder_pattern"
	ConfigInvalidProfileFolderUsed        Msg = "config.invalid.profile_folder_used"
	ConfigInvalidSyncTombstones           Msg = "config.invalid.sync_tombstones"
	ConfigInvalidSyncSecrets              Msg = "config.invalid.sync_secrets"
	ConfigInvalidSyncExclude              Msg = "config.invalid.sync_exclude"
	ConfigInvalidSyncAppPrefsUnknown      Msg = "config.invalid.sync_app_prefs_unknown"
	ConfigInvalidSyncAppPrefsTagged       Msg = "config.invalid.sync_app_prefs_tagged"
	ConfigInvalidSyncAppPrefsTwice        Msg = "config.invalid.sync_app_prefs_twice"
	ConfigInvalidSyncSecretsAllow         Msg = "config.invalid.sync_secrets_allow"
	ConfigInvalidBoundaryAlias            Msg = "config.invalid.boundary_alias"
	ConfigInvalidBoundaryCategory         Msg = "config.invalid.boundary_category"
	ConfigInvalidLimitsMaxDeleteFraction  Msg = "config.invalid.limits_max_delete_fraction"
	ConfigInvalidLimitsMaxDeleteMin       Msg = "config.invalid.limits_max_delete_min"
	ConfigInvalidBackupsKeep              Msg = "config.invalid.backups_keep"
	ConfigInvalidBackupsLeveldbClonesKeep Msg = "config.invalid.backups_leveldb_clones_keep"
	ConfigInvalidBackupsMaxSize           Msg = "config.invalid.backups_max_size"
	ErrConfigNoConfig                     Msg = "err.config.no_config"
	ErrIDsAlias                           Msg = "err.ids.alias"
	ErrIDsFolderPattern                   Msg = "err.ids.folder_pattern"
	ErrIDsFolderReserved                  Msg = "err.ids.folder_reserved"
	ErrStateNewerRoot                     Msg = "err.state.newer_root"
	ErrStateNewerRootVersion              Msg = "err.state.newer_root_version"
	ErrApplyHolderCheck                   Msg = "err.apply.holder_check"
	ErrApplyLSLocked                      Msg = "err.apply.ls_locked"
	ErrApplyLSCompression                 Msg = "err.apply.ls_compression"
	ErrApplyLSJournal                     Msg = "err.apply.ls_journal"
	ErrApplyLSOpen                        Msg = "err.apply.ls_open"
	ErrApplyReadFailed                    Msg = "err.apply.read_failed"
	ErrApplyRolledBackWritten             Msg = "err.apply.rolled_back_written"
	ErrApplyRolledBackNone                Msg = "err.apply.rolled_back_none"
	ErrApplyFullSync                      Msg = "err.apply.full_sync"
	ErrApplyChangedDuring                 Msg = "err.apply.changed_during"
	ErrApplyModeFailed                    Msg = "err.apply.mode_failed"
	ErrApplyWriteFailed                   Msg = "err.apply.write_failed"
	ErrApplyReadBackFailed                Msg = "err.apply.read_back_failed"
	ErrApplyReadBackMismatch              Msg = "err.apply.read_back_mismatch"
	ErrApplyEncodeFailed                  Msg = "err.apply.encode_failed"
	ErrApplyLSWrite                       Msg = "err.apply.ls_write"
	ErrApplyLSClose                       Msg = "err.apply.ls_close"
	ErrOpenFilesBlind                     Msg = "err.open_files.blind"
	ErrOpenFilesSelfCheck                 Msg = "err.open_files.self_check"
	ErrOpenFilesNativeFailed              Msg = "err.open_files.native_failed"
	ErrOpenFilesPartial                   Msg = "err.open_files.partial"
	ErrAppQuitTimeout                     Msg = "err.app.quit_timeout"
	ErrAppQuitRefused                     Msg = "err.app.quit_refused"
	ErrAppQuitNoReply                     Msg = "err.app.quit_no_reply"
	ErrQuitLate                           Msg = "err.quit.late"
	ErrQuitAsking                         Msg = "err.quit.asking"
	ErrQuitStillRunning                   Msg = "err.quit.still_running"
	ErrQuitFailed                         Msg = "err.quit.failed"
	ErrQuitOSAStatus                      Msg = "err.quit.osa_status"
	ErrQuitRefused                        Msg = "err.quit.refused"
	ErrQuitTimedOut                       Msg = "err.quit.timed_out"
	ErrQuitSandboxed                      Msg = "err.quit.sandboxed"
	ErrQuitNotDelivered                   Msg = "err.quit.not_delivered"
	ErrQuitNotQuit                        Msg = "err.quit.not_quit"
	ErrApplyEnteringInUse                 Msg = "err.apply.entering_in_use"
	ErrApplyEnteringNotRestored           Msg = "err.apply.entering_not_restored"
	ErrApplyLSLockedRecover               Msg = "err.apply.ls_locked_recover"
	ErrApplyLSReopen                      Msg = "err.apply.ls_reopen"
	DoctorModeReasonIdentity              Msg = "doctor.mode_reason.identity"
	DoctorModeReasonDowngraded            Msg = "doctor.mode_reason.downgraded"
	DoctorModeReasonUntested              Msg = "doctor.mode_reason.untested"
	DoctorModeReasonRowsSkipped           Msg = "doctor.mode_reason.rows_skipped"
	DoctorModeReasonNoManifest            Msg = "doctor.mode_reason.no_manifest"
	DoctorModeReasonKnownBad              Msg = "doctor.mode_reason.known_bad"
	DoctorModeReasonSQLite                Msg = "doctor.mode_reason.sqlite"
	DoctorModeReasonCompression           Msg = "doctor.mode_reason.compression"
	DoctorModeReasonNativeMulti           Msg = "doctor.mode_reason.native_multi"
	DoctorModeReasonCriticalProbe         Msg = "doctor.mode_reason.critical_probe"
	ArtifactsPlanDue                      Msg = "artifacts.plan_due"
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

	AskBusyStill:       "These sessions still look busy. Quit Claude now anyway? Claude asks before it quits a working session. [y/N] ",
	PlanBusyHeader:     "Still working (%d): the switch waits up to %s, and Claude asks before it quits a working session",
	PlanBusyLine:       "%s, working for %s",
	PlanBusyLineNoTime: "%s, working",
	PlanBusyStale:      "%d more session(s) look stuck and are not waited for",
	PlanBusyTitles:     "Working sessions:",
	PlanBusyTitled:     "%s (%s)",

	DoctorFDScanNative:       "Open files: checked with macOS's process information interface (libproc).",
	DoctorFDScanLsofConfig:   `Open files: checked with lsof, as app.open_file_scan = "lsof" asks.`,
	DoctorFDScanFallback:     "Open files: the native check is not available here, so lsof is used. Switches still work; please report this with paritydesk doctor --bundle.",
	DoctorFDScanNativeFailed: `Open files: the native check is not available here and app.open_file_scan is "native", so switches stop before writing anything. Set it to "auto".`,
	DoctorFDScanNone:         "Open files: neither the native check nor /usr/sbin/lsof is available, so paritydesk cannot confirm that Claude closed its files; switches stop before writing anything.",
	DoctorFDScanLsofTest:     "Open files: checked with lsof (test build).",
	DoctorFDScanBlind:        "Open files: paritydesk cannot read other processes' open files here (is it running inside a sandbox?), so switches stop before writing anything. Run paritydesk from Terminal.",
	DoctorFDScanPartial:      "Open files: %d process(es) could not be read and %d only in part, so lsof is used for those checks. Switches still work.",

	VerifyUnverified: "Verify: these could not be read back while Claude runs; they are checked at the next switch: %s",

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

	HookOnlinePrompt:             "paritydesk: some changes for this profile can only be made through Claude's own tools. Tell the user and offer to run /paritydesk:reconcile.",
	HookOnlineAuto:               "paritydesk: some changes for this profile can only be made through Claude's own tools. Run /paritydesk:reconcile before other work, unless the user asked for something else.",
	GuardDenyFloor:               "paritydesk: this tool is blocked while paritydesk applies changes in this session. Nothing was changed.",
	GuardDenyUnplanned:           "paritydesk: only the calls paritydesk listed may run now, with exactly the listed input. Run paritydesk online next for the list, or paritydesk online cancel in Terminal to stop.",
	GuardDenyNotYet:              "paritydesk: this call is listed but not ready yet. Run paritydesk online next first.",
	GuardDenyUnreadable:          "paritydesk could not check this call, so it was blocked. Run paritydesk online cancel in Terminal to stop applying changes.",
	GuardDenyDuplicate:           "paritydesk: this exact call is already running once. Wait for it, then run paritydesk online next.",
	GuardDenyArtifactTool:        "paritydesk is syncing artifacts in this session; Claude's own artifact tool is paused until it finishes. To stop the sync, run paritydesk online cancel in Terminal.",
	GuardDenyUnplannedArtifacts:  "paritydesk: only the calls paritydesk listed may run now, with exactly the listed input. Run paritydesk artifacts next for the list, or paritydesk online cancel in Terminal to stop.",
	GuardDenyNotYetArtifacts:     "paritydesk: this call is listed but not ready yet. Run paritydesk artifacts next first.",
	GuardDenyDuplicateArtifacts:  "paritydesk: this exact call is already running once. Wait for it, then run paritydesk artifacts next.",
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

	ArtifactsOff:                  "Artifact sync is off (sync.artifacts in the config, or not supported on this Claude version).",
	ArtifactsErrOutsideApp:        "Artifact sync runs inside Claude: use /paritydesk:artifacts in a Code session.",
	ArtifactsErrTerminalOnly:      "Run this in Terminal: decisions about artifacts are not taken inside Claude.",
	ArtifactsErrToolsMissing:      "This Claude session does not offer the artifact tools paritydesk uses, so nothing was synced. Try a new Code session; see paritydesk doctor.",
	ArtifactsErrReadOnly:          "Read-only mode: the artifact archive is not changed.",
	ArtifactsErrBusy:              "Another paritydesk command is updating the artifact archive. Try again in a moment.",
	ArtifactsErrManifestInvalid:   "The artifact archive's manifest is not valid, so nothing was changed. Its previous copies are next to it (manifest.prev-*.json).",
	ArtifactsErrBlobCorrupt:       "A file in the artifact archive does not match its checksum, so nothing was changed. Run paritydesk doctor.",
	ArtifactsErrRoundInvalid:      "paritydesk could not build the next artifact step, so nothing more was done. Run /paritydesk:artifacts again; if it repeats, see paritydesk doctor.",
	ArtifactsErrNeedsYes:          "This needs a confirmation. Run it in Terminal and answer the question.",
	ArtifactsErrUnknownPair:       "No artifact with that id or slug is in the archive. paritydesk artifacts status lists them.",
	ArtifactsErrLegacyPath:        "The folder to import must be an absolute path to a real folder outside paritydesk's and Claude's own folders.",
	ArtifactsErrLegacyFormat:      "The folder holds no artifact manifest this version can read (formats 1 and 2).",
	ArtifactsReconcileFirst:       "paritydesk has other changes waiting for this profile. Run /paritydesk:reconcile first, then /paritydesk:artifacts.",
	ArtifactsReplicaUnknown:       "paritydesk cannot tell from Claude's log which account Claude is using now, so nothing was synced. Quit and reopen Claude, then run /paritydesk:artifacts again.",
	ArtifactsReplicaChanged:       "Claude is not on the account this sync started with, so it stopped and nothing more was recorded. Run /paritydesk:artifacts again on the right account.",
	ArtifactsInventoryForeign:     "The artifact list did not come from this profile's account, so nothing was recorded. Check that Claude is on the right account, then try again.",
	ArtifactsRoundsCapped:         "Artifact sync stopped after its round limit; run /paritydesk:artifacts again to continue.",
	ArtifactsPaused:               "Artifact sync paused; nothing is held in this session. Run /paritydesk:artifacts again to continue where it stopped.",
	ArtifactsLoopCapped:           "Artifact sync is not finished yet. Run /paritydesk:artifacts again to continue where it stopped.",
	ArtifactsPublishUnavailable:   "Claude did not accept paritydesk's file for publishing, so nothing was published. Artifacts are still saved; see paritydesk doctor.",
	ArtifactsGrantDeclined:        "Publishing needs access to paritydesk's outbox folder, which was not granted, so nothing was published this time. Artifacts are still saved.",
	ArtifactsGateClosed:           "A published page read back differently from the archive, so publishing is paused for this Claude version. The archive and every copy are unchanged since then; see paritydesk artifacts status.",
	ArtifactsOtherOrgHint:         "Some copies belong to another organization of this profile. To publish there again, run paritydesk artifacts allow <id> --rehome --profile <name> in Terminal.",
	ArtifactsForgetChainActive:    "This artifact is part of a sync that is running; try again after it finished.",
	ArtifactsQHeader:              "Artifacts",
	ArtifactsQText:                "To match the other profiles, paritydesk will publish %d update(s) and %d new artifact(s) on %s; %d of them replace a change made here (kept in paritydesk's history). Claude will ask you to allow access to paritydesk's outbox folder and to approve each publish, and may open each page. Declining a card skips that artifact this time.",
	ArtifactsQApply:               "Publish",
	ArtifactsQApplyDesc:           "Publish from paritydesk's archive; new copies stay private",
	ArtifactsQSkip:                "Not now",
	ArtifactsQSkipDesc:            "Nothing is published; the next sync offers this again",
	ArtifactsDoneSummary:          "Artifacts on %s: %d saved, %d published (%d new), %d conflict(s) (newer kept, older in history), %d skipped.",
	ArtifactsDoneDetails:          "For titles and details run paritydesk artifacts status in Terminal.",
	ArtifactsDoneDeleted:          "%d artifact(s) were deleted on %s; the other profiles keep theirs. paritydesk never deletes artifacts.",
	ArtifactsDoneIgnored:          "%d artifact(s) cannot be synced (several files, a type, a live document, capabilities or another author); they are checked again when they change.",
	ArtifactsDoneRootBusy:         "paritydesk is busy with another run; it closes this sync's record with the next paritydesk command.",
	ArtifactsWarnInventorySuspect: "Unusually many artifacts look deleted on %s, so none was recorded as deleted this time.",
	ArtifactsWarnPairCollision:    "An artifact could not be added to the archive (id collision); it is skipped.",
	ArtifactsStatusSummary:        "%d artifact(s): %d in step, %d to publish, %d deleted somewhere, %d skipped, %d cannot be synced.",
	ArtifactsStatusNone:           "No artifacts are in paritydesk's archive yet. Run /paritydesk:artifacts in a Code session to save them.",
	ArtifactsAskCapabilities:      "This artifact declares capabilities (%s). Its page is copied without them: on the other profile it may not work until you declare them there. Sync it anyway?",
	ArtifactsAskShared:            "Copies of this artifact are shared with other people. Publishing changes what they see. Allow?",
	ArtifactsAskRoundtrip:         "Publishing was paused because a page read back differently. Allow publishing again for this Claude version?",
	ArtifactsAskForget:            "Stop tracking this artifact? Its archived versions stay on this Mac; nothing changes in any account.",
	ArtifactsAskRehome:            "The copy on %s belongs to an organization this profile no longer uses. Stop tracking it and create a new copy on the next sync?",
	ArtifactsAskRestore:           "The copy on %s was deleted there. Create a new copy on the next sync?",
	ArtifactsAskCreate:            "Create a new copy on %s on the next sync, even though an artifact with the same title may be there?",
	ArtifactsAskResume:            "Sync this artifact again?",
	ArtifactsAskImport:            "Import these artifacts into paritydesk's archive? The old folder is not changed.",
	ArtifactsAllowed:              "Recorded. The next /paritydesk:artifacts uses it.",
	ArtifactsAllowedPublishOff:    "Recorded. Publishing is off in this version (capture only), so the copy is created only after publishing is turned on.",
	ArtifactsSkippedPair:          "paritydesk no longer syncs this artifact. Its archived versions are kept.",
	ArtifactsForgotten:            "paritydesk no longer tracks this artifact. Its archived versions stay on this Mac.",
	ArtifactsIngested:             "The last round of the artifact sync is in the archive.",
	ArtifactsNothingToIngest:      "No finished artifact round is waiting to be recorded.",
	ArtifactsImportSummary:        "%d artifact(s) found; %d can be imported (%d without content, read on the next sync).",
	ArtifactsImportMapHint:        "%d account(s) in the old archive match no profile's current account; use --map <account>=<alias> to import their copies.",
	ArtifactsImportChainActive:    "An artifact sync is running; import after it finished.",
	ArtifactsImportApplied:        "Imported %d artifact(s) into paritydesk's archive. The old folder was not changed.",
	OnlineArtifactsNext:           "Some artifacts from another profile are not on this account yet: run /paritydesk:artifacts to publish them.",
	PlanArtifactsDue:              "  artifacts     %d to publish on %s (run /paritydesk:artifacts after the switch)",
	PlanArtifactsUnsynced:         "  artifacts     %s was not checked since it became active (run /paritydesk:artifacts first to carry its edits)",
	HookArtifactsDue:              "paritydesk: some artifacts from another profile are not on this account yet. Tell the user and offer to run /paritydesk:artifacts.",
	DoctorArtifactsOK:             "Artifacts: archive readable (%d artifact(s)), publishing %s",
	DoctorArtifactsBad:            "Artifacts: archive problem: %s",
	DoctorArtifactsOff:            "Artifacts: not synced (turned off, or not supported on this Claude version)",
	ArtifactsOffReconcile:         "Artifact sync is off because paritydesk's online layer is off (online.reconcile = \"off\" in the config).",
	ArtifactsPlanSummary:          "read %d, publish %d, create %d, deleted %d, skipped %d, new here %d",
	ArtifactsNoTitle:              "(no title)",
	ArtifactsRenameFailed:         "the artifact archive could not record the new profile name; run paritydesk artifacts status",
	DoctorArtifactsUnreadable:     "manifest.json cannot be read",
	DoctorArtifactsInvalid:        "manifest.json is not valid",
	DoctorArtifactsBlobsMissing:   "%d archived page(s) are missing",
	DoctorArtifactsGrantEachSync:  "available (grant needed each sync)",
	DoctorArtifactsCaptureOnly:    "off (capture only)",

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

	// Separators.
	WordListSep: "; ",

	// Notifications of the detached job.
	NotifySwitched: "The switch finished. Claude is open on the other account.",
	NotifyFinished: "The switch finished.",
	NotifyCheck:    "The switch finished, but the check after launch found a problem. Run paritydesk status in Terminal.",
	NotifyStopped:  "The switch did not finish. Run paritydesk status in Terminal.",
	// Log signals (busy detection, account changes, Chrome pairing org); moved from orchestrator/busytext.go.
	ConfirmBusyLine:                   "%s (%s), working for %s",
	ConfirmBusyLineNoTime:             "%s (%s)",
	ConfirmBusyUntitled:               "untitled session",
	ConfirmBusyWaited:                 "waited %s",
	ProgressP3BusyWait:                "waiting for %d session(s) to finish (Ctrl-C stops without changes)",
	WarnBusyStale:                     "%d session(s) started a turn more than %s ago and show no end in Claude's log; they are not waited for, and Claude asks before it quits if one is still working",
	WarnBusyPreview:                   "%d session(s) are still working; the switch waits up to %s for them before it quits Claude",
	WarnBusyTruncated:                 "Claude's log is large; sessions that started long ago may not be listed",
	WarnBusyLogUnreadable:             "Claude's log could not be read, so working sessions are not listed; only Claude's own quit dialog protects them",
	WarnAccountWindow:                 "Claude's log since the last switch is larger than paritydesk reads; older account changes are not seen",
	WarnAccountChange:                 "the folder of %s was signed in to another account (%s) in Claude since the last switch; run paritydesk doctor",
	WarnInFolderSignIn:                "the folder of %s holds the account of %s: it was signed in to that account in Claude; run paritydesk doctor",
	WarnNativeMulti:                   "Claude's own multi-account mode looks active in the folder of %s; the next switch only swaps folders",
	WarnSwapOnlyNativeLog:             "swap-only mode: the folder of %s changed accounts %d times in Claude since the last switch, so the switch will not carry anything",
	WarnSwapOnlyNativeHistory:         "swap-only mode: the folder of %s has held several accounts, so the switch will not carry anything",
	ErrSyncSwapOnlyNativeLog:          "sync is not possible in swap-only mode: the folder of %s changed accounts %d times in Claude since the last switch",
	ErrSyncSwapOnlyNativeHistory:      "sync is not possible in swap-only mode: the folder of %s has held several accounts",
	ErrResolveInFolderSignIn:          "the folder of %s was signed in to the account of %s in Claude; nothing was changed. In Claude, switch back to the account of %s, then run the command again; see paritydesk doctor",
	DoctorBusyDetectionOK:             "working sessions are found in Claude's log (turn start, end, interrupt and stop lines, and Claude's keep-awake hold for Code sessions); Claude's own quit dialog stays the last guard",
	DoctorBusyDetectionRecord:         "working Code sessions are detected from their records before Claude quits",
	DoctorBusyDetectionPartial:        "turn starts and ends are found in Claude's log, but not every way a turn can end; such a session can look busy for up to %s",
	DoctorBusyDetectionUnavailable:    "busy detection unavailable: this Claude version has no confirmed busy signal, so only Claude's own quit dialog protects a working Code session",
	DoctorBusyNowAppStopped:           "Claude is not running, so no session is working",
	DoctorBusyNowNone:                 "Claude's log shows no session working now",
	DoctorBusyNowSome:                 "Claude's log shows %d session(s) working now: %s (this includes the session running doctor, if any)",
	DoctorBusyNowNoSignals:            "this Claude version has no busy signal in its log, so working sessions cannot be listed; only Claude's own quit dialog protects them",
	DoctorBusyNowUnreadable:           "Claude's log could not be read, so working sessions cannot be listed; only Claude's own quit dialog protects them",
	DoctorBusyNowRecords:              "%d session(s) working now by their records: %s (this includes the session running doctor, if any)",
	DoctorBusyNowStale:                "%d more session(s) started a turn long ago with no end in the log",
	DoctorBusyNowTruncated:            "Claude's log is large; sessions that started long ago may not be listed",
	DoctorAccountChangeUnavailable:    "this Claude version has no known log line for an account change; paritydesk relies on the accounts and markers it recorded for each folder",
	DoctorAccountChangeNone:           "no account change inside a data folder in the recent Claude log",
	DoctorAccountChangeOne:            "the folder of %s (account %s) was signed in to %s at %s (from the Claude log)",
	DoctorAccountChangeUnknown:        "a folder that held account %s, which no profile registers, was signed in to %s at %s (from the Claude log)",
	DoctorAccountChangeInFolder:       "Claude holds the folder of %s, signed in to the account of %s; paritydesk refuses to run until Claude uses the account of %s in that folder again",
	DoctorAccountChangeNativeLog:      "the folder of %s changed accounts %d times since the last switch: Claude's own multi-account mode looks active, so the next switch only swaps folders and carries nothing",
	DoctorAccountChangeNativeHistory:  "the folder of %s has held several accounts (recorded history): switches only swap folders and carry nothing",
	DoctorAccountChangeUnknownTime:    "an unknown time",
	DoctorChromePairingOrgKnown:       "Claude in Chrome pairs with the organization active in Claude (%s, from the Claude log; an organization override set by an administrator would win) and pairs again when the account or organization changes; no other organization stays pinned. Use one Chrome profile per account",
	DoctorChromePairingOrgUnknown:     "the active organization is not in Claude's log for this launch, so it cannot be shown; Claude in Chrome always pairs with the organization active in Claude",
	DoctorChromePairingOrgState:       "the last local pairing state Claude logged since it started: %s",
	DoctorChromeBridgePairingDisabled: "Claude turned off Claude in Chrome's local pairing for its last launch because its data folder was not in the usual place; paritydesk never moves it, so another tool may have started Claude with a different folder",
	DoctorVersionDeprecatedKey:        "a compatibility manifest override sets %s, which paritydesk no longer uses; remove it from the override",
	// Orchestrator progress, warnings, doctor details and recover actions (migrated literals).
	ProgressP5FinalPlan:                "final plan",
	ProgressP6BackingUp:                "backing up",
	ProgressP7Applying:                 "applying",
	WarnEnteringInUseNotRestored:       "the entering folder is in use, so it was not restored; quit Claude, then run paritydesk recover",
	WarnEnteringNotFullyRestored:       "the entering folder could not be fully restored; run paritydesk recover",
	ProgressP8SwappingFolders:          "swapping folders",
	WarnPostSwapCheckNotRun:            "the post-swap check could not run",
	ProgressP9Parking:                  "parking",
	WarnLeavingMarkerNotWritten:        "the leaving folder's marker could not be written; the next switch back merges without deleting",
	WarnRunInterrupted:                 "a run was interrupted; run paritydesk recover",
	ProfileProblemFolderMissing:        "folder missing",
	WarnFoldersUnreadable:              "the data folders could not be read",
	WarnProfilesMismatch:               "profiles do not match their folders; run paritydesk doctor",
	DoctorStateUnreadable:              "root.json is unreadable; run paritydesk recover",
	DoctorFoldersUnreadable:            "the data folders could not be read",
	DoctorVersionNotFound:              "Claude was not found",
	DoctorForeignToolsNone:             "no other account-switching tool",
	DoctorStateInterrupted:             "a run was interrupted; run paritydesk recover",
	DoctorStateOK:                      "no open run",
	DoctorOrgUnknown:                   "the active organization is unknown; per-organization rows are skipped (exit 32)",
	DoctorUnclassifiedNone:             "every entry in Claude is classified",
	DoctorAutomationNotChecked:         "not checked; paritydesk doctor --grant-automation asks macOS for the permission",
	DoctorAutomationMissing:            "Automation permission for Claude is missing; allow it in System Settings > Privacy & Security > Automation",
	DoctorAutomationGranted:            "Automation permission for Claude is granted",
	DoctorUpdateUnreadable:             "could not check for a staged update",
	DoctorUpdateStaged:                 "a Claude update is staged; the next switch lets it install, then re-checks the new version",
	DoctorUpdateNone:                   "no staged update",
	DoctorChromeBridgeUnreadable:       "the Claude log could not be read",
	DoctorChromeBridgeConnected:        "the recent Claude log shows a Claude in Chrome connection",
	DoctorManagedAll:                   "every profile is marked managed_by_org",
	DoctorRunnerNone:                   "no detached jobs",
	DoctorRunnerNoStale:                "no stale detached jobs",
	ProgressP0AutomationMissing:        "Automation permission for Claude is missing; paritydesk doctor --grant-automation asks again",
	ProgressP0AutomationGranted:        "Automation permission for Claude is granted",
	ProgressP11LaunchFailedSignIn:      "Claude could not be launched; open it manually to sign in",
	ProgressP10ObjectsNotRemoved:       "unreferenced state objects could not be removed",
	ProgressP11RelaunchedClaude:        "relaunched Claude",
	ProgressP12NotSettled:              "Claude did not settle in time; verifying what is there",
	WarnAccountChangedOutside:          "the active account changed outside paritydesk; paritydesk sync re-applies the shared state",
	ProgressP10Recording:               "recording",
	WarnEnteringNotReread:              "the entering folder could not be re-read; its bases are invalid until the next run",
	WarnObjectsNotRemoved:              "unreferenced state objects could not be removed",
	WarnGenerationsNotPruned:           "old backup generations could not be pruned",
	ProgressP11Launching:               "launching",
	ProgressP11NotLaunched:             "not launched (--no-launch)",
	ProgressP11NotRunningBefore:        "Claude was not running before; not launched",
	WarnLaunchNoProfile:                "Claude was not launched: the active folder holds no registered profile",
	WarnLaunchFailed:                   "Claude could not be launched; open it manually",
	WarnInvokingNotInProfile:           "the invoking session is not in the entering profile; it was not reopened",
	WarnInvokingNotReopened:            "the invoking session could not be reopened",
	ProgressP12BackgroundCheck:         "Claude is up; the values are checked in the background (paritydesk verify shows the result)",
	ProgressP12Verifying:               "verifying",
	ProgressP10EmptyGenerationKept:     "an empty backup generation could not be removed",
	ProgressP10OnlineNotQueued:         "the changes for inside Claude could not be queued; the next switch plans them again",
	WarnOnlineLeaseNotRemoved:          "the lease of the changes waiting for inside Claude could not be removed",
	ProgressP2AcksUnreadable:           "permission acknowledgements of the entering folder could not be read",
	RecoverNothing:                     "nothing to recover",
	RecoverRelaunched:                  "relaunched Claude",
	RecoverPutBack:                     "put the leaving profile back into Claude",
	RecoverRemovedGeneration:           "removed the incomplete backup generation",
	RecoverMovedEntering:               "moved the entering profile into Claude",
	RecoverEnteringBasesInvalid:        "the entering profile's bases are invalid; the next run is a first sync for it",
	RecoverOnlineRequeued:              "the changes for inside Claude are queued again",
	RecoverLaunchedForSignIn:           "launched Claude for the sign-in",
	RecoverLeftPendingAdd:              "left the pending profile add as it is",
	RecoverNoJournal:                   "no journal; Claude is in place, nothing to move",
	ProgressP11RelaunchNoProfile:       "Claude was not relaunched: the active folder holds no registered profile",
	ProgressP11LaunchFailed:            "Claude could not be launched; open it manually",
	ProgressP0Checking:                 "checking",
	WarnUpdateUnreadable:               "could not check for a staged Claude update",
	WarnUpdateStaged:                   "a Claude update is staged; it will install when Claude quits",
	ProgressP0UpdateWait:               "waiting for the Claude update to install",
	WarnDegraded:                       "degraded mode: some rows are skipped",
	WarnAppleEventTimeout:              "Claude did not answer an Apple Event in time (AppleScript error -1712); if macOS asks whether paritydesk may control Claude, allow it",
	WarnInvokingNotFound:               "the invoking session was not found; it will not be reopened",
	ProgressP2Planning:                 "planning",
	ProgressP3Quitting:                 "quitting Claude",
	WarnInvokingTurnNotEnded:           "the invoking session's turn did not end in time; Claude may ask before quitting",
	ProgressP3QuitDialog:               "Claude asks whether to quit while work is running; choose Wait for Claude to let the switch continue, or Cancel",
	ProgressP3LateQuitWatch:            "Claude did not quit in time; watching for a late quit",
	WarnReadOnlyHolder:                 "a read-only process (indexing or backup) has a file open; continuing",
	ProgressP4Waiting:                  "waiting for files to close",
	ProgressP2ReadOnly:                 "read-only mode: nothing was changed",
	ProgressP0Detached:                 "the switch continues in the background",
	WordCommaSep:                       ", ",
	WordList:                           "%s",
	WarnSwapOnly:                       "swap-only mode: the switch will not carry anything",
	ConfirmOutsideSwitchLine:           "The active account changed outside paritydesk. paritydesk sync can re-apply the shared state; continue with the switch?",
	ProgressP11Relaunched:              "relaunched %s",
	ProgressP0Updated:                  "Claude updated to %s",
	ConfirmDeletionLine:                "%s: %s",
	ErrPreflightSessionsBusy:           "%d Code session(s) are still working",
	ErrQuitSessionsBusy:                "%d Code session(s) are still working; Claude was not asked to quit",
	WarnPendingAdd:                     "profile add of %s waits for its sign-in; after signing in, run paritydesk recover",
	DoctorForeignToolsFound:            "another account-switching tool was detected: %s",
	DoctorStatePendingAdd:              "profile add of %s waits for its sign-in",
	DoctorVersionTested:                "Claude %s (tested), mode %s",
	DoctorVersionUntested:              "Claude %s (untested), mode %s",
	DoctorSkippedRows:                  "a run would skip: %s",
	DoctorOrgActive:                    "active organization %s",
	DoctorProbesLSUnreadable:           "%s: Local Storage could not be read",
	DoctorProbesNotRun:                 "%s: probes could not run",
	DoctorProbesFailed:                 "%s: %d of %d probes failed: %s",
	DoctorProbesPassed:                 "%s: %d probes passed",
	DoctorIdentityNone:                 "%s: no account recorded",
	DoctorIdentityOK:                   "%s: account %s (%s)",
	DoctorIdentityUnregistered:         "%s: account %s (unregistered)",
	DoctorMarkerNone:                   "%s: no marker; its bases are rebuilt on the next run",
	DoctorMarkerOther:                  "%s: the marker names another profile; its bases are invalid",
	DoctorMarkerOK:                     "%s: marker matches %s",
	DoctorUnclassifiedSome:             "%d unclassified entries in Claude (never touched; add them to the compat manifest): %s",
	DoctorChromeBridgeNotUsed:          "Claude in Chrome was not used in the recent Claude log; it connects on first use. The extension follows the Chrome profile's sign-in, not the Claude folder: use one Chrome profile per account",
	DoctorManagedHint:                  "if an account of %s belongs to an organization, set managed_by_org = true for it",
	DoctorRunnerStale:                  "%d detached job(s) never finished; they are removed on the next detached switch",
	ProgressP0ChromeNote:               "Claude in Chrome follows the Chrome profile's sign-in, not the Claude folder: use one Chrome profile per account",
	ProgressP0InitAdd:                  "config: add profile %s (account %s)",
	ProgressP0UnregisteredFolder:       "%s holds a signed-in account that no profile registers; register it with paritydesk profile adopt <alias> %s instead of profile add, or adopt every such folder at once as described in docs/migration-from-legacy.md",
	ProgressP0ForeignTool:              "another account-switching tool was detected: %s",
	ProgressP11AddSignIn:               "sign in to the new account in Claude, then run paritydesk recover to register it as %s",
	ProgressP10Removed:                 "removed %s; its folder was left as it is",
	ConfirmRollbackLine:                "Restore generation %d of %s (%d item(s)); items changed since are left as they are.",
	ConfirmRollbackPropagate:           "Propagate the restored values to other profiles: %s.",
	DoctorAccountChangeToProfile:       "%s (account %s)",
	WordSentences:                      "%s %s",
	ProfileWhyNoState:                  "no recorded state for this profile",
	ProfileWhyFolderMissing:            "the profile's folder is missing",
	ProfileWhyIdentityUnknown:          "the folder's account could not be read",
	ProfileWhyNoMarker:                 "the folder has no marker",
	ProfileWhyMarkerAlias:              "the folder's marker names another profile",
	ProfileWhyMarkerReplica:            "the folder's marker names another account or org",
	ProfileWhyMarkerBase:               "the folder's marker does not match the recorded bases",
	ProfileWhyFolderID:                 "the folder is not the recorded folder (device, inode or birth time differ)",
	ProfileWhyReplicaChanged:           "the profile's account or org changed since the bases were recorded",
	ErrResolveMismatch:                 "folder %s holds a different account than profile %s expects",
	WarnRowNotReadBack:                 "%s: the entering folder does not read back as planned; its base is invalid",
	ProgressP10Registered:              "registered %d profile(s)",
	RecoverLeftoversMoved:              "%d of %d leftover directories were moved into backup generation %d; paritydesk rollback %d puts them back",
	ProgressP2BaseUnreadable:           "%s: the base of %s is unreadable and is treated as invalid",
	ProgressP2PresetsDiffer:            "%s: %d preset(s) differ between Local Storage and ccd-session-presets.json; presets are not written until they agree",
	ProgressP2StaleValues:              "%s: %d stale Local Storage value(s) ignored; the config mirror wins",
	ProgressP2EnteringUnreadable:       "%s: the entering folder's state could not be read; the row is left unchanged",
	ProgressP12LSUnreadable:            "Local Storage could not be read back while Claude runs; checked at the next switch: %s",
	ProgressP12LSRowUnreadable:         "%s: Local Storage could not be read back while Claude runs; checked at the next switch",
	RecoverMovedLeaving:                "moved the leaving profile's folder to %s",
	RecoverConflictsLeft:               "%d item(s) changed since the run were left as they are",
	RecoverRollbackFinished:            "finished restoring generation %d (%d restored, %d left as they are)",
	RecoverPutBackAdd:                  "put %s back into Claude; run paritydesk profile add again",
	RecoverAddWaits:                    "the profile add waits for the sign-in of %s",
	ConfirmAddBindLine:                 "Claude now holds account %s; register it as %s?",
	RecoverAddDropped:                  "Claude already holds profile %s; the pending profile add of %s was dropped",
	RecoverRegistered:                  "registered the new account as %s",
	ConfirmFoldersMismatch:             "The folders do not match the interrupted run: %s",
	ErrRecoverMismatchClaude:           "The folders do not match the interrupted run: %s; nothing was moved; see paritydesk doctor",
	ConfirmMoveFolder:                  "Move %s (profile %s) into Claude?",
	RecoverMoved:                       "moved %s into Claude",
	ErrRecoverMismatchNoMove:           "The folders do not match the interrupted run: %s; no folder can be moved safely",
	ConfirmNoJournal:                   "No journal was found and Claude is missing.",
	ConfirmMoveRecent:                  "Move %s (the most recently used profile folder) into Claude?",
	RecoverFolderUnidentified:          "%s: unidentified",
	RecoverFolderAccount:               "%s: account %s (%s)",
	RecoverFolderUnregistered:          "%s: account %s (unregistered)",
	RecoverNoFolders:                   "no data folders",
	RecoverRolledBack:                  "rolled back %s from the generation",
	RecoverCompleted:                   "completed the interrupted run; %s is active",
	RecoverClosedLeftoversUnmoved:      "closed the interrupted move of leftovers; nothing was moved",
	RecoverClosedLeftoversGenRemoved:   "closed the interrupted move of leftovers; nothing was moved and its empty backup generation was removed",
	RecoverClosedLeftovers:             "closed the interrupted move of leftovers",
	RecoverClosedSwitch:                "closed the interrupted switch; nothing was changed in the folders",
	RecoverClosedRunUnchanged:          "closed the interrupted run; the folders were not changed",
	RecoverClosedSync:                  "closed the interrupted sync; nothing was changed",
	RecoverRestoredActive:              "restored the active profile from the generation",
	RecoverClosedRollbackUnrestored:    "closed the interrupted rollback; nothing was restored",
	RecoverClosedRollback:              "closed the rollback",
	RecoverClosedAddUnmoved:            "closed the interrupted profile add; nothing was moved",
	RecoverClosedAdd:                   "closed the interrupted profile add",
	RecoverClosedRun:                   "closed the interrupted run",
	ErrInterrupted:                     "a run was interrupted; run paritydesk recover",
	ErrArtifactsUnknownQuestion:        "unknown question or answer",
	ErrArtifactsUnknownDecision:        "unknown decision",
	ErrArtifactsProfileNamesProfile:    "--profile names the profile",
	ErrArtifactsCopyNotOtherOrg:        "the copy on that profile is not in another organization",
	ErrAppUpdateNotInstalled:           "the staged update did not finish installing in time",
	ErrAppTurnNotEnded:                 "the session's turn did not end in time",
	ErrPlansHashInvalid:                "the confirmed plan hash is not valid",
	ErrPlansNotFound:                   "the confirmed plan was not found; run paritydesk plan again",
	ErrPlansUnreadable:                 "the confirmed plan is unreadable",
	ErrPlansHashMismatch:               "the confirmed plan does not match its hash",
	ErrInterruptedFirst:                "a run was interrupted; run paritydesk recover first",
	ErrPendingAddFirst:                 "a profile add is waiting for its sign-in; run paritydesk recover first",
	ErrLogNoLog:                        "no log for that run",
	ErrStateLockBusy:                   "another paritydesk run holds the lock for this data root",
	ErrOnlineUnknownAnswer:             "unknown answer for this question",
	ErrOnlineNoQuestion:                "no such question is open",
	ErrPreflightInterrupted:            "a previous run was interrupted; run paritydesk recover",
	ErrPendingAdd:                      "a profile add is waiting for its sign-in; run paritydesk recover",
	ErrResolveNoActiveProfile:          "the active folder holds no registered profile",
	ErrSwitchNoOtherProfile:            "no other profile to switch to; register one with paritydesk profile add or profile adopt",
	ErrCompatRefused:                   "this Claude version or folder layout cannot be handled safely",
	ErrCompatUntested:                  "this Claude version is untested and require_tested is set; only plan works",
	ErrCompatSyncSwapOnly:              "sync is not possible in swap-only mode",
	ErrPreflightDiskSpace:              "not enough free disk space for the backup generation",
	ErrPreflightAutomationMissing:      "Automation permission for Claude is missing",
	ErrConfirmedPlanFirstSync:          "the confirmed plan was not a first sync, but this run is; run paritydesk plan again",
	ErrPlanFirstSync:                   "a first-sync union needs confirmation",
	ErrQuiescenceInUse:                 "files in the data folders are still in use",
	ErrLegacyDegraded:                  "registration is complete, but some leftovers were kept for now or a marker could not be written",
	ErrSwitchDetachedNeedsConfirmation: "a detached switch needs --confirmed-plan or --yes",
	ErrSyncInsideClaude:                "sync quits Claude; run it from Terminal, not from inside Claude",
	ErrRollbackReadOnly:                "rollback writes; it cannot run in read-only mode",
	ErrRunOpen:                         "a run is open; run paritydesk recover first",
	ErrRollbackNoGeneration:            "there is no generation to roll back",
	ErrRollbackNotQuit:                 "Claude did not quit; nothing was restored",
	ErrRebaseReadOnly:                  "rebase writes; it cannot run in read-only mode",
	ErrOnlineDoneReadOnly:              "online done writes; it cannot run in read-only mode",
	ErrConfirmNoTTY:                    "a confirmation is needed; run in a terminal or pass --yes",
	ErrConfirmDeclined:                 "declined",
	ErrStateOtherDataRoot:              "the state belongs to a different data root; pass the matching --state-dir",
	ErrSwitchSwapOnly:                  "switched without syncing (swap-only mode)",
	ErrSwitchDegraded:                  "some rows were skipped",
	ErrHarvestAppChanged:               "Claude changed during the run (an update installed?)",
	ErrQuiescenceOpened:                "Claude or another process opened the data folders during the run; nothing was changed",
	ErrSwapInterrupted:                 "the folder swap was interrupted; run paritydesk recover",
	ErrSwapProcessStarted:              "a process started during the folder swap; quit Claude, then run paritydesk recover",
	ErrSwapFoldersOpened:               "a process opened the data folders during the swap; quit it, then run paritydesk recover",
	ErrSwapBackFailed:                  "swapping back failed; run paritydesk recover",
	ErrSwapUndone:                      "a process started during the swap; the switch was undone",
	ErrVerifyNoActive:                  "no active profile; run paritydesk init",
	ErrVerifyNoManifest:                "no compatibility manifest covers this Claude version",
	ErrVerifyDrift:                     "some values differ from what was written; the drift is recorded",
	ErrRecoverReadOnly:                 "recover writes; it cannot run in read-only mode",
	ErrRecoverAppNotFound:              "Claude could not be found, so recover cannot tell whether it is running; quit Claude, make sure it is in Applications, then run paritydesk recover again",
	ErrRecoverQuitFirst:                "quit Claude, then run paritydesk recover again",
	ErrRecoverInUse:                    "a process still uses the data folders; quit Claude (and any copy started with --user-data-dir), then run paritydesk recover again",
	ErrRecoverNoJournal:                "no journal and no profile folder to move into place",
	ErrInitNoAccount:                   "Claude holds no signed-in account; open Claude and sign in first",
	ErrProfileRemoveActive:             "the active profile cannot be removed; switch to another profile first",
	ErrApplyInUse:                      "the folder is in use: %s",
	ErrApplyChanged:                    "%s changed since the plan was made",
	ErrApplyCopySourceChanged:          "the copy source of %s changed since the plan was made",
	ErrOpenFilesNativeUnavailable:      "the native open-file scan is not available here; set app.open_file_scan to \"auto\" or \"lsof\"",
	ErrValidateBreaker:                 "%d deletions in %s exceed the limit (%d of %d items)",
	ErrValidateBreakerEmpty:            "%d deletions would leave %s empty (%d items before)",
	ErrValidateWorktreeRisk:            "%d worktrees with uncommitted changes would lose their registration",
	ErrForeignTool:                     "another account-switching tool was detected: %s",
	ErrLegacyAliasNotFolder:            "--legacy-alias names %s, which is not a folder next to Claude",
	ErrLegacyExcludeNotFolder:          "--legacy-exclude names %s, which is not a folder next to Claude",
	ErrLegacyState:                     "--legacy-state: %s",
	ErrSwitchNoPrevious:                "no previous profile; name the profile to switch to: %s",
	ErrSwitchUnknownProfile:            "unknown profile %s",
	ErrSwitchAlreadyActive:             "%s is already active; use paritydesk sync to re-apply",
	ErrSwitchFolderMissing:             "the folder of %s is missing",
	ErrPreflightParkTaken:              "%s already exists, so %s cannot be parked there; nothing was changed; see paritydesk doctor",
	ErrConfirmedPlanNewDestructive:     "the plan now deletes %d item(s) the confirmed plan did not; run paritydesk plan again",
	ErrRollbackNoProfile:               "generation %d belongs to no registered profile",
	ErrRollbackOtherAccount:            "generation %d was taken from another account than %s holds now",
	ErrRollbackFolderAccount:           "%s does not hold the account of %s; nothing was restored",
	ErrRollbackInUse:                   "a process still uses the data folder; quit Claude (and any copy started with --user-data-dir), then run the rollback again; nothing was restored",
	ErrHarvestNewDestructive:           "the final plan deletes %d item(s) the preview did not; nothing was changed",
	ErrParkTaken:                       "%s already exists; folders hold accounts %s and %s",
	ErrVerifyIdentity:                  "the active folder does not hold %s's account",
	ErrVerifySignIn:                    "sign in with %s's account only; every other account stays signed in",
	ErrVerifyDriftNotRecorded:          "some values differ from the last run's bases; nothing was recorded, because a later difference may be your own edit",
	ErrRecoverParkTaken:                "%s already exists, so the leaving profile cannot be parked; run paritydesk recover --rollback to undo the switch; folders: %s",
	ErrRecoverNameTaken:                "%s already exists; folders: %s",
	ErrReadOnlyWrites:                  "%s writes; it cannot run in read-only mode",
	ErrProfileExists:                   "profile %s already exists",
	ErrInitRegistered:                  "the active account is already registered as %s",
	ErrProfileAdoptNotFound:            "folder %s was not found",
	ErrProfileAdoptNoAccount:           "%s holds no signed-in account",
	ErrProfileAdoptRegistered:          "%s holds the account of profile %s",
	ErrProfileAddTaken:                 "%s already exists; nothing was moved",
	// Command-line errors (usageError).
	UsageProfileFlag:           "--profile: %v",
	UsageProfileNoneActive:     "--profile names the profile (no profile is active)",
	UsageAllowOneOf:            "artifacts allow takes exactly one of --%s",
	UsageAllowRoundtripNoID:    "artifacts allow --roundtrip takes no id",
	UsageAllowNeedsID:          "artifacts allow <id> --%s",
	UsageImportLegacyFrom:      "artifacts import-legacy needs --from <folder>",
	UsageMapSyntax:             "--map takes <account-uuid>=<alias>",
	UsageUnavailableSyntax:     "--unavailable takes tool names",
	UsageRefusedSyntax:         "--refused takes step ids such as a001",
	UsageQuestionAnswer:        "--question and --answer take ids",
	UsageMissingCommand:        "missing command; run paritydesk help %s",
	UsageUnknownCommand:        "unknown command %q",
	UsageSyntax:                "%s %s",
	UsageUnknownShell:          "unknown shell %q (one of %s)",
	UsageOutOfRange:            "--%s must be between 0 and %d",
	UsageFlagValue:             "--%s: %v",
	UsageFaultAfter:            "%s=after:%s: that phase has no intent to stop after; use one of %s",
	UsageFaultPausePhase:       "%s=pause:%s: only pause:%s is supported",
	UsageFaultPauseRefused:     "%s=pause:%s is refused: %v",
	UsageFaultUnknown:          "%s=%q is not understood; use after:<phase> or pause:%s",
	UsageCategoryArtifacts:     "--%s: artifacts are synced by /paritydesk:artifacts, not by switch",
	UsageUnknownCategory:       "--%s: unknown category %q (one of %s)",
	UsageCategoryTwice:         "--%s: category %q given twice",
	UsageCategoryOnlyAndSkip:   "category %q is in both --only and --skip",
	UsageBadAlias:              "%q is not a profile alias (a-z, 0-9 and -, starting with a letter or digit)",
	UsageBadGeneration:         "%q is not a generation number",
	UsageSessionUuid:           "--%s must be a session UUID",
	UsageNegative:              "--%s must not be negative",
	UsagePlanHash:              "--%s must be the plan_hash printed by paritydesk plan --json",
	UsageAbsolutePath:          "--%s must be an absolute path",
	UsageLegacyAliasSyntax:     "--%s takes <folder>=<alias>, where <folder> is Claude or a Claude-<name> folder",
	UsageLegacyAliasTwice:      "--%s names %s twice",
	UsageLegacyExcludeSyntax:   "--%s takes Claude or a Claude-<name> folder",
	UsageLegacyPlanHash:        "--%s must be the plan hash the dry run printed",
	UsageManagedBoth:           "--managed and --unmanaged exclude each other",
	UsageLegacyManagedBoth:     "--%s excludes --managed and --unmanaged",
	UsageLegacyManagedRequired: "--managed or --unmanaged is required when there is no terminal to ask (or --%s)",
	UsageLegacyManagedUnknown:  "--%s: %s is not a profile this run creates",
	UsageLegacyAliasRequired:   "pass --%s Claude=<alias> when there is no terminal to ask",
	UsageOlderThan:             "--%s takes a number of days and needs --%s",
	UsageRunFlag:               "--run: not a run id",
	UsageSessionFlag:           "--session must be a session UUID",
	UsageTokenFlag:             "--token must be 32 lower-case hex digits",
	UsageBadRunID:              "--%s is not a run id",
	UsageLegacyNoArgs:          "profile adopt --from-legacy takes no arguments",
	UsageNeedsFlag:             "--%s needs --%s",
	UsageBadFolder:             "%q is not a parked folder name (Claude-<name>)",
	UsageManagedRequired:       "--managed or --unmanaged is required when there is no terminal to ask",
	UsageAliasRequired:         "--alias is required when there is no terminal to ask",
	UsageLabelTooLong:          "--label: at most %d printable characters",
	UsageFlag:                  "%s",
	// Help: command short texts (help.<path>) and the help frame.
	HelpUsage:                 "usage: %s %s",
	HelpCommands:              "commands:",
	HelpFlags:                 "flags",
	HelpGlobalFlags:           "global flags",
	HelpDefault:               "(default %s)",
	HelpRoot:                  "Switch the Claude desktop app between accounts and keep everything else in step.",
	HelpInit:                  "Set up paritydesk for the account in the app now",
	HelpProfile:               "Manage profiles",
	HelpProfileList:           "List profiles",
	HelpProfileAdopt:          "Register a parked folder after checking its identity",
	HelpProfileAdd:            "Add an account without signing out of any other",
	HelpProfileRename:         "Rename a profile",
	HelpProfileRemove:         "Unregister a profile; its folder is kept",
	HelpStatus:                "Show the active profile and anything pending",
	HelpPlan:                  "Preview what a switch would change (approximate)",
	HelpSwitch:                "Switch to another profile",
	HelpSync:                  "Re-apply the shared state to the active profile",
	HelpVerify:                "Check that the app kept what the last run wrote",
	HelpRecover:               "Finish or undo an interrupted run",
	HelpRollback:              "Restore a generation without overwriting newer work",
	HelpRebase:                "Forget every sync base; the next run is a first sync",
	HelpGenerations:           "List or show backup generations",
	HelpGenerationsList:       "List generations",
	HelpGenerationsShow:       "Show one generation",
	HelpBackups:               "Prune or purge backup generations",
	HelpBackupsPrune:          "Apply the retention policy",
	HelpBackupsPurge:          "Remove every generation",
	HelpDoctor:                "Check the installation, folders and app compatibility",
	HelpLog:                   "Show the log of a run",
	HelpHistory:               "List past runs",
	HelpConfig:                "Show, locate or validate the config file",
	HelpConfigPath:            "Print the config file path",
	HelpConfigShow:            "Print the effective config",
	HelpConfigValidate:        "Validate the config file",
	HelpOnline:                "Show or drop changes waiting to be applied in Claude",
	HelpOnlineStatus:          "Show the changes waiting to be applied in Claude",
	HelpOnlineCancel:          "Drop the waiting changes; nothing in Claude changes",
	HelpOnlineCapture:         "Internal: read-only calls before a switch (used by /paritydesk:switch)",
	HelpOnlineNext:            "Internal: the next calls to make (used by /paritydesk:reconcile)",
	HelpOnlineDecide:          "Internal: record an answer (used by /paritydesk:reconcile)",
	HelpOnlineRetry:           "Offer the changes that were not applied once more (then run /paritydesk:reconcile)",
	HelpOnlineDone:            "Internal: finish applying changes (used by /paritydesk:reconcile)",
	HelpArtifacts:             "Keep claude.ai artifacts the same on every profile (beta)",
	HelpArtifactsStatus:       "Show the artifact archive",
	HelpArtifactsPlan:         "Preview what a sync on a profile would do (approximate)",
	HelpArtifactsIngest:       "Record a finished sync round now",
	HelpArtifactsAllow:        "Decide about one artifact (Terminal only)",
	HelpArtifactsSkip:         "Stop syncing an artifact (it stays in the archive)",
	HelpArtifactsForget:       "Stop tracking an artifact (Terminal only)",
	HelpArtifactsImportLegacy: "Import the artifact archive of an earlier folder-swap script",
	HelpArtifactsNext:         "Internal: the next calls of an artifact sync (used by /paritydesk:artifacts)",
	HelpArtifactsDecide:       "Internal: record an answer (used by /paritydesk:artifacts)",
	HelpArtifactsPause:        "Internal: stop an artifact sync for now",
	HelpArtifactsDone:         "Internal: finish an artifact sync (used by /paritydesk:artifacts)",
	HelpUninstall:             "Remove paritydesk's state; every data folder is kept",
	HelpHook:                  "Internal: entry point of the plugin's hooks",
	HelpHookSessionStart:      "SessionStart hook",
	HelpHookPreToolUse:        "PreToolUse hook",
	HelpHookPostToolUse:       "PostToolUse hook",
	HelpCompletion:            "Print a shell completion script",
	HelpAliases:               "Internal: list profile aliases for completion",
	HelpHelp:                  "Show help for a command",
	HelpVersion:               "Print the version",
	// Help: flag usage texts (flag.<name>; flag.<command path>.<name> for a different text).
	FlagAppSupportDir:                 "paritydesk support directory",
	FlagASCII:                         "ASCII only",
	FlagConfig:                        "config file (also PARITYDESK_CONFIG)",
	FlagJSON:                          "print one JSON document",
	FlagNoColor:                       "no colour (also NO_COLOR, TERM=dumb)",
	FlagPlain:                         "plain lines, no tables or spinners",
	FlagReadOnly:                      "plan only; never write (also PARITYDESK_READONLY=1)",
	FlagStateDir:                      "state directory (also PARITYDESK_STATE_DIR)",
	FlagAlias:                         "alias for the account in Claude now (asked when missing)",
	FlagLabel:                         "display label for the profile",
	FlagManaged:                       "the account is managed by an employer or client",
	FlagUnmanaged:                     "the account is not managed by an employer or client",
	FlagProfileAdoptApply:             "(hidden) make the changes (default: dry run)",
	FlagFromLegacy:                    "(hidden) adopt every folder an earlier folder-swap script left next to Claude",
	FlagLegacyAlias:                   "(hidden) <folder>=<alias>; may be repeated",
	FlagLegacyExclude:                 "(hidden) leave this folder out; may be repeated",
	FlagLegacyManaged:                 "(hidden) comma list of new aliases that are managed; every other new profile is unmanaged",
	FlagLegacyNoSeed:                  "(hidden) register only; seed no sync bases",
	FlagLegacyPlan:                    "(hidden) plan hash printed by the dry run",
	FlagLegacyState:                   "(hidden) absolute path of the earlier script's state file (a hint only)",
	FlagQuarantine:                    "(hidden) with --apply: move other accounts' leftovers into a backup generation (needs a terminal)",
	FlagShort:                         "one line; never opens Local Storage",
	FlagInvokingSession:               "the Claude Code session running this command, left out of the busy list",
	FlagOnly:                          "categories to carry, comma-separated: %s",
	FlagSkip:                          "categories to leave alone, comma-separated",
	FlagAcceptWorktreeRisk:            "switch even if a worktree with uncommitted changes loses its registration",
	FlagAllowDeletions:                "allow up to N deletions per category past the breaker",
	FlagConfirmFirstSync:              "confirm the first-sync union that the confirmed plan showed",
	FlagConfirmedPlan:                 "hash of the plan you confirmed (printed by paritydesk plan --json)",
	FlagDetach:                        "run as a detached launchd job",
	FlagFromRunner:                    "internal: this process is the detached job",
	FlagJSONProgress:                  "NDJSON progress on stderr",
	FlagLogInode:                      "internal: inode of the main.log the offset belongs to",
	FlagLogOffset:                     "internal: main.log offset recorded before the hand-off",
	FlagNoLaunch:                      "do not relaunch Claude",
	FlagNoReopen:                      "do not reopen the invoking session",
	FlagReopenSession:                 "Claude Code session id of the invoking session (set by the plugin; empty: no reopen)",
	FlagSwitchYes:                     "do not ask (destructive differences after the preview still ask)",
	FlagYes:                           "do not ask",
	FlagAfterRun:                      "run id a background check verifies (set by paritydesk)",
	FlagRollback:                      "undo the interrupted run instead of completing it",
	FlagPropagate:                     "carry the restored values to other profiles on the next switch",
	FlagOlderThan:                     "with --scratch: only sets older than this many days",
	FlagScratch:                       "remove the kept sets of scratch files removed by earlier switches (asks first)",
	FlagBundle:                        "write a redacted support archive (tar.gz) to this new file",
	FlagBundleLogs:                    "newest run logs to put in the archive",
	FlagGrantAutomation:               "ask macOS for Automation permission now",
	FlagRun:                           "run id (default: the last run)",
	FlagFinish:                        "close the capture",
	FlagSession:                       "the Claude Code session running this command",
	FlagToken:                         "the token of the first online next answer",
	FlagUnavailable:                   "tools the session could not load, comma-separated",
	FlagWait:                          "wait until the next calls can be made (at most 60 s)",
	FlagAnswer:                        "option id",
	FlagQuestion:                      "question id",
	FlagAll:                           "include forgotten and ignored artifacts",
	FlagProfile:                       "profile alias (default: the active profile)",
	FlagTitles:                        "include titles in --json output",
	FlagCapabilities:                  "sync it although it declares capabilities",
	FlagCreate:                        "create it on a profile although the title is taken there",
	FlagPublic:                        "publish to public copies (implies --shared)",
	FlagRehome:                        "stop tracking a copy in an organization the profile no longer uses",
	FlagRestore:                       "create it again on a profile where it was deleted",
	FlagResume:                        "sync a skipped or ignored artifact again",
	FlagRoundtrip:                     "allow publishing again after a page read back differently",
	FlagShared:                        "publish to copies shared with other people",
	FlagApply:                         "import (default: dry run; Terminal only)",
	FlagFrom:                          "absolute path of the old archive folder (required)",
	FlagMap:                           "<account-uuid>=<alias>; may be repeated",
	FlagRefused:                       "step ids whose call was declined, comma-separated",
	FlagKeepArtifacts:                 "keep the artifact archive (saved pages and their history)",
	FlagKeepBackups:                   "keep backup generations",
	FlagKeepState:                     "keep the state directory as well",
	FlagHookSessionStartPluginVersion: "version of the calling plugin (silent on version skew)",
	FlagPluginVersion:                 "version of the calling plugin",
	// Config validation (config.invalid.*).
	ConfigInvalid:                         "invalid config: %s",
	ConfigInvalidVersion:                  "version: must be %d",
	ConfigInvalidAppBundleID:              "app.bundle_id: not a bundle id",
	ConfigInvalidAppDataRoot:              "app.data_root: must be absolute or start with ~/",
	ConfigInvalidAppQuitTimeout:           "app.quit_timeout: must be between %s and %s",
	ConfigInvalidAppLateQuitWatch:         "app.late_quit_watch: must be between 0s and %s",
	ConfigInvalidAppOpenFileScan:          "app.open_file_scan: must be %s, %s or %s",
	ConfigInvalidDurationRange:            "%s: must be between %s and %s",
	ConfigInvalidOnlineReconcile:          "online.reconcile: must be %s, %s or %s",
	ConfigInvalidOnlineGuard:              "online.guard: must be %s or %s",
	ConfigInvalidArtifactsVerify:          "artifacts.verify: must be %s or %s",
	ConfigInvalidArtifactsHistoryKeep:     "artifacts.history_keep: must be between %d and %d",
	ConfigInvalidIntRange:                 "%s: must be between %d and %d",
	ConfigInvalidScratchMaxWorkspaceBytes: "scratch.max_workspace_bytes: must not be larger than scratch.max_run_bytes",
	ConfigInvalidScratchMaxDeleteFraction: "scratch.max_delete_fraction: must be greater than 0 and at most 1",
	ConfigInvalidScratchMaxDeleteMin:      "scratch.max_delete_min: must not be negative",
	ConfigInvalidScratchExclude:           "scratch.exclude[%d]: not a relative slash-separated pattern without ..",
	ConfigInvalidScratchCacheDirs:         "scratch.cache_dirs[%d]: must be one folder name",
	ConfigInvalidSize:                     "%s: %v",
	ConfigInvalidSizeRange:                "%s: must be between 1MiB and 1TiB",
	ConfigInvalidProfileAlias:             "profile[%d].alias: must match ^[a-z0-9][a-z0-9-]{0,31}$",
	ConfigInvalidProfileAliasTwice:        "profile[%d].alias: %s is used twice",
	ConfigInvalidProfileLabel:             "profile[%d].label: at most %d printable characters",
	ConfigInvalidProfileFolderDefault:     "profile[%d].folder: must differ from %s",
	ConfigInvalidProfileFolderPattern:     "profile[%d].folder: must match ^Claude-[A-Za-z0-9._-]{1,48}$ and not be a reserved name",
	ConfigInvalidProfileFolderUsed:        "profile[%d].folder: used by another profile (names compare without case)",
	ConfigInvalidSyncTombstones:           "sync.tombstones: must be %q or %q",
	ConfigInvalidSyncSecrets:              "sync.secrets: must be %q or %q",
	ConfigInvalidSyncExclude:              "sync.exclude[%d]: not a valid pattern",
	ConfigInvalidSyncAppPrefsUnknown:      "sync.app_prefs[%d]: unknown preference",
	ConfigInvalidSyncAppPrefsTagged:       "sync.app_prefs[%d]: preference tagged %q cannot be synced",
	ConfigInvalidSyncAppPrefsTwice:        "sync.app_prefs[%d]: listed twice",
	ConfigInvalidSyncSecretsAllow:         "sync.secrets_allow[%d]: empty or not printable",
	ConfigInvalidBoundaryAlias:            "%s: %q is not a profile alias",
	ConfigInvalidBoundaryCategory:         "%s.%s: %q is not a category",
	ConfigInvalidLimitsMaxDeleteFraction:  "limits.max_delete_fraction: must be greater than 0 and at most 1",
	ConfigInvalidLimitsMaxDeleteMin:       "limits.max_delete_min: must not be negative",
	ConfigInvalidBackupsKeep:              "backups.keep: must be at least 1",
	ConfigInvalidBackupsLeveldbClonesKeep: "backups.leveldb_clones_keep: must not be negative",
	ConfigInvalidBackupsMaxSize:           "backups.max_size: %v",
	ErrConfigNoConfig:                     "config: no config file; run paritydesk init",
	ErrIDsAlias:                           "ids: alias must match [a-z0-9][a-z0-9-]{0,31}",
	ErrIDsFolderPattern:                   "ids: folder must match ^Claude-[A-Za-z0-9._-]{1,48}$",
	ErrIDsFolderReserved:                  "ids: folder name is reserved",
	ErrStateNewerRoot:                     "state: root.json was written by a newer paritydesk; upgrade paritydesk before running it again",
	ErrStateNewerRootVersion:              "state: root.json was written by a newer paritydesk; upgrade paritydesk before running it again (found version %d, this binary supports %d)",
	ErrApplyHolderCheck:                   "holder check failed",
	ErrApplyLSLocked:                      "the folder is in use: another process holds the Local Storage lock",
	ErrApplyLSCompression:                 "Local Storage uses an unknown block compression",
	ErrApplyLSJournal:                     "Local Storage holds journal batches out of order",
	ErrApplyLSOpen:                        "opening Local Storage failed",
	ErrApplyReadFailed:                    "reading %s failed",
	ErrApplyRolledBackWritten:             "%s (files already written: %s)",
	ErrApplyRolledBackNone:                "%s (no files written yet)",
	ErrApplyFullSync:                      "full sync of the folder failed",
	ErrApplyChangedDuring:                 "%s changed during apply",
	ErrApplyModeFailed:                    "reading the mode of %s failed",
	ErrApplyWriteFailed:                   "writing %s failed",
	ErrApplyReadBackFailed:                "reading back %s failed",
	ErrApplyReadBackMismatch:              "read-back mismatch for %s",
	ErrApplyEncodeFailed:                  "encoding %s failed",
	ErrApplyLSWrite:                       "Local Storage write failed",
	ErrApplyLSClose:                       "closing Local Storage failed",
	ErrOpenFilesBlind:                     "paritydesk cannot read other processes' open files here (is it running inside a sandbox?); run it from Terminal",
	ErrOpenFilesSelfCheck:                 "the native open-file scan failed its self-check; set app.open_file_scan to \"auto\" or \"lsof\"",
	ErrOpenFilesNativeFailed:              "the native open-file scan failed",
	ErrOpenFilesPartial:                   "the native open-file scan could not read every process; set app.open_file_scan to \"auto\"",
	ErrAppQuitTimeout:                     "the app did not quit before the timeout",
	ErrAppQuitRefused:                     "the app refused to quit and did not quit before the timeout",
	ErrAppQuitNoReply:                     "the quit event got no reply in time and the app did not quit before the timeout",
	ErrQuitLate:                           "Claude quit late; relaunched %s",
	ErrQuitAsking:                         "Claude is still asking whether to quit; your answer there decides",
	ErrQuitStillRunning:                   "%s; it is still running and nothing was changed",
	ErrQuitFailed:                         "%s; nothing was changed",
	ErrQuitOSAStatus:                      " (AppleScript error %d)",
	ErrQuitRefused:                        "Claude refused to quit%s",
	ErrQuitTimedOut:                       "Claude did not answer the quit event in time%s; if macOS asked whether paritydesk may control Claude, allow it (paritydesk doctor --grant-automation)",
	ErrQuitSandboxed:                      "macOS refused to send the quit event to Claude%s, as it does for a sandboxed process; run paritydesk outside any sandbox, for example from Terminal",
	ErrQuitNotDelivered:                   "the quit event to Claude failed%s",
	ErrQuitNotQuit:                        "Claude did not quit%s",
	ErrApplyEnteringInUse:                 "the entering folder is in use; run paritydesk recover",
	ErrApplyEnteringNotRestored:           "the entering folder could not be fully restored; run paritydesk recover",
	ErrApplyLSLockedRecover:               "another process holds the Local Storage lock; quit Claude (and any copy started with --user-data-dir), then run paritydesk recover",
	ErrApplyLSReopen:                      "reopening Local Storage failed",
	DoctorModeReasonIdentity:              "default folder identity unreadable",
	DoctorModeReasonDowngraded:            "app version is older than the last one seen",
	DoctorModeReasonUntested:              "app version not tested",
	DoctorModeReasonRowsSkipped:           "some rows skipped",
	DoctorModeReasonNoManifest:            "no manifest covers the app version",
	DoctorModeReasonKnownBad:              "app version is known bad",
	DoctorModeReasonSQLite:                "Local Storage uses an unsupported backend",
	DoctorModeReasonCompression:           "Local Storage uses an unknown block compression",
	DoctorModeReasonNativeMulti:           "the app's own multi-account mode is in use",
	DoctorModeReasonCriticalProbe:         "critical probe failed: %s",
	ArtifactsPlanDue:                      "artifacts     %d to publish on %s (run /paritydesk:artifacts after the switch)",
}
