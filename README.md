# paritydesk

Switch the Claude desktop app between accounts on one Mac, and keep everything except the account in step.

> **Status: pre-alpha.** Nothing here is usable yet. The design is done and the first runtime experiments are next. Do not run anything from this repository against a real Claude data folder.

## What it will do

- Switch the signed-in account with one command, slash command or click, without signing out of any account.
- Carry sessions, environments, sidebar layout and app preferences between accounts in both directions. The newest change wins.
- Keep Claude in Chrome working on every account.
- Stop safely at any point: a killed switch leaves a complete folder in place, and `recover` and `rollback` repair or undo it.

## What it will never do

- Read, copy or move tokens, cookies or Keychain items.
- Make network calls to Claude or Anthropic.
- Patch the app, or pool usage limits across accounts.

## License

MIT. See [LICENSE](LICENSE).

paritydesk is an independent project. It is not affiliated with, endorsed by or sponsored by Anthropic. "Claude" is a trademark of Anthropic.
