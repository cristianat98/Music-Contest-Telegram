package contest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

const (
	disqualifyThreshold = 3

	OutboxActionStartResultsPrompt = "start_results_prompt"
	OutboxActionPartialNotice      = "results_partial_notice"
	OutboxActionPublishResults     = "publish_results"
)

// ResultsNotifier lets this package post the final results announcement to
// the group and a partial-progress notice to a single participant (R25),
// without depending on go-telegram/bot.
type ResultsNotifier interface {
	GroupNotifier
	SendPrivateMessage(ctx context.Context, telegramUserID int64, text string) error
}

// ResultsHooks implements ResultsCollectionHooks: dispatching the initial
// questionnaire/ranking prompts (R21), computing completion (R24), scoring
// and disqualification (R23, R27), discarding-and-notifying incomplete
// sessions on a forced close (R24, R25), and striking anyone not done
// (R28).
type ResultsHooks struct {
	db *sql.DB
}

func NewResultsHooks(db *sql.DB) *ResultsHooks {
	return &ResultsHooks{db: db}
}

func (h *ResultsHooks) OpenResultsCollection(ctx context.Context, tx *sql.Tx, weekID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT participant_id FROM week_participants WHERE week_id = ?`, weekID)
	if err != nil {
		return fmt.Errorf("contest: list required participants: %w", err)
	}
	var participantIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("contest: scan participant id: %w", err)
		}
		participantIDs = append(participantIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, participantID := range participantIDs {
		payload, err := json.Marshal(map[string]any{"week_id": weekID, "participant_id": participantID})
		if err != nil {
			return fmt.Errorf("contest: marshal start_results_prompt payload: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO outbox_actions (action_type, payload_json, status) VALUES (?, ?, 'pending')
		`, OutboxActionStartResultsPrompt, string(payload)); err != nil {
			return fmt.Errorf("contest: enqueue start_results_prompt for %d: %w", participantID, err)
		}
	}
	return nil
}

// requiredVoteCount is how many songs a participant must rank and answer
// the familiarity question for: every submission except their own, if they
// have one (R22 excludes a participant's own song from their ranking list).
func requiredVoteCount(ctx context.Context, q querier, weekID, participantID int64) (int, error) {
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM submissions WHERE week_id = ?`, weekID).Scan(&total); err != nil {
		return 0, fmt.Errorf("contest: count submissions: %w", err)
	}
	var ownSubmission int
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM submissions WHERE week_id = ? AND participant_id = ?
	`, weekID, participantID).Scan(&ownSubmission); err != nil {
		return 0, fmt.Errorf("contest: check own submission: %w", err)
	}
	return total - ownSubmission, nil
}

// participantDone reports whether a participant has completed both the
// ranking and the familiarity questionnaire for a week (R24).
func participantDone(ctx context.Context, q querier, weekID, participantID int64) (bool, error) {
	required, err := requiredVoteCount(ctx, q, weekID, participantID)
	if err != nil {
		return false, err
	}
	if required == 0 {
		return true, nil
	}

	var rankedCount int
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM votes WHERE week_id = ? AND voter_id = ?
	`, weekID, participantID).Scan(&rankedCount); err != nil {
		return false, fmt.Errorf("contest: count votes: %w", err)
	}

	var quizCount int
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM quiz_answers WHERE week_id = ? AND participant_id = ?
	`, weekID, participantID).Scan(&quizCount); err != nil {
		return false, fmt.Errorf("contest: count quiz answers: %w", err)
	}

	return rankedCount >= required && quizCount >= required, nil
}

func (h *ResultsHooks) ResultsCollectionComplete(ctx context.Context, db *sql.DB, weekID int64) (bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT participant_id FROM week_participants WHERE week_id = ?`, weekID)
	if err != nil {
		return false, fmt.Errorf("contest: list required participants: %w", err)
	}
	var participantIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, fmt.Errorf("contest: scan participant id: %w", err)
		}
		participantIDs = append(participantIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()

	// The outer query must be fully drained and closed before issuing the
	// nested per-participant queries below: SetMaxOpenConns(1) (KTD2) means
	// only one connection exists at all, so an open, mid-iteration *Rows
	// would otherwise deadlock against the nested QueryRowContext calls
	// inside participantDone.
	if len(participantIDs) == 0 {
		return false, nil
	}
	for _, participantID := range participantIDs {
		done, err := participantDone(ctx, db, weekID, participantID)
		if err != nil {
			return false, err
		}
		if !done {
			return false, nil
		}
	}
	return true, nil
}

func (h *ResultsHooks) CloseResultsCollection(ctx context.Context, tx *sql.Tx, weekID int64, forced bool) error {
	if forced {
		if err := h.discardAndStrikeIncomplete(ctx, tx, weekID); err != nil {
			return err
		}
	}

	if err := disqualifyOverfamiliarSongs(ctx, tx, weekID); err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{"week_id": weekID})
	if err != nil {
		return fmt.Errorf("contest: marshal publish_results payload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO outbox_actions (action_type, payload_json, status) VALUES (?, ?, 'pending')
	`, OutboxActionPublishResults, string(payload)); err != nil {
		return fmt.Errorf("contest: enqueue publish_results: %w", err)
	}
	return nil
}

