package contest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// enrollContestParticipants snapshots every currently-active participant into
// contest_participants for the given contest (R1, R2); left_at stays NULL,
// meaning still obligated. Called once, inside StartContest's transaction --
// there is no mechanism to add participants to a contest afterward.
func enrollContestParticipants(ctx context.Context, tx *sql.Tx, contestID int64) error {
	ids, err := collectParticipantIDs(ctx, tx, `SELECT id FROM participants WHERE active = 1`)
	if err != nil {
		return err
	}

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO contest_participants (contest_id, participant_id) VALUES (?, ?)
		`, contestID, id); err != nil {
			return fmt.Errorf("contest: enroll participant %d: %w", id, err)
		}
	}
	return nil
}

// SetParticipantLeft sets a participant's contest_participants.left_at for
// whichever contest is currently active (R3), which is what makes them
// no-longer-obligated -- there's no separate active flag to keep in sync
// (obligated is exactly left_at IS NULL). A no-op, not an error, when no
// contest is active, the participant is unknown, or they aren't enrolled in
// the active contest.
func SetParticipantLeft(ctx context.Context, db *sql.DB, telegramUserID int64) error {
	var participantID int64
	err := db.QueryRowContext(ctx, `SELECT id FROM participants WHERE telegram_user_id = ?`, telegramUserID).Scan(&participantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("contest: look up participant for telegram user %d: %w", telegramUserID, err)
	}

	contestID, err := activeContestID(ctx, db)
	if errors.Is(err, ErrNoActiveContest) {
		return nil
	}
	if err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE contest_participants SET left_at = datetime('now')
		WHERE contest_id = ? AND participant_id = ? AND left_at IS NULL
	`, contestID, participantID); err != nil {
		return fmt.Errorf("contest: mark participant %d left in contest %d: %w", participantID, contestID, err)
	}
	return nil
}

// StrikesForParticipant computes how many strikes a participant has within a
// specific contest (R6), derived from existing weeks/submissions/votes/
// quiz_answers data rather than a stored counter (R9).
//
// Only closed weeks count: a week still in songs_collection or
// results_collection hasn't missed its deadline yet. A missed-submission
// strike (R7) requires songs_collection to have already closed (state is
// results_collection or idle) and the participant to have been obligated as
// of songs_collection's start -- left_at IS NULL OR left_at > weeks.created_at,
// since created_at is set once at week creation and never overwritten, unlike
// state_started_at which advance() rewrites on every state transition. A
// missed-results strike (R8) requires results_collection to have already
// closed (state is idle) and obligation as of results_collection's start --
// left_at IS NULL OR left_at > weeks.state_started_at, which is stable from
// the moment results_collection opens through the idle transition.
func StrikesForParticipant(ctx context.Context, db *sql.DB, contestID, participantID int64) (int, error) {
	var missedSubmissions int
	// Comparisons wrap both sides in datetime(...) because left_at (set via
	// SQLite's datetime('now'), UTC) and state_started_at (set by Go code as
	// RFC3339 with a Madrid offset) are not the same string format -- a raw
	// string comparison between them would not reliably reflect chronological
	// order. datetime(...) normalizes both to SQLite's internal UTC form
	// before comparing.
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM weeks w
		WHERE w.contest_id = ?
		AND w.state IN ('results_collection', 'idle')
		AND EXISTS (
			SELECT 1 FROM contest_participants cp
			WHERE cp.contest_id = w.contest_id AND cp.participant_id = ?
			AND (cp.left_at IS NULL OR datetime(cp.left_at) > datetime(w.created_at))
		)
		AND NOT EXISTS (
			SELECT 1 FROM submissions s WHERE s.week_id = w.id AND s.participant_id = ?
		)
	`, contestID, participantID, participantID).Scan(&missedSubmissions); err != nil {
		return 0, fmt.Errorf("contest: count missed-submission strikes: %w", err)
	}

	var missedResults int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM weeks w
		WHERE w.contest_id = ?
		AND w.state = 'idle'
		AND EXISTS (
			SELECT 1 FROM contest_participants cp
			WHERE cp.contest_id = w.contest_id AND cp.participant_id = ?
			AND (cp.left_at IS NULL OR datetime(cp.left_at) > datetime(w.state_started_at))
		)
		AND (
			(SELECT COUNT(*) FROM votes v WHERE v.week_id = w.id AND v.voter_id = ?) <
			(SELECT COUNT(*) FROM submissions s WHERE s.week_id = w.id AND s.participant_id != ?)
			OR
			(SELECT COUNT(*) FROM quiz_answers q WHERE q.week_id = w.id AND q.participant_id = ?) <
			(SELECT COUNT(*) FROM submissions s WHERE s.week_id = w.id AND s.participant_id != ?)
		)
	`, contestID, participantID, participantID, participantID, participantID, participantID).Scan(&missedResults); err != nil {
		return 0, fmt.Errorf("contest: count missed-results strikes: %w", err)
	}

	return missedSubmissions + missedResults, nil
}
