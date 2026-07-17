# Architecture

Single Go binary (`cmd/musiccontestbot`); all logic lives under `internal/`
since nothing here is consumed as an importable library (KTD1).

```mermaid
classDiagram
    class main {
        +main()
        +runTickLoop(ctx, app)
        +runTick(ctx, app)
    }

    class config_Config {
        +BotToken string
        +ChatID int64
        +DBPath string
    }
    class config {
        +Load() Config, error
    }

    class storage {
        +Open(path string) sql.DB, error
        +Checkpoint(db) error
    }

    class bot_App {
        +TG *tgbot.Bot
        +DB *sql.DB
        +ChatID int64
        +SelfID int64
        +Contest *contest.Engine
        +New(ctx, token, chatID, db) App, error
        +Start(ctx)
        +SendGroupMessage(ctx, text) error
        +SendPrivateMessage(ctx, telegramUserID, text) error
        +CheckAdminStatus(ctx) error
        +ProcessResultsPrompts(ctx) error
        +ProcessResultsNotifications(ctx) error
    }
    class AdminOnly {
        <<middleware>>
        +AdminOnly(app, next) HandlerFunc
    }

    class contest_Engine {
        -db *sql.DB
        -mu sync.Mutex
        -songsHooks SongsCollectionHooks
        -resultsHooks ResultsCollectionHooks
        +StartContest(ctx, name) string, error
        +FinishContest(ctx) string, error
        +StartWeek(ctx) string, error
        +ModifyLimit(ctx, days) string, error
        +ForceAdvance(ctx) string, error
        +Tick(ctx) error
        +CurrentWeek(ctx) WeekInfo, error
        +SetSongsHooks(h)
        +SetResultsHooks(h)
    }
    class contest_participants_go {
        <<file: participants.go>>
        +StrikesForParticipant(ctx, db, contestID, participantID) int, error
        +SetParticipantLeft(ctx, db, telegramUserID) error
        -enrollContestParticipants(ctx, tx, contestID) error
    }
    class contest_topics_go {
        <<file: topics.go>>
        -associateDefaultTopic(ctx, tx, contestID) error
        -pickTopic(ctx, tx, contestID) int64, string, error
    }
    class SongsCollectionHooks {
        <<interface>>
        +CloseSongsCollection(ctx, tx, weekID, forced) error
        +SongsCollectionComplete(ctx, db, weekID) bool, error
    }
    class ResultsCollectionHooks {
        <<interface>>
        +OpenResultsCollection(ctx, tx, weekID) error
        +CloseResultsCollection(ctx, tx, weekID, forced) error
        +ResultsCollectionComplete(ctx, db, weekID) bool, error
    }
    class SongsHooks {
        +CloseSongsCollection(...)
        +SongsCollectionComplete(...)
    }
    class ResultsHooks {
        +OpenResultsCollection(...)
        +CloseResultsCollection(...)
        +ResultsCollectionComplete(...)
    }
    class GroupNotifier {
        <<interface>>
        +SendGroupMessage(ctx, text) error
    }
    class ResultsNotifier {
        <<interface>>
        +SendPrivateMessage(ctx, telegramUserID, text) error
    }

    main --> config : Load()
    main --> storage : Open(), Checkpoint()
    main --> bot_App : New(), Start()
    main --> contest_Engine : Tick()

    bot_App --> AdminOnly : wraps admin commands
    bot_App --> contest_Engine : owns
    bot_App ..|> GroupNotifier : implements
    bot_App ..|> ResultsNotifier : implements

    contest_Engine --> SongsCollectionHooks : delegates songs_collection close
    contest_Engine --> ResultsCollectionHooks : delegates results_collection open/close
    contest_Engine --> contest_participants_go : enrolls participants at StartContest
    contest_Engine --> contest_topics_go : picks/associates topics at StartContest/StartWeek
    SongsHooks ..|> SongsCollectionHooks : implements
    ResultsHooks ..|> ResultsCollectionHooks : implements
    SongsHooks --> GroupNotifier : publish_songs outbox
    ResultsHooks --> ResultsNotifier : publish_results / partial-notice outbox
    bot_App --> contest_participants_go : SetParticipantLeft on Telegram leave (chatmember.go)
```

