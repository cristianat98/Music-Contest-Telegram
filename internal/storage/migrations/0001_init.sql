-- +goose Up
CREATE TABLE contests (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    active     INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX idx_contests_active ON contests (active) WHERE active = 1;

-- Topics are a global catalog, reusable across contests. Per-contest usage
-- is tracked separately in topic_usage so a new contest's pool starts fully
-- unused (R7/R10) without needing to duplicate or discard topic text.
CREATE TABLE topics (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    text       TEXT    NOT NULL UNIQUE,
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- Guarantees a contest's topic catalog is never empty even before any other
-- topic has been manually associated (R16).
INSERT INTO topics (text) VALUES ('normal');

-- Membership of the global topic catalog into a specific contest's rotation
-- (R10/R11), plus whether the topic is still eligible to be picked in that
-- contest (R13) -- selectable flips to false once picked, with no reset;
-- once every associated topic is unselectable, picking falls back to a
-- repeat (R12) instead of failing.
CREATE TABLE topic_usage (
    contest_id INTEGER NOT NULL REFERENCES contests (id),
    topic_id   INTEGER NOT NULL REFERENCES topics (id),
    selectable INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (contest_id, topic_id)
);

CREATE INDEX idx_topic_usage_contest ON topic_usage (contest_id, selectable);

CREATE TABLE participants (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    telegram_user_id INTEGER NOT NULL UNIQUE,
    display_name     TEXT    NOT NULL DEFAULT '',
    active           INTEGER NOT NULL DEFAULT 1,
    created_at       TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- Contest-scoped enrollment (R1/R2): snapshotted once at /startcontest from
-- whoever is currently active in participants. left_at IS NULL means still
-- obligated in this contest; it's set once (R3) and compared against
-- weeks.created_at/state_started_at to determine retroactive obligation for
-- strike computation (R7/R8). No separate active flag: obligated is exactly
-- left_at IS NULL, so there's nothing to keep in sync. Strikes themselves
-- are never stored here or anywhere -- they're computed by joining
-- weeks/submissions/votes/quiz_answers against this table (R6/R9).
CREATE TABLE contest_participants (
    contest_id     INTEGER NOT NULL REFERENCES contests (id),
    participant_id INTEGER NOT NULL REFERENCES participants (id),
    left_at        TEXT,
    PRIMARY KEY (contest_id, participant_id)
);

CREATE TABLE weeks (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    contest_id            INTEGER NOT NULL REFERENCES contests (id),
    state                 TEXT    NOT NULL DEFAULT 'idle'
                                  CHECK (state IN ('idle', 'songs_collection', 'results_collection')),
    topic_id              INTEGER REFERENCES topics (id),
    state_started_at      TEXT,
    deadline_override_days INTEGER,
    last_reminder_at      TEXT,
    created_at            TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_weeks_contest ON weeks (contest_id);

CREATE TABLE submissions (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    week_id        INTEGER NOT NULL REFERENCES weeks (id),
    participant_id INTEGER NOT NULL REFERENCES participants (id),
    url            TEXT    NOT NULL,
    -- Assigned once, when songs are published (shuffled): gives ranking
    -- buttons and any retried publish message a stable "Song N" numbering
    -- instead of re-shuffling differently each time. An integer shuffle
    -- position, unrelated to participants.display_name (a shown string
    -- identity) despite the similar column name.
    display_name   INTEGER,
    created_at     TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE (week_id, participant_id)
);

-- points aren't stored: a vote's points are fully derivable from its rank
-- and its voter's required-ranking count (itself derivable from
-- submissions), so storing them would just be the same fact twice. See
-- submissionPoints in results.go, which computes them at read time.
CREATE TABLE votes (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    week_id        INTEGER NOT NULL REFERENCES weeks (id),
    voter_id       INTEGER NOT NULL REFERENCES participants (id),
    submission_id  INTEGER NOT NULL REFERENCES submissions (id),
    rank           INTEGER NOT NULL,
    UNIQUE (week_id, voter_id, submission_id)
);

CREATE TABLE quiz_answers (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    week_id        INTEGER NOT NULL REFERENCES weeks (id),
    participant_id INTEGER NOT NULL REFERENCES participants (id),
    submission_id  INTEGER NOT NULL REFERENCES submissions (id),
    already_knew   INTEGER NOT NULL,
    UNIQUE (week_id, participant_id, submission_id)
);

CREATE TABLE outbox_actions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    action_type  TEXT    NOT NULL,
    payload_json TEXT    NOT NULL,
    status       TEXT    NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending', 'in_progress', 'done')),
    created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT
);

CREATE INDEX idx_outbox_status ON outbox_actions (status);

-- +goose Down
DROP TABLE outbox_actions;
DROP TABLE quiz_answers;
DROP TABLE votes;
DROP TABLE submissions;
DROP TABLE weeks;
DROP TABLE contest_participants;
DROP TABLE participants;
DROP TABLE topic_usage;
DROP TABLE topics;
DROP TABLE contests;
