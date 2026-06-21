# Flows

## Contest / week state machine

```mermaid
stateDiagram-v2
  [*] --> NoActiveContest
  NoActiveContest --> ContestActive: /startcontest
  ContestActive --> NoActiveContest: /finishcontest (week idle)

  state ContestActive {
    [*] --> idle
    idle --> songs_collection: /startweek (>=2 eligible participants)
    songs_collection --> results_collection: all submitted OR /forceadvance
    results_collection --> idle: all voted+answered OR /forceadvance
  }
```

`/startcontest` resets every participant's strike count and the new
contest's topic pool (KTD7, R10, R12) -- the pool reset falls out of the
schema for free, since `topic_usage` rows are scoped per `contest_id`.

## Tick (every 15 minutes, plus once on startup)

```mermaid
flowchart TB
  Tick[Tick fires] --> Lifecycle[contest.Engine.Tick: natural-completion check + advance]
  Lifecycle --> Reminders[contest.SendDueReminders: 20:00 Europe/Madrid daily reminder]
  Reminders --> PublishSongs[contest.PublishDueSongs: drain pending publish_songs outbox rows]
  PublishSongs --> ResultsPrompts[bot.ProcessResultsPrompts: drain pending start_results_prompt outbox rows]
  ResultsPrompts --> ResultsNotify[bot.ProcessResultsNotifications: drain partial-notice + publish_results outbox rows]
  ResultsNotify --> AdminCheck[bot.CheckAdminStatus: self GetChatMember, queue alert if lost]
```

Each step is independent and logs its own failure rather than aborting the
rest (a stalled Telegram call in one step shouldn't block the others). The
lock inside `contest.Engine` (KTD5) covers both admin commands and this
tick's own auto-advance check, so a tick-driven close can't race a
concurrent `/forceadvance`.

## Songs collection cycle (songs_collection)

```mermaid
flowchart TB
  Start["/startweek: pick random unused topic, snapshot required participants"] --> Open[songs_collection open]
  Open --> Submit[Participant sends a YouTube URL privately]
  Submit --> Validate{Format-only URL check}
  Validate -->|invalid| Reject[Reply: not a valid URL]
  Validate -->|valid| Store[Upsert submissions row]
  Store --> CheckComplete{All required participants submitted?}
  CheckComplete -->|no, deadline not yet hit| Wait[Wait; daily 20:00 reminder lists stragglers]
  CheckComplete -->|yes| Close[CloseSongsCollection: assign display_order, enqueue publish_songs]
  Admin["/forceadvance"] --> ForceClose[CloseSongsCollection forced=true: strike stragglers]
  ForceClose --> Close
  Close --> Transition[advance to results_collection]
  Transition --> OpenResults[OpenResultsCollection: enqueue start_results_prompt per participant]
```

## Results collection cycle (results_collection)

```mermaid
flowchart TB
  OpenResults[start_results_prompt outbox row per participant] --> FirstQuiz[bot sends first familiarity question]
  FirstQuiz --> QuizAnswer[Participant taps Yes/No]
  QuizAnswer --> RecordQuiz[contest.RecordQuizAnswer]
  RecordQuiz --> MoreQuiz{More songs to ask about?}
  MoreQuiz -->|yes| FirstQuiz
  MoreQuiz -->|no| FirstRank[bot sends remaining-songs ranking buttons]
  FirstRank --> RankPick[Participant picks next favorite]
  RankPick --> RecordRank["contest.RecordRankingPick: rank = count+1, points = required-rank+1"]
  RecordRank --> MoreRank{Songs left to rank?}
  MoreRank -->|yes| FirstRank
  MoreRank -->|no| Done[Participant done: questionnaire + ranking complete]
  Done --> CheckAllDone{All required participants done?}
  CheckAllDone -->|no| WaitR[Wait; no auto-strike until close]
  CheckAllDone -->|yes| CloseNatural[CloseResultsCollection forced=false]
  AdminForce["/forceadvance"] --> CloseForced[CloseResultsCollection forced=true: discard incomplete ranking, strike once, queue partial-notice]
  CloseForced --> Disqualify
  CloseNatural --> Disqualify[disqualifyOverfamiliarSongs: zero points for songs known by >=3, no redistribution]
  Disqualify --> EnqueuePublish[Enqueue publish_results]
  EnqueuePublish --> Announce[bot posts per-song points + familiarity outcome, de-anonymized]
  Announce --> Idle[week returns to idle]
```
