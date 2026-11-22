# paritydesk

Switch the Claude desktop app between accounts on one Mac, and keep everything except the account in step.

> **Status: pre-alpha.** The command line, the plugin and the safety machinery are being implemented. Do not run anything from this repository against a real Claude data folder yet. paritydesk is unofficial and not affiliated with Anthropic; see [DISCLAIMER.md](DISCLAIMER.md).

## How it works

The Claude desktop app keeps everything for the signed-in account in one data folder. paritydesk keeps one folder per account: the active one at the app's default location, the others parked next to it as `Claude-<alias>`. A switch quits Claude, carries your shared state into the folder of the account you are switching to, swaps the two folders, and relaunches Claude. No account is ever asked to sign in again, and paritydesk never touches credentials.

Every write is preceded by a numbered backup generation, every irreversible step is journaled, and an interrupted switch can be finished or undone with `paritydesk recover`.

## What has been confirmed on a real Mac

paritydesk claims a capability only after a runtime spike on the real app backs it. The results are recorded in [ADR 0003](docs/adr/0003-online-reconcile.md):

- **Quitting is clean and polite.** paritydesk quits Claude with an Apple Event, never with a signal. When a Code session is still working, Claude asks you whether to quit, and your answer decides; paritydesk waits for it and never forces a quit (S3, findings 1 and 2).
- **Switching from inside a Code session works.** A background launchd job started from a Code session survives Claude quitting, and quits and relaunches Claude without an extra permission prompt (S3).
- **Sidebar order survives a relaunch** when it is written to both of the places Claude keeps it, which is how paritydesk writes it. Values Claude later replaces are reported, not fought over (S1, S4).
- **The session you switched from can be reopened** on the other account with Claude's own session link (S10).
- **Folders keep their identity marker** across restarts, so paritydesk can tell which account a folder belongs to (S17).
- **Claude in Chrome keeps working on every account, provided you use one Chrome profile per Claude account**, each with Claude in Chrome signed in to that account and kept open. The extension follows the Chrome profile's sign-in, not the Claude data folder, so paritydesk cannot move it between accounts (S18, finding 5).

## Scope of v0.1 (in development, not yet a claim)

v0.1 is designed to carry Code sessions, backlog and scheduled tasks, environments, the sidebar's group order, pin order and view, selected app preferences, window size, worktree registrations and, if you opt in, MCP servers, in both directions with the newest change winning. Each item becomes a claim here only once the release checklist has confirmed it on a real Mac. See the [CHANGELOG](CHANGELOG.md) for what is deferred to v0.2.

A staged Claude update installs when Claude quits (finding 4 of ADR 0003), so v0.1 is designed to look for one before quitting and stop (exit 13) unless you pass `--allow-update`. That check has not yet been run against the real app (release checklist, step 20).

## What it will never do

- Read, copy or move tokens, browser cookie stores or Keychain items.
- Open a network connection of any kind.
- Patch the app, or pool usage limits across accounts.
- Ask any account to give up its sign-in. Every account stays signed in.

## Commands

```
paritydesk init                         set up for the account in Claude now
paritydesk profile add <alias>          add another account
paritydesk status                       active profile and anything pending
paritydesk plan [<alias>]               preview a switch (approximate)
paritydesk switch [<alias>|-]           switch ("-" is the previous profile)
paritydesk recover                      finish or undo an interrupted run
paritydesk rollback [<n>]               restore a backup generation
paritydesk doctor                       check the installation
```

Every command accepts `--json` (one JSON document with `"v":1`), `--plain`, `--no-color`, `--ascii` and `--read-only`. Exit codes and what to do next are listed in [docs/if-something-goes-wrong.md](docs/if-something-goes-wrong.md).

Inside a Claude Code session, the plugin's `/paritydesk:switch` command shows you the plan, asks you to confirm it, and hands the switch to a background job.

## Build from source

```
go install ./cmd/paritydesk
```

This puts `paritydesk` in `$(go env GOPATH)/bin` (usually `~/go/bin`); make sure that directory is on your `PATH`. paritydesk needs macOS and Go 1.26. Releases, a Homebrew tap and shell integrations follow later in v0.1.

## Install the Claude Code plugin

The plugin adds the `/paritydesk:switch` command and a SessionStart hook. From a clone of this repository, in Claude Code:

```
/plugin marketplace add /path/to/paritydesk
/plugin install paritydesk@paritydesk
```

Run `paritydesk init` once before using it. The SessionStart hook finds the binary through the path that `paritydesk init` records, but `/paritydesk:switch` runs `paritydesk` from the `PATH` of Claude Code's shell, so the binary must be on that `PATH` too (and it should be the same binary).

## Configuration

`paritydesk config path` prints where the config file is, `paritydesk config show` prints it with every default filled in, and `paritydesk config validate` checks it.

`sync.exclude` keeps Code sessions on the account where they are, so they are never copied to another account. Each entry is either a session id (`local_<uuid>`) or a glob over the session's project path: `*` and `?` match within one path segment, an entry ending in `/**` matches that directory and everything below it, and a leading `~/` is your home directory. While any glob is set, a session whose project path cannot be read is treated as excluded.

```toml
[sync]
exclude = ["~/clients/**", "local_00000000-0000-4000-8000-000000000000"]
```

## Documentation

- [Architecture and the ids cited in the code](docs/architecture.md)
- [Privacy: every path paritydesk reads and writes](PRIVACY.md)
- [Security policy](SECURITY.md)
- [Contributing](CONTRIBUTING.md)

## License

MIT. See [LICENSE](LICENSE).

paritydesk is an independent project. It is not affiliated with, endorsed by or sponsored by Anthropic. "Claude" is a trademark of Anthropic.
