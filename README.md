# Music-Contest-Telegram

## Purpose

This is a Telegram bot that runs a weekly music contest inside a group chat.
Each week gets a random theme; participants privately send the bot a YouTube
song matching it, and once everyone's in, the bot publishes all the songs to
the group anonymously and shuffled. Participants then privately rank the
other songs (strict order, no ties) and say which ones they already knew
before the contest — a song known by too many people beforehand is
disqualified from scoring. Once everyone's done and the phase's deadline has
passed, the bot posts the de-anonymized results — points per song and the
familiarity outcome. (Cumulative season standings across weeks are on the
roadmap but not built yet.)

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

## Usage

All of this happens in two places: admin commands typed in the group chat,
and the bot's own private messages to each participant for submissions,
ranking, and the familiarity questionnaire. The state machine behind it is
diagrammed in [`docs/flows.md`](docs/flows.md).

### 1. Start a contest

```
/startcontest Summer 2026
```

Enrolls every current group member as a participant and activates a new
contest — only one contest is active at a time, and starting a new one
deactivates the previous (its history stays intact, just no longer active).
Strikes for missed deadlines are tracked per contest, so a new contest
always starts clean.

Optionally, set the contest's own songs-phase and results-phase deadline
lengths, in days (both or neither):

```
/startcontest Summer 2026 4 4
```

Omitting them uses the default (4 days each). The count is inclusive of
both the day a phase starts and its deadline day: with the default 4, a
phase that starts on Monday has its deadline on Thursday (Monday = day 1,
Thursday = day 4). Since the results phase starts the moment the songs
phase closes, the two phases share that boundary day — a week that starts
Monday runs songs Monday–Thursday, then results Thursday–Sunday.

### 2. Start a week

```
/startweek
```

Picks a topic from the contest's pool (today, that pool starts with a
single seeded topic — dedicated topic-management commands are still on the
roadmap) and announces it in the group, opening the **songs phase**.

**Sending a song is not a command.** Each participant just opens a private
chat with the bot and pastes a plain YouTube link
(`https://youtube.com/watch?v=...` or `https://youtu.be/...`) — no command
needed. The bot only checks the URL's *format*; it never calls the YouTube
API to confirm the video exists. Sending another link before the deadline
replaces the previous one. The bot publishes every submission at once,
anonymized and shuffled, once *every* participant has submitted *and* the
phase's deadline has passed — whichever happens last.

### 3. Vote and answer the questionnaire

Once songs are published, the bot opens the **results phase** — and this
step is bot-initiated too: the moment the phase opens, the bot starts a
private conversation with each participant automatically.

1. **Familiarity questionnaire.** The bot sends one song at a time —
   *"Song N: `<url>` — Did you already know this song before it was
   submitted?"* — with `Yes`/`No` buttons. It repeats until every song
   except the participant's own has been answered. A song known beforehand
   by 3 or more participants is disqualified and scores zero.
2. **Ranking.** Once the questionnaire's done, the bot sends *"Pick your
   next favorite among the remaining songs:"* with one button per song
   still unranked. Tapping a button records it as the next favorite and
   removes it from the list; the bot resends the shrunk list and repeats
   until none are left. Picking exactly one button at a time is what makes
   ties impossible — there's no way to select two songs at once.

Once both are done for a participant, the bot confirms with *"Thanks! Your
ranking and questionnaire are complete."* and waits for everyone else.
Once everyone's done and this phase's deadline has passed, the bot
publishes the de-anonymized results — points per song, and the
familiarity outcome — in the group.

### 4. Recovering from a stuck week

- `/modifylimit <days>` — override the *current* phase's deadline for this
  week only, e.g. `/modifylimit 2` to shorten it.
- `/forceadvance` — close the current phase immediately regardless of who's
  finished; anyone still missing accrues a strike.
- `/fixsubmission @user <url>` / `/removesubmission @user` — correct or
  delete a participant's song during the songs phase.
- `/syncparticipants` — reconcile the roster against the group's current
  member list, e.g. after the bot missed a join/leave event.

### 5. Ending a contest

```
/finishcontest
```

Deactivates the current contest — only allowed once its week is back to
idle.

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
