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

// enqueueEarlyFinishNotice enqueues one outbox row for the given phase's
// early-finish notice (R8), snapshotting the resolved deadline into the
// payload so drain never needs to re-read the week's (possibly since
// -advanced) state. The guard checks identity by (action type, week_id)
// alone, not the whole payload -- if it compared the full payload
// (including the deadline), a /modifylimit override change between this
// phase completing and its notice being drained would produce a different
// deadline on the next tick, fail to match the still-pending row, and
// enqueue a second, conflicting notice. It checks every row regardless of
// status (not just pending/in_progress): once a notice is drained to
// 'done', the phase is still open until its deadline arrives, so a later
// tick must not re-enqueue just because no pending row remains. Called
// from inside Tick() while it still holds e.mu, so this check-then-insert
// can't race a concurrent enqueue for the same week.
func enqueueEarlyFinishNotice(ctx context.Context, db *sql.DB, actionType string, weekID int64, deadline time.Time) error {
	rows, err := db.QueryContext(ctx, `SELECT payload_json FROM outbox_actions WHERE action_type = ?`, actionType)
	if err != nil {
		return fmt.Errorf("contest: list existing %s actions: %w", actionType, err)
	}
	defer rows.Close()
	for rows.Next() {
		var payloadJSON string
		if err := rows.Scan(&payloadJSON); err != nil {
			return fmt.Errorf("contest: scan existing %s action: %w", actionType, err)
		}
		var existing struct {
			WeekID int64 `json:"week_id"`
		}
		if err := json.Unmarshal([]byte(payloadJSON), &existing); err != nil {
			return fmt.Errorf("contest: unmarshal existing %s payload: %w", actionType, err)
		}
		if existing.WeekID == weekID {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("contest: list existing %s actions: %w", actionType, err)
	}

	payload, err := json.Marshal(map[string]any{
		"week_id":  weekID,
		"deadline": deadline.Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("contest: marshal %s payload: %w", actionType, err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO outbox_actions (action_type, payload_json, status) VALUES (?, ?, 'pending')
	`, actionType, string(payload)); err != nil {
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

	if err := markOutboxInProgress(ctx, db, actionID); err != nil {
		return err
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

	return markOutboxDone(ctx, db, actionID)
}
