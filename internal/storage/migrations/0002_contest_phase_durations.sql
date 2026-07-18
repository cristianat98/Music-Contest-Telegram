-- +goose Up
-- Per-contest phase durations, in days, using the same day-counting
-- convention as DefaultDeadlineDays (contest.DefaultDeadlineDays). NULL
-- means "use the package default" -- read via COALESCE at lookup time
-- (contestPhaseDefaultDays in deadline.go) -- so existing contests need no
-- backfill: a plain ADD COLUMN already leaves them NULL, which already
-- means "behave exactly as before".
ALTER TABLE contests ADD COLUMN songs_deadline_days INTEGER;
ALTER TABLE contests ADD COLUMN results_deadline_days INTEGER;

-- +goose Down
ALTER TABLE contests DROP COLUMN songs_deadline_days;
ALTER TABLE contests DROP COLUMN results_deadline_days;