// discardAndStrikeIncomplete implements R24/R25/R28 for a forced close:
// any required participant not done gets their partial ranking discarded
// entirely (not partially scored), exactly one strike regardless of which
// part(s) were missing, and a queued private notice (R25).
func (h *ResultsHooks) discardAndStrikeIncomplete(ctx context.Context, tx *sql.Tx, weekID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT participant_id FROM week_participants WHERE week_id = ? AND results_struck = 0
	`, weekID)
	if err != nil {
		return fmt.Errorf("contest: list required participants: %w", err)
	}
	var participantIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("contest: scan participant id: %w", err)
		}
		participantIDs = append(participantIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, participantID := range participantIDs {
		done, err := participantDone(ctx, tx, weekID, participantID)
		if err != nil {
			return err
		}
		if done {
			continue
		}

		if _, err := tx.ExecContext(ctx, `
			DELETE FROM votes WHERE week_id = ? AND voter_id = ?
		`, weekID, participantID); err != nil {
			return fmt.Errorf("contest: discard partial ranking for %d: %w", participantID, err)
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE week_participants SET results_struck = 1 WHERE week_id = ? AND participant_id = ?
		`, weekID, participantID); err != nil {
			return fmt.Errorf("contest: mark results_struck for %d: %w", participantID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE participants SET strikes = strikes + 1 WHERE id = ?
		`, participantID); err != nil {
			return fmt.Errorf("contest: increment strikes for %d: %w", participantID, err)
		}

		payload, err := json.Marshal(map[string]any{"week_id": weekID, "participant_id": participantID})
		if err != nil {
			return fmt.Errorf("contest: marshal partial notice payload: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO outbox_actions (action_type, payload_json, status) VALUES (?, ?, 'pending')
		`, OutboxActionPartialNotice, string(payload)); err != nil {
			return fmt.Errorf("contest: enqueue partial notice for %d: %w", participantID, err)
		}
	}
	return nil
}

// disqualifyOverfamiliarSongs implements R27: a song known by
// disqualifyThreshold (3) or more participants beforehand has its voting
// points zeroed, with no redistribution to other songs, and no further
// penalty to its submitter.
func disqualifyOverfamiliarSongs(ctx context.Context, tx *sql.Tx, weekID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT submission_id, COUNT(*) FROM quiz_answers
		WHERE week_id = ? AND already_knew = 1
		GROUP BY submission_id
		HAVING COUNT(*) >= ?
	`, weekID, disqualifyThreshold)
	if err != nil {
		return fmt.Errorf("contest: find overfamiliar songs: %w", err)
	}
	var submissionIDs []int64
	for rows.Next() {
		var id int64
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			rows.Close()
			return fmt.Errorf("contest: scan overfamiliar song: %w", err)
		}
		submissionIDs = append(submissionIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, id := range submissionIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE votes SET points = 0 WHERE submission_id = ?`, id); err != nil {
			return fmt.Errorf("contest: zero points for disqualified submission %d: %w", id, err)
		}
	}
	return nil
}

// SubmissionResult is one song's final standing for the results
// announcement: its submitter, total points, and familiarity outcome.
type SubmissionResult struct {
	SubmitterName string
	URL           string
	Points        int
	KnownCount    int
	Disqualified  bool
}

