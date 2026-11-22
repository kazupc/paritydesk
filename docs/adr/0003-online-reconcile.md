# ADR 0003: Runtime spikes and the write path for each row

- Status: accepted (Week 0, first round)
- Date: 2026-11-22
- App versions: 6.6395.1 (first attempt), 6.28899.2 (all results below)

## Context

The design (D12, D26) says no capability claim goes into the README until a spike on the real app backs it. This ADR records the first round of spikes. They were run on one Mac with one real account, by a detached launchd job that quit the app with an Apple Event, wrote test values while the app was stopped, relaunched it and took read-only copies of Local Storage and `claude_desktop_config.json` at fixed times. Every write was preceded by a verified backup, and a final phase put the test values back.

## Results

| Spike | Question | Result | Evidence |
|---|---|---|---|
| S1 | Do values written while the app is stopped survive the next launch? | **Depends on the copy.** Sidebar sort (`dframe-code-sections`) written to Local Storage only was replaced within 20 s of launch by the value in the config mirror (or the server; both held the same value). Pin order (`dframe-local-slice`, `dframe-store.pinnedOrder`) and group order (`code-projects-order.<acct>`), written to Local Storage **and** the config mirror, stayed for the whole 10-minute window. No server overwrite was seen. | Snapshots at launch +20, +60, +180, +600 s |
| S3 | Does a launchd job started from a Code session survive the app quitting, and can it quit the app? | **Yes.** A `gui/<uid>` job ran through three quit and relaunch cycles. `tell application id … to quit` returned 0 and the app was gone after about 7 s, with no Automation prompt. | Runner log |
| S4 | Is a change made in the app on disk when the app quits? | **The config mirror is written at once; Local Storage later.** `set_view` wrote `claude_desktop_config.json` within a second; Local Storage still held the old value 4 s later. After an unclean exit (SIGTERM), the next launch rebuilt the Local Storage value from the config mirror. Flush-on-quit itself was not isolated. | Snapshots, `Config file written` in main.log |
| S10 | Which `claude://` link reopens a given session? | **`claude://claude.ai/epitaxy/<local session id>`**, warm and cold (3 of 3), confirmed by `setFocusedSession` in main.log. `claude://code/continue?session=…` did nothing. On its own the app focuses no session after launch. | main.log |
| S17 | Does the app keep an extended attribute on its data folder? | **Yes**, across two restarts. | `xattr -p` after each phase |

## Findings that were not spikes

1. **Quit is vetoed while Claude is working.** If any session is mid-turn, the app answers the quit Apple Event with a dialog: "Claude is still working" — *Quit anyway*, *Wait for Claude* (default), *Cancel* — and logs `beforeQuit: vetoed by before-quit interceptor`. A message queued in the invoking session starts a new turn as soon as the current one ends, so "the turn ended" is not enough.
2. **SIGTERM is not a clean quit.** The next launch logged `Previous run did not exit cleanly`. The legacy script's SIGTERM fallback is removed; paritydesk never sends signals to the app.
3. **Chrome's native messaging host lives in the app bundle but belongs to Chrome.** `Contents/Helpers/chrome-native-host` processes are children of Chrome and outlive the app. Any "is the app still running" check that matches the bundle path must exclude them, along with `ShipIt` and `chrome_crashpad_handler`.
4. **A staged update installs on quit.** ShipIt waits for the app to exit and then swaps the bundle. A switch that quits the app while an update is staged relaunches a different version. Preflight must detect this and either refuse or wait for ShipIt to finish.
5. **Claude in Chrome is bound to one account at a time.** The app connects to `wss://bridge.claudeusercontent.com/chrome/<account uuid>`, and the extension connects to the bridge of the account it is signed in to. After a switch, the extension connects only if it is signed in to the account now in the app. Folder swapping does not affect this, and paritydesk cannot fix it without touching credentials.

## Decisions

- **Rows 9 to 11 (sidebar):** every key that the config mirrors under `preferences.epitaxyPrefs` is written to Local Storage and the config mirror together, then verified after launch. Section settings stay ON (`set_view`, `create_section`) by default; OFF writes to both copies are allowed with verification. `collapsedGroups` was not tested and stays not carried.
- **Quit (D14):** Apple Event only. Before quitting, wait until no session is working (no running turn, no queued message), then quit. If the app vetoes, wait for the user's answer up to a limit, never force.
- **Quiescence (D15):** the app's process set excludes Chrome's native host, ShipIt and crashpad. Writes still require no open file under `Local Storage/` or on the config file.
- **Preflight:** refuse to switch while a ShipIt update is staged, unless `--allow-update` is given, in which case wait for the install and re-run the shape probes on the new version.
- **Relaunch (P11):** reopen the invoking session with `claude://claude.ai/epitaxy/<id>`, rebuilt from a validated id (`^local_[0-9a-f-]{36}$`), and confirm it with `setFocusedSession` in main.log.
- **Identity marker (S17):** the xattr marker is used.
- **Claude in Chrome (G1):** paritydesk cannot keep one extension signed in to two accounts. The proposed setup is **one Chrome profile per Claude account**, each with Claude in Chrome signed in to that account and kept open. This is not yet tested (spike S18). `doctor` reports whether an extension connected to the active account's bridge after the last launch (`Chrome extension connected to bridge` in main.log) and explains the setup when it did not.

## Not yet run

S2, S5, S6, S7, S13, S14, S15 and S16. S1 for `collapsedGroups`, and the question of whether the server or the config mirror wins when they differ, need a second round.
