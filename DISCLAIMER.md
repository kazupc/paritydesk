# Disclaimer

paritydesk is an **unofficial**, independent open-source tool. It is not
affiliated with, endorsed by, sponsored by or supported by Anthropic PBC.
"Claude" is a trademark of Anthropic. Problems with paritydesk belong in
this project's issue tracker, never in Anthropic's support channels.

## It may break

paritydesk works with files that the Claude desktop app keeps for itself.
Their layout is not a public interface and can change with any app
update. paritydesk checks the shapes it relies on before every write and
refuses, or falls back to swapping folders without syncing, when they
differ from what it was tested with. Even so, an app update can make it
stop working until a new compatibility manifest ships.

## Your data

paritydesk takes a backup generation before every write, journals every
irreversible step, and never deletes a data folder. It is still young
software that edits application data: keep your own backups (for example
Time Machine), and keep Claude quit while you restore anything by hand.
The software is provided "as is", without warranty of any kind; see
[LICENSE](LICENSE).

## Accounts

- Use paritydesk only with accounts you own or are allowed to use, each
  under its own plan. paritydesk does not combine or share usage limits
  between accounts, and it is not a way around them.
- Accounts managed by an employer or a client may be subject to their
  policies. Mark such profiles with `--managed`; paritydesk then keeps
  secrets (environment variable values, MCP server environments) from
  crossing between a managed and a personal account unless you allow it
  per key.
- paritydesk never touches credentials, and every account stays signed in.
  If the app ever asks for a sign-in after a switch, sign in with that
  profile's own account only.

## Claude in Chrome

Claude in Chrome connects to the account its Chrome profile is signed in
to, not to the Claude data folder. paritydesk cannot change that. The
supported setup is **one Chrome profile per Claude account**, each with
Claude in Chrome signed in to that account and kept open. With a single
Chrome profile, Claude in Chrome works only on the account that profile
is signed in to.
