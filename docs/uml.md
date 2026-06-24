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
    SongsHooks ..|> SongsCollectionHooks : implements
    ResultsHooks ..|> ResultsCollectionHooks : implements
    SongsHooks --> GroupNotifier : publish_songs outbox
    ResultsHooks --> ResultsNotifier : publish_results / partial-notice outbox
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
