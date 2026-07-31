package contest

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Tick re-checks the active week against natural-completion criteria and
// advances the state machine if met (R19, R26). It takes the same lock as
// the command handlers so a tick-driven close can't race an admin command
// (resolves doc-review finding A1, which flagged that KTD5's lock only
// named admin commands, not the tick's own auto-transitions).
func (e *Engine) Tick(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	contestID, err := activeContestID(ctx, e.db)
	if errors.Is(err, ErrNoActiveContest) {
		return nil
	}
	if err != nil {
		return err
	}
	w, err := e.currentWeek(ctx, e.db, contestID)
	if err != nil {
		return err
	}
	if w == nil || w.state == StateIdle {
		return nil
	}

	var complete bool
	switch w.state {
	case StateSongsCollection:
		complete, err = e.songsHooks.SongsCollectionComplete(ctx, e.db, w.id)
	case StateResultsCollection:
		complete, err = e.resultsHooks.ResultsCollectionComplete(ctx, e.db, w.id)
	}
	if err != nil {
		return err
	}
	if !complete {
		return nil
	}

	var override *int
	if w.deadlineOverrideDays.Valid {
		days := int(w.deadlineOverrideDays.Int64)
		override = &days
	}
	deadline, err := e.phaseDeadline(ctx, contestID, w.state, w.stateStartedAt.String, override)
	if err != nil {
		return err
	}
	if time.Now().In(madridLocation).Before(deadline) {
		actionType := OutboxActionSongsEarlyFinish
		if w.state == StateResultsCollection {
			actionType = OutboxActionResultsEarlyFinish
		}
		return enqueueEarlyFinishNotice(ctx, e.db, actionType, w.id, deadline)
	}

	_, err = e.advance(ctx, w, false)
	return err
}

// phaseDeadline computes the deadline for the given contest's phase
// (state), starting at stateStartedAt (RFC3339): the contest's configured
// duration for that phase (or DefaultDeadlineDays when unset, per
// contestPhaseDefaultDays), unless override is non-nil, which still takes
// precedence exactly as before (R6). Shared by Tick's natural-completion
// check and ModifyLimit's override-preview message -- the only difference
// between the two call sites is where the override comes from
// (weeks.deadline_override_days vs. the command's argument).
func (e *Engine) phaseDeadline(ctx context.Context, contestID int64, state, stateStartedAt string, override *int) (time.Time, error) {
	defaultDays, err := contestPhaseDefaultDays(ctx, e.db, contestID, state)
	if err != nil {
		return time.Time{}, err
	}
	started, err := time.Parse(time.RFC3339, stateStartedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("contest: parse state_started_at: %w", err)
	}
	return Deadline(started, override, defaultDays), nil
}

// advance performs the actual songs_collection->results_collection or
// results_collection->idle transition, delegating the close-time side
// effects to hooks within the same transaction.
func (e *Engine) advance(ctx context.Context, w *week, forced bool) (string, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("contest: begin tx: %w", err)
	}
	defer tx.Rollback()

	switch w.state {
	case StateSongsCollection:
		if err := e.songsHooks.CloseSongsCollection(ctx, tx, w.id, forced); err != nil {
			return "", fmt.Errorf("contest: close songs_collection: %w", err)
		}
		now := time.Now().In(madridLocation)
		if _, err := tx.ExecContext(ctx, `
			UPDATE weeks SET state = ?, state_started_at = ?, deadline_override_days = NULL WHERE id = ?
		`, StateResultsCollection, now.Format(time.RFC3339), w.id); err != nil {
			return "", fmt.Errorf("contest: transition to results_collection: %w", err)
		}
		if err := e.resultsHooks.OpenResultsCollection(ctx, tx, w.id); err != nil {
			return "", fmt.Errorf("contest: open results_collection: %w", err)
		}
	case StateResultsCollection:
		if err := e.resultsHooks.CloseResultsCollection(ctx, tx, w.id, forced); err != nil {
			return "", fmt.Errorf("contest: close results_collection: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE weeks SET state = ?, deadline_override_days = NULL WHERE id = ?
		`, StateIdle, w.id); err != nil {
			return "", fmt.Errorf("contest: transition to idle: %w", err)
		}
	default:
		return "", fmt.Errorf("contest: cannot advance from state %q", w.state)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("contest: commit advance: %w", err)
	}

	return "Advanced.", nil
}
