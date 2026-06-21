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

CREATE TABLE topic_usage (
    contest_id INTEGER NOT NULL REFERENCES contests (id),
    topic_id   INTEGER NOT NULL REFERENCES topics (id),
    used       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (contest_id, topic_id)
);

CREATE INDEX idx_topic_usage_contest ON topic_usage (contest_id, used);

CREATE TABLE participants (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    telegram_user_id INTEGER NOT NULL UNIQUE,
    display_name     TEXT    NOT NULL DEFAULT '',
    active           INTEGER NOT NULL DEFAULT 1,
    strikes          INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE weeks (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    contest_id            INTEGER NOT NULL REFERENCES contests (id),
    state                 TEXT    NOT NULL DEFAULT 'idle'
                                  CHECK (state IN ('idle', 'songs_collection', 'results_collection')),
    topic_id              INTEGER REFERENCES topics (id),
    state_started_at      TEXT,
    deadline_override_days INTEGER,
    created_at            TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_weeks_contest ON weeks (contest_id);

-- Required-participant snapshot for a week (R11): who must submit/vote, and
-- whether their strike for this week has already been recorded.
CREATE TABLE week_participants (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    week_id        INTEGER NOT NULL REFERENCES weeks (id),
    participant_id INTEGER NOT NULL REFERENCES participants (id),
    submission_struck INTEGER NOT NULL DEFAULT 0,
    results_struck    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (week_id, participant_id)
);

CREATE TABLE submissions (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    week_id        INTEGER NOT NULL REFERENCES weeks (id),
    participant_id INTEGER NOT NULL REFERENCES participants (id),
    url            TEXT    NOT NULL,
    created_at     TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE (week_id, participant_id)
);

CREATE TABLE votes (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    week_id        INTEGER NOT NULL REFERENCES weeks (id),
    voter_id       INTEGER NOT NULL REFERENCES participants (id),
    submission_id  INTEGER NOT NULL REFERENCES submissions (id),
    rank           INTEGER NOT NULL,
    points         INTEGER NOT NULL DEFAULT 0,
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
DROP TABLE week_participants;
DROP TABLE weeks;
DROP TABLE participants;
DROP TABLE topic_usage;
DROP TABLE topics;
DROP TABLE contests;
