package contest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const (
	OutboxActionSongsEarlyFinish   = "songs_early_finish"
	OutboxActionResultsEarlyFinish = "results_early_finish"
)

// enqueueEarlyFinishNotice guard-inserts one outbox row for the given
// phase's early-finish notice (R8), snapshotting the resolved deadline into
// the payload so drain never needs to re-read the week's (possibly since
// -advanced) state. The insert is a no-op when a row with this exact
// action type + payload already exists -- since deadline is a pure
// function of state_started_at/override/contest-default, none of which
// change while the phase is still open, re-invoking this on every tick
// before the deadline produces the same payload each time, so the guard
// reliably fires the notice at most once per phase instance (R9, KTD4).
// Called from inside Tick() while it still holds e.mu, not a separate
// function, so a /forceadvance racing the gap can't slip in first.
func enqueueEarlyFinishNotice(ctx context.Context, db *sql.DB, actionType string, weekID int64, deadline time.Time) error {
	payload, err := json.Marshal(map[string]any{
		"week_id":  weekID,
		"deadline": deadline.Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("contest: marshal %s payload: %w", actionType, err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO outbox_actions (action_type, payload_json, status)
		SELECT ?, ?, 'pending'
		WHERE NOT EXISTS (
			SELECT 1 FROM outbox_actions WHERE action_type = ? AND payload_json = ?
		)
	`, actionType, string(payload), actionType, string(payload)); err != nil {
		return fmt.Errorf("contest: enqueue %s: %w", actionType, err)
	}
	return nil
}

// ProcessEarlyFinishNotices drains pending early-finish outbox rows for
// both phases, posting a one-time group notice naming when publication
// will happen. Safe to call on every tick: idempotent per outbox row via
// the pending->in_progress->done lifecycle, the same shape PublishDueSongs
// uses.
func ProcessEarlyFinishNotices(ctx context.Context, db *sql.DB, notifier GroupNotifier) error {
	for _, actionType := range []string{OutboxActionSongsEarlyFinish, OutboxActionResultsEarlyFinish} {
		actions, err := ListPendingOutboxActions(ctx, db, actionType)
		if err != nil {
			return err
		}
		for _, a := range actions {
			if err := processEarlyFinishAction(ctx, db, notifier, actionType, a.ID, a.Payload); err != nil {
				return err
			}
		}
	}
	return nil
}

func processEarlyFinishAction(ctx context.Context, db *sql.DB, notifier GroupNotifier, actionType string, actionID int64, payloadJSON string) error {
	var payload struct {
		Deadline string `json:"deadline"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return fmt.Errorf("contest: unmarshal %s payload: %w", actionType, err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE outbox_actions SET status = 'in_progress' WHERE id = ?`, actionID); err != nil {
		return fmt.Errorf("contest: mark %s in_progress: %w", actionType, err)
	}

	deadline, err := time.Parse(time.RFC3339, payload.Deadline)
	if err != nil {
		return fmt.Errorf("contest: parse %s deadline: %w", actionType, err)
	}

	phaseName := "songs"
	if actionType == OutboxActionResultsEarlyFinish {
		phaseName = "results"
	}
	text := fmt.Sprintf(
		"Everyone's in for the %s phase -- publishing on %s.",
		phaseName, deadline.In(madridLocation).Format("Mon 2 Jan 15:04"),
	)
	if err := notifier.SendGroupMessage(ctx, text); err != nil {
		return fmt.Errorf("contest: send %s message: %w", actionType, err)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE outbox_actions SET status = 'done', completed_at = datetime('now') WHERE id = ?
	`, actionID); err != nil {
		return fmt.Errorf("contest: mark %s done: %w", actionType, err)
	}
	return nil
}
