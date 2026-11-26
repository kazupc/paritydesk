# paritydesk

日本語: [README.ja.md](README.ja.md)

[![ci](https://github.com/kazupc/paritydesk/actions/workflows/ci.yml/badge.svg)](https://github.com/kazupc/paritydesk/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Use the Claude desktop app with several accounts on one Mac, and switch
between them with one command. Everything except the account stays the
same on every account: your Code sessions, environments, sidebar and
pins.

## What it does

- **Switch in one step.** `paritydesk switch` in Terminal, or
  `/paritydesk:switch` in a Code session. Claude quits, comes back on the
  other account, and reopens the session you were in.
- **Your Claude Code setup follows you.** Sessions, tasks, environments,
  the sidebar, pins, worktrees, scratch workspaces and window size are
  carried in both directions.
- **Newest change wins.** If something changed on both accounts, the
  newer change is kept; the older one stays in a backup.
- **Nothing is signed out.** Each account keeps its own data folder and
  sign-in; paritydesk swaps the folders.

**Status: pre-release, experimental.** No version is tagged yet. The
building blocks (quitting Claude, swapping folders, reopening a session)
were checked on a real Mac; the full switch and the newest features are
tested with synthetic data only. Keep a Time Machine backup if you try
it. Unofficial and not affiliated with Anthropic.

## Install

Requires macOS and Go 1.26.1 or newer (`brew install go`). A Homebrew tap
and release downloads come with the first release.

```
go install github.com/kazupc/paritydesk/cmd/paritydesk@latest
```

The binary lands in `$(go env GOPATH)/bin`; add that to your `PATH` if
needed. Details: [docs/install.md](docs/install.md).

**Claude Code plugin** (optional): adds `/paritydesk:switch`,
`/paritydesk:reconcile` and `/paritydesk:artifacts`. In a Code session:

```
/plugin marketplace add kazupc/paritydesk
/plugin install paritydesk@paritydesk
```

## Quick start

With Claude signed in to your first account:

```
paritydesk init --alias work        # name the account signed in now
paritydesk profile add personal     # Claude reopens empty: sign in to the second account
paritydesk recover                  # finish adding it
paritydesk plan                     # preview the next switch
paritydesk switch                   # switch; run again to switch back
```

`init` asks whether the account is managed by an employer or client, and
macOS asks once to let your terminal control Claude: allow it. If the
second account already lives in a folder such as `Claude-personal`, use
`paritydesk profile adopt personal Claude-personal` instead of
`profile add`. The first switch only combines both accounts and deletes
nothing.

In a Code session, `/paritydesk:switch` shows the plan and asks with
**Switch** and **Cancel** buttons. `paritydesk doctor` checks the setup;
[docs/commands.md](docs/commands.md) lists every command.

## What carries over

| Carried on every switch | In a Code session after the switch (`/paritydesk:reconcile`) | Not carried |
|---|---|---|
| Code sessions, backlog and scheduled tasks | Which sessions are pinned | Sign-in and credentials |
| Scratch workspaces | Sidebar sort and group-by | claude.ai chats and usage limits |
| Environments (secrets only between unmanaged accounts) | | Earlier thinking in a session |
| Sidebar groups, sections, pin order, collapsed groups | | Permission grants |
| Worktree registrations, window size, some app settings | | Claude in Chrome (one Chrome profile per account) |

MCP servers are carried if you turn on `sync.mcp_servers`. Artifacts
(beta): `/paritydesk:artifacts` saves them on this Mac. Settings:
[docs/configuration.md](docs/configuration.md).

## Safety

- **Never signs an account out** and never reads tokens, cookies or
  Keychain items ([PRIVACY.md](PRIVACY.md)).
- **No network.** No telemetry and no update checks; a test checks that
  the binary links no network code.
- **Quits Claude politely**, like choosing Quit, and waits for sessions
  that are still working. If Claude asks whether to quit, you decide.
- **Backups before every write.** `paritydesk recover` finishes or undoes
  an interrupted switch, and `paritydesk rollback` restores an earlier
  state.

## Good to know

- After a switch, continuing a session can show **"Claude can't use
  thinking from another organization"**. It is harmless: earlier thinking
  is tied to the account it was written on, so Claude rereads the
  session for its next reply, which may be slower and use more tokens.
- **Claude in Chrome** needs one Chrome profile per Claude account, each
  signed in to that account.
- **Do not switch accounts inside Claude itself**; use paritydesk.

More in [If something goes wrong](docs/if-something-goes-wrong.md).

## Documentation

- [Install](docs/install.md) · [Commands](docs/commands.md) · [Configuration](docs/configuration.md)
- [Integrations: SwiftBar, Raycast, shell](docs/integrations.md)
- [If something goes wrong](docs/if-something-goes-wrong.md)
- [How it works](docs/how-it-works.md) · [Architecture](docs/architecture.md)
- [Moving from an earlier folder-swap script](docs/migration-from-legacy.md)
- [Privacy](PRIVACY.md) · [Security](SECURITY.md) · [Changelog](CHANGELOG.md) · [Contributing](CONTRIBUTING.md)

## Licence

MIT; see [LICENSE](LICENSE). paritydesk is an independent project. It is
not affiliated with, endorsed by or sponsored by Anthropic. "Claude" is a
trademark of Anthropic. See [DISCLAIMER.md](DISCLAIMER.md).
