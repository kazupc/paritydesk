# Installing paritydesk

paritydesk runs on macOS only. There are three ways to install it. All
three are designed to give you the same binary. [Release
checklist](release-checklist.md) steps 31 to 35 run each of them on a
clean Mac, and this page changes if they find a problem.

paritydesk is unofficial and not affiliated with Anthropic; see
[DISCLAIMER.md](../DISCLAIMER.md). After installing, run `paritydesk init`
once. It asks macOS to let your terminal control Claude (Automation), so
that paritydesk can quit Claude; allow it. `paritydesk doctor
--grant-automation` asks again later.

## Why paritydesk is not notarized

macOS Gatekeeper checks programs that were downloaded from the internet.
A program passes without a prompt when it is signed with an Apple
Developer ID and notarized by Apple. paritydesk has no Developer ID, so
its release binaries are not notarized. The Go linker ad-hoc signs Apple
silicon binaries, which lets them run, but an ad-hoc signature does not
satisfy Gatekeeper for a quarantined download.

The install paths below are chosen so that you do not need to weaken
Gatekeeper:

- The Homebrew tap builds paritydesk from source on your Mac. A binary
  you build yourself carries no quarantine mark, so Gatekeeper does not
  stop it.
- `go install` also builds from source.
- A release tarball that a browser downloads is quarantined. The tarball
  section explains how to check it and how to clear the mark on that one
  file only.

**Never disable Gatekeeper globally** (for example with `spctl` or by
allowing apps from anywhere) to run paritydesk. No install path needs it.

## 1. Homebrew tap, built from source (recommended)

```
brew install kazupc/tap/paritydesk
```

This taps `kazupc/tap` and installs the `paritydesk` formula, which
downloads the tagged source, checks its sha256, and builds it with Go
(`depends_on "go" => :build`, so Homebrew installs Go for the build if
it is missing). It also installs completions for bash, zsh and fish.

If your Homebrew version asks you to trust a third-party tap before it
installs from it, read the formula first (`brew cat
kazupc/tap/paritydesk`), then follow Homebrew's own prompt.

To check that the installed binary is not quarantined:

```
xattr -l "$(brew --prefix)/bin/paritydesk"
```

Expected: no `com.apple.quarantine` line (release checklist step 33).

Update with `brew upgrade paritydesk`. `brew install --HEAD
kazupc/tap/paritydesk` builds the newest commit on `main` instead of a
release.

## 2. go install

With Go 1.26.1 or newer (1.26.0 builds it, but has standard library
vulnerabilities that `govulncheck` reports):

```
go install github.com/kazupc/paritydesk/cmd/paritydesk@latest
```

This puts `paritydesk` in `$(go env GOPATH)/bin` (usually `~/go/bin`).
Make sure that directory is on your `PATH`. `paritydesk version` then
prints the module version. Until a version is tagged, `@latest` builds
the newest commit on `main`.

From a clone of the repository, `go install ./cmd/paritydesk` does the
same.

## 3. Release tarball

Each release on [GitHub](https://github.com/kazupc/paritydesk/releases) has these files:

- `paritydesk_<version>_darwin_arm64.tar.gz` (Apple silicon)
- `paritydesk_<version>_darwin_amd64.tar.gz` (Intel)
- `paritydesk_<version>_darwin_universal.tar.gz` (both)
- `paritydesk_<version>_source.tar.gz`
- `SHA256SUMS`

Download the tarball for your Mac and `SHA256SUMS` into one empty
directory, then check the checksum before you open anything:

```
cd <that directory>
shasum -a 256 -c SHA256SUMS --ignore-missing
```

Expected: `paritydesk_<version>_darwin_<arch>.tar.gz: OK`. If it says
`FAILED`, delete the download and do not run it. The checksum file comes
from the same release page, so it protects against a damaged download,
not against a compromised release; the Homebrew tap path builds from the
tagged source instead.

Then unpack it and look at the binary:

```
tar -xzf paritydesk_<version>_darwin_<arch>.tar.gz
xattr -l paritydesk
```

A file downloaded with `curl` carries no quarantine mark. A file a
browser downloaded shows `com.apple.quarantine`, and macOS refuses to run
it because it is not notarized. After the checksum matched, you can
remove the mark from this one file only:

```
xattr -d com.apple.quarantine paritydesk
```

Then move it to a directory on your `PATH`, for example
`/usr/local/bin` (or `/opt/homebrew/bin` on Apple silicon if you use
Homebrew there). The plugin's hook and the shell integrations look for
the binary at the path `paritydesk init` records, so run `paritydesk
init` (or `paritydesk doctor`) once after moving it.

## Claude Code plugin

The plugin adds three commands, `/paritydesk:switch`,
`/paritydesk:reconcile` and `/paritydesk:artifacts`, and three hooks. In
Claude Code (the Code tab of the desktop app or the `claude` command
line):

```
/plugin marketplace add kazupc/paritydesk
/plugin install paritydesk@paritydesk
```

Where `/plugin` is not available, the same works from a terminal:

```
claude plugin marketplace add kazupc/paritydesk
claude plugin install paritydesk@paritydesk
```

To try a change to the plugin from a clone, add the clone's path instead
of `kazupc/paritydesk`.

Run `paritydesk init` once before using it.

- The hooks find the binary through the path that `paritydesk init`
  records (or the Homebrew locations), never through `PATH`. If the
  binary cannot be found while changes are being applied, the PreToolUse
  hook blocks those tool calls.
- The commands run `paritydesk` from the `PATH` of Claude Code's shell,
  so the binary must be on that `PATH` too, and it should be the same
  binary. With `go install`, that means `~/go/bin` (or
  `$(go env GOPATH)/bin`) must be on the `PATH` Claude Code sees.
- A SessionStart hook prints one line when something is waiting (a
  recover, a sign-in, changes to apply). PreToolUse and PostToolUse hooks
  run on Claude's sidebar, session, settings, directory and artifact
  tools. They act only while `/paritydesk:switch` captures, or while
  `/paritydesk:reconcile` or `/paritydesk:artifacts` runs. With
  `online.guard = "always"`, the PreToolUse hook also blocks its fixed
  list of tools in every session. While queued changes are being
  applied, it lets only the listed calls (and a short read list) run, and
  it never approves anything for you. [PRIVACY.md](../PRIVACY.md) lists what the
  hooks read and record.

## Shell completion

The Homebrew formula installs completions for you. Otherwise:

```
# bash (~/.bashrc)
source <(paritydesk completion bash)
# zsh (~/.zshrc, after compinit)
source <(paritydesk completion zsh)
# fish
paritydesk completion fish > ~/.config/fish/completions/paritydesk.fish
```

Profile aliases are completed from your config file.

## Uninstall

`paritydesk uninstall` removes paritydesk's own state and keeps every
Claude data folder; then remove the binary (`brew uninstall paritydesk`,
or delete the file).
