package contest

import (
	"context"
	"database/sql"
	"fmt"
)

// PendingOutboxAction is one outbox_actions row not yet fully processed.
type PendingOutboxAction struct {
	ID      int64
	Payload string
}

// ListPendingOutboxActions returns the pending/in_progress outbox_actions
// rows for actionType, shared by every outbox consumer (publish_songs,
// publish_results, results_partial_notice, start_results_prompt) to
// dequeue their work.
func ListPendingOutboxActions(ctx context.Context, db *sql.DB, actionType string) ([]PendingOutboxAction, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, payload_json FROM outbox_actions WHERE action_type = ? AND status IN ('pending', 'in_progress')
	`, actionType)
	if err != nil {
		return nil, fmt.Errorf("contest: list pending %s actions: %w", actionType, err)
	}
	defer rows.Close()

	var actions []PendingOutboxAction
	for rows.Next() {
		var a PendingOutboxAction
		if err := rows.Scan(&a.ID, &a.Payload); err != nil {
			return nil, fmt.Errorf("contest: scan %s action: %w", actionType, err)
		}
		actions = append(actions, a)
	}
	return actions, rows.Err()
}

// markOutboxInProgress and markOutboxDone are the two status-transition
// steps every outbox consumer (publish_songs, publish_results,
// results_partial_notice, songs/results_early_finish) performs identically
// around its own send logic.
func markOutboxInProgress(ctx context.Context, db *sql.DB, actionID int64) error {
	if _, err := db.ExecContext(ctx, `UPDATE outbox_actions SET status = 'in_progress' WHERE id = ?`, actionID); err != nil {
		return fmt.Errorf("contest: mark outbox action %d in_progress: %w", actionID, err)
	}
	return nil
}

func markOutboxDone(ctx context.Context, db *sql.DB, actionID int64) error {
	if _, err := db.ExecContext(ctx, `
		UPDATE outbox_actions SET status = 'done', completed_at = datetime('now') WHERE id = ?
	`, actionID); err != nil {
		return fmt.Errorf("contest: mark outbox action %d done: %w", actionID, err)
	}
	return nil
}
