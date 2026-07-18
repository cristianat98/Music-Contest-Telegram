# Music-Contest-Telegram

## Purpose

This is a Telegram bot that runs a weekly music contest inside a group chat.
Each week gets a random theme; participants privately send the bot a YouTube
song matching it, and once everyone's in, the bot publishes all the songs to
the group anonymously and shuffled. Participants then privately rank the
other songs (strict order, no ties) and say which ones they already knew
before the contest — a song known by too many people beforehand is
disqualified from scoring. Once everyone's done and the phase's deadline has
passed, the bot posts the de-anonymized results and running season
standings.

It's a single Go binary with no external dependencies beyond the Telegram
Bot API and a local SQLite file, designed to run unattended as a systemd
service — the reference deployment target is a Raspberry Pi Zero W.

## Installation

**Get a binary:**

- Download a release from [GitHub Releases](../../releases) (`linux/amd64`
  or `linux/arm64` tarballs, published automatically by
  [GoReleaser](https://goreleaser.com) on every tag), or
- Build from source: `go build -o bin/musiccontestbot ./cmd/musiccontestbot`,
  or `make build-pi` for the Raspberry Pi Zero W target (32-bit ARMv6,
  `CGO_ENABLED=0`).

**Create a Telegram bot:** talk to
[@BotFather](https://t.me/BotFather) to get a bot token, add the bot to your
group, and make it a group **admin** (required so it reliably receives
member join/leave events). Find the group's chat ID (e.g. by temporarily
adding [@getidsbot](https://t.me/getidsbot) or checking the bot's own
`getUpdates` response).

**Configure and run:** the bot reads its configuration from three required
environment variables:

| Variable | Description |
|---|---|
| `BOT_TOKEN` | Telegram bot token from BotFather |
| `CHAT_ID` | Numeric ID of the group chat to run the contest in |
| `DB_PATH` | Path to the SQLite database file (created on first run) |

```bash
BOT_TOKEN=... CHAT_ID=... DB_PATH=./musiccontestbot.db ./musiccontestbot
```

For a persistent deployment, use the provided
[`deploy/musiccontestbot.service`](deploy/musiccontestbot.service) systemd
unit as a starting point — it expects the binary at
`/opt/musiccontestbot/musiccontestbot-pi` and the environment variables in
`/etc/musiccontestbot/musiccontestbot.env`, both adjustable to your setup.

## Development setup

### Requirements

- [Go](https://go.dev) — the version pinned in [`go.mod`](go.mod) (currently
  1.25).
- Python 3 + `pip`, only to install `pre-commit`.
- The SQLite driver ([`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite))
  is pure Go — no `cgo` toolchain or system SQLite library needed to build,
  test, or run.

### Pre-commit checks

This repo uses [pre-commit](https://pre-commit.com) to run the same checks
locally and in CI, configured in
[`.pre-commit-config.yaml`](.pre-commit-config.yaml):

- **Basic hygiene** — trailing whitespace, missing final newline, valid
  YAML, no accidentally-committed large files, no leftover merge-conflict
  markers.
- **Go correctness** — `gofmt`, `go vet`, and `go build` must all pass.
- **[`golangci-lint`](https://golangci-lint.run)** — runs the linters
  configured in [`.golangci.yml`](.golangci.yml) (`errcheck`, `govet`,
  `ineffassign`, `staticcheck`, `unused`). Production code must handle every
  returned error; test files are exempt from `errcheck` since seed/assertion
  helpers routinely ignore it for brevity.

Install and enable the hooks once:

```bash
pip install pre-commit
pre-commit install
```

From then on, `git commit` runs every hook automatically; `pre-commit run
--all-files` runs them all on demand. The first `golangci-lint` run may take
a moment while pre-commit fetches the pinned linter version.

### Adding a new feature

1. Branch off `develop` (not `master`) for the new work.
2. Commit using [Conventional Commits](https://www.conventionalcommits.org/)
   (`feat:`, `fix:`, `refactor:`, `docs:`, etc.) — the type matters, since it
   drives the automatic version bump described below.
3. Open a pull request **into `develop`**. Every PR runs
   [`.github/workflows/check-code.yml`](.github/workflows/check-code.yml) in
   CI: the same `pre-commit` hooks, the full test suite with coverage, and a
   SonarCloud quality-gate check — all must pass before merging.
4. `develop` is periodically merged into `master`. That merge is what
   triggers the release: a `feat:` commit bumps the minor version, `fix:`
   bumps patch, and a `BREAKING CHANGE:` footer (or `!` after the type) bumps
   major — computed automatically from the commits since the last tag, no
   manual tagging needed. Each new tag then triggers
   [GoReleaser](https://goreleaser.com) to publish a GitHub Release with
   `linux/amd64` and `linux/arm64` binaries attached (see
   [`.goreleaser.yaml`](.goreleaser.yaml)).