// FinalResults computes the per-song results for a week: total points
// (after disqualification zeroing), how many participants already knew
// each song, and the submitter's name (results de-anonymize submitters,
// unlike the songs_collection publication, since points must be
// attributed to someone) (R26).
func FinalResults(ctx context.Context, db *sql.DB, weekID int64) ([]SubmissionResult, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT s.id, s.url, p.display_name,
			COALESCE((SELECT SUM(v.points) FROM votes v WHERE v.submission_id = s.id), 0),
			(SELECT COUNT(*) FROM quiz_answers q WHERE q.submission_id = s.id AND q.already_knew = 1)
		FROM submissions s
		JOIN participants p ON p.id = s.participant_id
		WHERE s.week_id = ?
		ORDER BY s.display_order
	`, weekID)
	if err != nil {
		return nil, fmt.Errorf("contest: query final results: %w", err)
	}
	defer rows.Close()

	var results []SubmissionResult
	for rows.Next() {
		var submissionID int64
		var r SubmissionResult
		if err := rows.Scan(&submissionID, &r.URL, &r.SubmitterName, &r.Points, &r.KnownCount); err != nil {
			return nil, fmt.Errorf("contest: scan final result row: %w", err)
		}
		r.Disqualified = r.KnownCount >= disqualifyThreshold
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(results, func(i, j int) bool { return results[i].Points > results[j].Points })
	return results, nil
}

// RequiredVoteCount is requiredVoteCount, exported for the bot package's
// voting/questionnaire flows to compute a ranking pick's points.
func RequiredVoteCount(ctx context.Context, db *sql.DB, weekID, participantID int64) (int, error) {
	return requiredVoteCount(ctx, db, weekID, participantID)
}

// SubmissionRef identifies one of a week's submissions for building
// questionnaire/ranking prompts, without exposing the submitter (R16's
// anonymity carries through voting; only FinalResults de-anonymizes).
type SubmissionRef struct {
	ID           int64
	DisplayOrder int
	URL          string
}

// PendingQuizSubmissions lists the submissions (other than the
// participant's own) they haven't yet answered the familiarity question
// for, in display order.
func PendingQuizSubmissions(ctx context.Context, db *sql.DB, weekID, participantID int64) ([]SubmissionRef, error) {
	return pendingSubmissions(ctx, db, weekID, participantID, `
		SELECT s.id, s.display_order, s.url FROM submissions s
		WHERE s.week_id = ? AND s.participant_id != ?
		AND s.id NOT IN (
			SELECT submission_id FROM quiz_answers WHERE week_id = ? AND participant_id = ?
		)
		ORDER BY s.display_order
	`)
}

// PendingRankingSubmissions lists the submissions (other than the
// participant's own) they haven't yet ranked, in display order.
func PendingRankingSubmissions(ctx context.Context, db *sql.DB, weekID, participantID int64) ([]SubmissionRef, error) {
	return pendingSubmissions(ctx, db, weekID, participantID, `
		SELECT s.id, s.display_order, s.url FROM submissions s
		WHERE s.week_id = ? AND s.participant_id != ?
		AND s.id NOT IN (
			SELECT submission_id FROM votes WHERE week_id = ? AND voter_id = ?
		)
		ORDER BY s.display_order
	`)
}

func pendingSubmissions(ctx context.Context, db *sql.DB, weekID, participantID int64, query string) ([]SubmissionRef, error) {
	rows, err := db.QueryContext(ctx, query, weekID, participantID, weekID, participantID)
	if err != nil {
		return nil, fmt.Errorf("contest: query pending submissions: %w", err)
	}
	defer rows.Close()

	var refs []SubmissionRef
	for rows.Next() {
		var r SubmissionRef
		if err := rows.Scan(&r.ID, &r.DisplayOrder, &r.URL); err != nil {
			return nil, fmt.Errorf("contest: scan pending submission: %w", err)
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// RecordQuizAnswer stores a participant's familiarity answer for one song.
func RecordQuizAnswer(ctx context.Context, db *sql.DB, weekID, participantID, submissionID int64, alreadyKnew bool) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO quiz_answers (week_id, participant_id, submission_id, already_knew) VALUES (?, ?, ?, ?)
		ON CONFLICT (week_id, participant_id, submission_id) DO UPDATE SET already_knew = excluded.already_knew
	`, weekID, participantID, submissionID, alreadyKnew)
	if err != nil {
		return fmt.Errorf("contest: record quiz answer: %w", err)
	}
	return nil
}

// RecordRankingPick stores a participant's next ranking pick, assigning the
// next sequential rank and its points (R22, R23: 5 down to 1 when ranking 5
// songs, generalized to requiredVoteCount down to 1 for any count).
func RecordRankingPick(ctx context.Context, db *sql.DB, weekID, participantID, submissionID int64) error {
	required, err := requiredVoteCount(ctx, db, weekID, participantID)
	if err != nil {
		return err
	}

	var alreadyRanked int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM votes WHERE week_id = ? AND voter_id = ?
	`, weekID, participantID).Scan(&alreadyRanked); err != nil {
		return fmt.Errorf("contest: count existing votes: %w", err)
	}

	rank := alreadyRanked + 1
	points := required - rank + 1
	if points < 0 {
		points = 0
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO votes (week_id, voter_id, submission_id, rank, points) VALUES (?, ?, ?, ?, ?)
	`, weekID, participantID, submissionID, rank, points); err != nil {
		return fmt.Errorf("contest: record ranking pick: %w", err)
	}
	return nil
}

