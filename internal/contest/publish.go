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
	rows, err := db.QueryContext(ctx, `
		SELECT id, payload_json FROM outbox_actions WHERE action_type = ? AND status IN ('pending', 'in_progress')
	`, OutboxActionPublishSongs)
	if err != nil {
		return fmt.Errorf("contest: list pending publish_songs actions: %w", err)
	}
	type pending struct {
		id      int64
		payload string
	}
	var actions []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.payload); err != nil {
			rows.Close()
			return fmt.Errorf("contest: scan publish_songs action: %w", err)
		}
		actions = append(actions, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, a := range actions {
		if err := processPublishSongsAction(ctx, db, notifier, a.id, a.payload); err != nil {
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

	urls, err := shuffledSubmissionURLs(ctx, db, payload.WeekID)
	if err != nil {
		return err
	}

	text := "Songs are in! Listen in this shuffled order (no names attached):\n"
	for i, url := range urls {
		text += fmt.Sprintf("%d. %s\n", i+1, url)
	}

	// Re-render from current DB state on retry rather than tracking a
	// separate idempotency key: a resend (even with a freshly re-shuffled
	// order, since voting doesn't depend on this announcement's order) is
	// harmless, just a re-announcement of the same songs -- never a
	// duplicate side effect (resolves doc-review finding A2 on outbox
	// idempotency for Telegram sends).
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

func shuffledSubmissionURLs(ctx context.Context, db *sql.DB, weekID int64) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT url FROM submissions WHERE week_id = ? ORDER BY RANDOM()
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
