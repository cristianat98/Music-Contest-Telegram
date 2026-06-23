package contest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

const OutboxActionPublishSongs = "publish_songs"

// GroupNotifier sends a message to the contest's group chat. Defined here
// rather than imported from the bot package so this package never depends
// on go-telegram/bot.
type GroupNotifier interface {
	SendGroupMessage(ctx context.Context, text string) error
}

// SongsHooks implements SongsCollectionHooks: striking stragglers on a
// forced close (R20) and queuing the durable publish action (R19) that
// PublishDueSongs later processes.
type SongsHooks struct {
	db *sql.DB
}

func NewSongsHooks(db *sql.DB) *SongsHooks {
	return &SongsHooks{db: db}
}

func (h *SongsHooks) CloseSongsCollection(ctx context.Context, tx *sql.Tx, weekID int64, forced bool) error {
	if forced {
		if err := strikeMissingSubmitters(ctx, tx, weekID); err != nil {
			return err
		}
	}

	// Assign display_order here, inside the same transaction that closes
	// songs_collection, rather than waiting for the separate
	// PublishDueSongs outbox processing: OpenResultsCollection (called
	// right after this, in the same advance()) enqueues the questionnaire/
	// ranking prompts, and those need display_order to already exist or
	// PendingQuizSubmissions/PendingRankingSubmissions would scan NULL.
	if err := ensureDisplayOrderAssigned(ctx, tx, weekID); err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{"week_id": weekID})
	if err != nil {
		return fmt.Errorf("contest: marshal publish_songs payload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO outbox_actions (action_type, payload_json, status) VALUES (?, ?, 'pending')
	`, OutboxActionPublishSongs, string(payload)); err != nil {
		return fmt.Errorf("contest: enqueue publish_songs: %w", err)
	}
	return nil
}

func (h *SongsHooks) SongsCollectionComplete(ctx context.Context, db *sql.DB, weekID int64) (bool, error) {
	var total, submitted int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM week_participants WHERE week_id = ?`, weekID).Scan(&total); err != nil {
		return false, fmt.Errorf("contest: count required participants: %w", err)
	}
	if total == 0 {
		return false, nil
	}
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM week_participants wp
		JOIN submissions s ON s.week_id = wp.week_id AND s.participant_id = wp.participant_id
		WHERE wp.week_id = ?
	`, weekID).Scan(&submitted)
	if err != nil {
		return false, fmt.Errorf("contest: count submissions: %w", err)
	}
	return submitted >= total, nil
}

// strikeMissingSubmitters records a strike (R20) for any required
// participant who hasn't submitted by the time songs_collection closes,
// guarded by submission_struck so a defensive re-run can't double-strike.
func strikeMissingSubmitters(ctx context.Context, tx *sql.Tx, weekID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT wp.participant_id FROM week_participants wp
		WHERE wp.week_id = ? AND wp.submission_struck = 0 AND NOT EXISTS (
			SELECT 1 FROM submissions s WHERE s.week_id = wp.week_id AND s.participant_id = wp.participant_id
		)
	`, weekID)
	if err != nil {
		return fmt.Errorf("contest: list missing submitters: %w", err)
	}
	defer rows.Close()

	var missing []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("contest: scan missing submitter: %w", err)
		}
		missing = append(missing, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, participantID := range missing {
		if _, err := tx.ExecContext(ctx, `
			UPDATE week_participants SET submission_struck = 1 WHERE week_id = ? AND participant_id = ?
		`, weekID, participantID); err != nil {
			return fmt.Errorf("contest: mark submission_struck for %d: %w", participantID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE participants SET strikes = strikes + 1 WHERE id = ?
		`, participantID); err != nil {
			return fmt.Errorf("contest: increment strikes for %d: %w", participantID, err)
		}
	}
	return nil
}

// submissionRow is a row from submissions joined with its topic-less data
// needed for publication: just the URL, deliberately not the submitter.
type submissionRow struct {
	url string
}

// PublishDueSongs processes any pending publish_songs outbox rows: shuffling
// the week's submissions and posting them with no sender attribution (R19).
// Safe to call on every tick; idempotent per outbox row via the
// pending->in_progress->done lifecycle (KTD4).
func PublishDueSongs(ctx context.Context, db *sql.DB, notifier GroupNotifier) error {
	actions, err := ListPendingOutboxActions(ctx, db, OutboxActionPublishSongs)
	if err != nil {
		return err
	}

	for _, a := range actions {
		if err := processPublishSongsAction(ctx, db, notifier, a.ID, a.Payload); err != nil {
			return err
		}
	}
	return nil
}

func processPublishSongsAction(ctx context.Context, db *sql.DB, notifier GroupNotifier, actionID int64, payloadJSON string) error {
	var payload struct {
		WeekID int64 `json:"week_id"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return fmt.Errorf("contest: unmarshal publish_songs payload: %w", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE outbox_actions SET status = 'in_progress' WHERE id = ?`, actionID); err != nil {
		return fmt.Errorf("contest: mark publish_songs in_progress: %w", err)
	}

	urls, err := orderedSubmissionURLs(ctx, db, payload.WeekID)
	if err != nil {
		return err
	}

	text := "Songs are in! Listen in this order (no names attached) -- you'll rank them by this number:\n"
	for i, url := range urls {
		text += fmt.Sprintf("%d. %s\n", i+1, url)
	}

	// Re-render from the now-persisted display_order on retry rather than
	// tracking a separate idempotency key: display_order is assigned once
	// and reused, so a resend renders byte-identical content -- never a
	// duplicate or divergent side effect (resolves doc-review finding A2 on
	// outbox idempotency for Telegram sends). The stable order also gives
	// U6's ranking buttons consistent "Song N" numbering.
	if err := notifier.SendGroupMessage(ctx, text); err != nil {
		return fmt.Errorf("contest: send publish_songs message: %w", err)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE outbox_actions SET status = 'done', completed_at = datetime('now') WHERE id = ?
	`, actionID); err != nil {
		return fmt.Errorf("contest: mark publish_songs done: %w", err)
	}
	return nil
}

// ensureDisplayOrderAssigned shuffles and persists a stable display_order
// for a week's submissions, but only the first time it's called for that
// week -- a retry sees every row already assigned and is a no-op, which is
// what makes the published numbering (and the ranking buttons built on it)
// stable across outbox retries.
func ensureDisplayOrderAssigned(ctx context.Context, q querier, weekID int64) error {
	var unassigned int
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM submissions WHERE week_id = ? AND display_order IS NULL
	`, weekID).Scan(&unassigned); err != nil {
		return fmt.Errorf("contest: count unassigned display_order: %w", err)
	}
	if unassigned == 0 {
		return nil
	}

	rows, err := q.QueryContext(ctx, `SELECT id FROM submissions WHERE week_id = ? ORDER BY RANDOM()`, weekID)
	if err != nil {
		return fmt.Errorf("contest: shuffle submissions: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("contest: scan submission id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for i, id := range ids {
		if _, err := q.ExecContext(ctx, `
			UPDATE submissions SET display_order = ? WHERE id = ?
		`, i+1, id); err != nil {
			return fmt.Errorf("contest: assign display_order for submission %d: %w", id, err)
		}
	}
	return nil
}

func orderedSubmissionURLs(ctx context.Context, db *sql.DB, weekID int64) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT url FROM submissions WHERE week_id = ? ORDER BY display_order
	`, weekID)
	if err != nil {
		return nil, fmt.Errorf("contest: list submissions for publish: %w", err)
	}
	defer rows.Close()

	var urls []string
	for rows.Next() {
		var s submissionRow
		if err := rows.Scan(&s.url); err != nil {
			return nil, fmt.Errorf("contest: scan submission url: %w", err)
		}
		urls = append(urls, s.url)
	}
	return urls, rows.Err()
}