// PublishDueResults processes pending publish_results outbox rows: building
// the final per-song/per-participant points and familiarity announcement
// (R26) and posting it to the group.
func PublishDueResults(ctx context.Context, db *sql.DB, notifier ResultsNotifier) error {
	type pending struct {
		id      int64
		payload string
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, payload_json FROM outbox_actions WHERE action_type = ? AND status IN ('pending', 'in_progress')
	`, OutboxActionPublishResults)
	if err != nil {
		return fmt.Errorf("contest: list pending publish_results actions: %w", err)
	}
	var actions []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.payload); err != nil {
			rows.Close()
			return fmt.Errorf("contest: scan publish_results action: %w", err)
		}
		actions = append(actions, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, a := range actions {
		if err := processPublishResultsAction(ctx, db, notifier, a.id, a.payload); err != nil {
			return err
		}
	}
	return nil
}

func processPublishResultsAction(ctx context.Context, db *sql.DB, notifier ResultsNotifier, actionID int64, payloadJSON string) error {
	var payload struct {
		WeekID int64 `json:"week_id"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return fmt.Errorf("contest: unmarshal publish_results payload: %w", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE outbox_actions SET status = 'in_progress' WHERE id = ?`, actionID); err != nil {
		return fmt.Errorf("contest: mark publish_results in_progress: %w", err)
	}

	results, err := FinalResults(ctx, db, payload.WeekID)
	if err != nil {
		return err
	}

	text := "Results are in!\n"
	for i, r := range results {
		if r.Disqualified {
			text += fmt.Sprintf("%d. %s (%s) -- disqualified, known by %d participants beforehand\n", i+1, r.SubmitterName, r.URL, r.KnownCount)
			continue
		}
		text += fmt.Sprintf("%d. %s (%s) -- %d points, known by %d beforehand\n", i+1, r.SubmitterName, r.URL, r.Points, r.KnownCount)
	}

	if err := notifier.SendGroupMessage(ctx, text); err != nil {
		return fmt.Errorf("contest: send publish_results message: %w", err)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE outbox_actions SET status = 'done', completed_at = datetime('now') WHERE id = ?
	`, actionID); err != nil {
		return fmt.Errorf("contest: mark publish_results done: %w", err)
	}
	return nil
}

// ProcessPartialNotices processes pending results_partial_notice outbox
// rows: privately telling a participant their incomplete ranking/
// questionnaire wasn't counted after a forced close (R25).
func ProcessPartialNotices(ctx context.Context, db *sql.DB, notifier ResultsNotifier) error {
	type pending struct {
		id      int64
		payload string
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, payload_json FROM outbox_actions WHERE action_type = ? AND status IN ('pending', 'in_progress')
	`, OutboxActionPartialNotice)
	if err != nil {
		return fmt.Errorf("contest: list pending partial notices: %w", err)
	}
	var actions []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.payload); err != nil {
			rows.Close()
			return fmt.Errorf("contest: scan partial notice action: %w", err)
		}
		actions = append(actions, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, a := range actions {
		if err := processPartialNoticeAction(ctx, db, notifier, a.id, a.payload); err != nil {
			return err
		}
	}
	return nil
}

func processPartialNoticeAction(ctx context.Context, db *sql.DB, notifier ResultsNotifier, actionID int64, payloadJSON string) error {
	var payload struct {
		WeekID        int64 `json:"week_id"`
		ParticipantID int64 `json:"participant_id"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return fmt.Errorf("contest: unmarshal partial notice payload: %w", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE outbox_actions SET status = 'in_progress' WHERE id = ?`, actionID); err != nil {
		return fmt.Errorf("contest: mark partial notice in_progress: %w", err)
	}

	var telegramUserID int64
	if err := db.QueryRowContext(ctx, `
		SELECT telegram_user_id FROM participants WHERE id = ?
	`, payload.ParticipantID).Scan(&telegramUserID); err != nil {
		return fmt.Errorf("contest: look up participant %d: %w", payload.ParticipantID, err)
	}

	text := "The results window closed before you finished voting and/or the questionnaire. Your partial progress wasn't counted, but you did receive a strike."
	if err := notifier.SendPrivateMessage(ctx, telegramUserID, text); err != nil {
		return fmt.Errorf("contest: send partial notice: %w", err)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE outbox_actions SET status = 'done', completed_at = datetime('now') WHERE id = ?
	`, actionID); err != nil {
		return fmt.Errorf("contest: mark partial notice done: %w", err)
	}
	return nil
}