## Data model

`contest_participants` replaces the old `week_participants` roster: enrollment
is per-contest, snapshotted once at `/startcontest`, not re-snapshotted per
week. Strikes are never stored -- `StrikesForParticipant` derives them from
`weeks`/`submissions`/`votes`/`quiz_answers` against each participant's
obligation window. Topic eligibility (`topic_usage.selectable`) is the direct,
renamed successor to the old `used` column -- still a stored per-association
flag, not derived, since it needs to flip and repeat-fallback rather than only
ever grow. `votes` stores only `rank`, not points: a vote's points are a pure
function of its rank and its voter's required-ranking count, so
`submissionPoints` derives them at read time instead of storing the same fact
twice; disqualification (a song known by 3+ beforehand) is applied the same
way, by zeroing a submission's points in `FinalResults` rather than mutating
`votes` when results close.

```mermaid
erDiagram
    CONTESTS ||--o{ CONTEST_PARTICIPANTS : enrolls
    PARTICIPANTS ||--o{ CONTEST_PARTICIPANTS : "enrolled in"
    CONTESTS ||--o{ WEEKS : has
    CONTESTS ||--o{ TOPIC_USAGE : "draws from"
    TOPICS ||--o{ TOPIC_USAGE : "assigned to"
    WEEKS ||--o| TOPICS : uses
    WEEKS ||--o{ SUBMISSIONS : collects
    PARTICIPANTS ||--o{ SUBMISSIONS : submits
    WEEKS ||--o{ VOTES : records
    PARTICIPANTS ||--o{ VOTES : casts
    SUBMISSIONS ||--o{ VOTES : "ranked by"
    WEEKS ||--o{ QUIZ_ANSWERS : records
    PARTICIPANTS ||--o{ QUIZ_ANSWERS : answers
    SUBMISSIONS ||--o{ QUIZ_ANSWERS : "asked about"

    CONTEST_PARTICIPANTS {
        int contest_id
        int participant_id
        datetime left_at
    }
    TOPIC_USAGE {
        int contest_id
        int topic_id
        bool selectable
    }
    SUBMISSIONS {
        int id
        int week_id
        int participant_id
        string url
        int display_name
    }
    VOTES {
        int id
        int week_id
        int voter_id
        int submission_id
        int rank
    }
    QUIZ_ANSWERS {
        int id
        int week_id
        int participant_id
        int submission_id
        bool already_knew
    }
```

## Module boundaries

- **`internal/config`** -- environment-variable loading only, no behavior.
- **`internal/storage`** -- SQLite connection (WAL, `SetMaxOpenConns(1)`,
  KTD2) and embedded goose migrations (KTD3). Owns the schema; no business
  logic.
- **`internal/contest`** -- the contest/week state machine (`Engine`),
  deadline math (`Deadline`), and the durable-outbox-backed song
  submission/publication (`SongsHooks`) and voting/questionnaire/results
  (`ResultsHooks`) logic. Depends only on `database/sql` -- never on
  `go-telegram/bot` -- so it stays testable without a Telegram client and
  reusable if the bot's transport ever changed.
- **`internal/bot`** -- all Telegram I/O: command routing, admin
  authorization, chat-member roster sync, and the interactive
  submission/questionnaire/ranking flows. Implements the small notifier
  interfaces (`contest.GroupNotifier`, `contest.ResultsNotifier`) that
  `internal/contest` defines, so the dependency points from `bot` to
  `contest`, never the other way.
- **`cmd/musiccontestbot`** -- composition root: wires config, storage, the
  bot, and the tick loop; owns graceful shutdown (R31).

## Why hooks instead of one combined interface

`SongsCollectionHooks` and `ResultsCollectionHooks` are deliberately
separate interfaces (not one `LifecycleHooks` covering all four methods).
Songs-side and results-side logic are independent slices of work with no
shared state; forcing one object to implement both would couple them for
no reason. `Engine` defaults to no-op implementations of each, so the
state machine is fully testable before either hook is wired in, and
`bot.New` wires the real ones during composition.
