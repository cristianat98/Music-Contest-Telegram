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

`/startcontest` enrolls every currently-active Telegram participant into
`contest_participants` (no later joiners) and associates the seeded
`"normal"` topic so the new contest's catalog is never empty (R1, R16).
Strikes are never reset -- they're computed per contest from
`contest_participants`/`weeks`/`submissions`/`votes`/`quiz_answers`, so a new
contest naturally starts at zero without any explicit reset step. When a
participant leaves the Telegram group, `participants.active` flips false and
`contest_participants.left_at` is set for whichever contest is active (R3):
they stop blocking week completion and their contribution is zeroed/marked
in results, without erasing what they already did.

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
  Start["/startweek: pick topic from contest's associated subset\n(prefer unused, fall back to repeat)"] --> Open[songs_collection open]
  Open --> Submit["Participant sends a YouTube URL privately\n(accepted only while songs_collection is open --\nsame window /fixsubmission, /removesubmission honor)"]
  Submit --> Validate{Format-only URL check}
  Validate -->|invalid| Reject[Reply: not a valid URL]
  Validate -->|valid| Store[Upsert submissions row]
  Store --> CheckComplete{All active contest_participants submitted?}
  CheckComplete -->|no, deadline not yet hit| Wait[Wait; daily 20:00 reminder lists active stragglers]
  CheckComplete -->|yes| Close[CloseSongsCollection: assign display_name, enqueue publish_songs]
  Admin["/forceadvance"] --> ForceClose["CloseSongsCollection forced=true\n(no strike write -- a missing submission for an\nactive participant is already a computable strike)"]
  ForceClose --> Close
  Close --> Transition[advance to results_collection]
  Transition --> OpenResults[OpenResultsCollection: enqueue start_results_prompt per active participant]
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
  RankPick --> RecordRank["contest.RecordRankingPick: stores rank = count+1 only"]
  RecordRank --> MoreRank{Songs left to rank?}
  MoreRank -->|yes| FirstRank
  MoreRank -->|no| Done[Participant done: questionnaire + ranking complete]
  Done --> CheckAllDone{All required participants done?}
  CheckAllDone -->|no| WaitR[Wait; incompleteness only becomes a strike at close]
  CheckAllDone -->|yes| CloseNatural[CloseResultsCollection forced=false]
  AdminForce["/forceadvance"] --> CloseForced["CloseResultsCollection forced=true: discard incomplete\nranking, queue partial-notice (no strike write --\nthe gap is already a computable strike)"]
  CloseForced --> EnqueuePublish[Enqueue publish_results]
  CloseNatural --> EnqueuePublish
  EnqueuePublish --> Announce["FinalResults derives each song's points from its votes' rank\n(zeroed for a song known by >=3 beforehand, no redistribution;\nzeroed for a departed submitter's song instead of its real score),\nthen the bot posts them de-anonymized"]
  Announce --> Idle[week returns to idle]
```
