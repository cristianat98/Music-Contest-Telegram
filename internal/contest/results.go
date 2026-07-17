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

	errFmtListRequiredParticipants = "contest: list required participants: %w"
	errFmtScanParticipantID        = "contest: scan participant id: %w"
)

// collectParticipantIDs runs query (expected to select a single int64
// participant_id column) and drains it fully before returning, satisfying
// the single-connection constraint (KTD2) that callers issuing nested
// per-participant queries afterwards rely on.
func collectParticipantIDs(ctx context.Context, q querier, query string, args ...any) ([]int64, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf(errFmtListRequiredParticipants, err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf(errFmtScanParticipantID, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ResultsNotifier lets this package post the final results announcement to
// the group and a partial-progress notice to a single participant (R25),
// without depending on go-telegram/bot.
type ResultsNotifier interface {
	GroupNotifier
	SendPrivateMessage(ctx context.Context, telegramUserID int64, text string) error
}

// ResultsHooks implements ResultsCollectionHooks: dispatching the initial
// questionnaire/ranking prompts (R21), computing completion (R24),
// discarding-and-notifying incomplete sessions on a forced close (R24, R25),
// and striking anyone not done (R28). Scoring and disqualification (R23,
// R27) aren't a close-time side effect here -- FinalResults derives them at
// read time instead.
type ResultsHooks struct {
	db *sql.DB
}

func NewResultsHooks(db *sql.DB) *ResultsHooks {
	return &ResultsHooks{db: db}
}

func (h *ResultsHooks) OpenResultsCollection(ctx context.Context, tx *sql.Tx, weekID int64) error {
	participantIDs, err := collectParticipantIDs(ctx, tx, `
		SELECT cp.participant_id FROM contest_participants cp
		JOIN weeks w ON w.contest_id = cp.contest_id
		WHERE w.id = ? AND cp.left_at IS NULL
	`, weekID)
	if err != nil {
		return err
	}

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
	// collectParticipantIDs fully drains and closes the outer query before
	// returning: SetMaxOpenConns(1) (KTD2) means only one connection exists
	// at all, so an open, mid-iteration *Rows would otherwise deadlock
	// against the nested QueryRowContext calls inside participantDone below.
	participantIDs, err := collectParticipantIDs(ctx, db, `
		SELECT cp.participant_id FROM contest_participants cp
		JOIN weeks w ON w.contest_id = cp.contest_id
		WHERE w.id = ? AND cp.left_at IS NULL
	`, weekID)
	if err != nil {
		return false, err
	}

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
		if err := h.discardIncomplete(ctx, tx, weekID); err != nil {
			return err
		}
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

// discardIncomplete implements R24/R25/R28 for a forced close: any
// obligated participant not done gets their partial ranking discarded
// entirely (not partially scored) and a queued private notice (R25). No
// guard flag or strike increment is needed here: deleting an incomplete
// participant's votes is naturally idempotent (deleting zero rows twice is
// a no-op), and the missed-results strike itself is a permanent, computable
// fact once the week closes (R7-R9; see StrikesForParticipant).
func (h *ResultsHooks) discardIncomplete(ctx context.Context, tx *sql.Tx, weekID int64) error {
	participantIDs, err := collectParticipantIDs(ctx, tx, `
		SELECT cp.participant_id FROM contest_participants cp
		JOIN weeks w ON w.contest_id = cp.contest_id
		WHERE w.id = ? AND cp.left_at IS NULL
	`, weekID)
	if err != nil {
		return err
	}

	for _, participantID := range participantIDs {
		done, err := participantDone(ctx, tx, weekID, participantID)
		if err != nil {
			return err
		}
		if done {
			continue
		}
		if err := discardParticipant(ctx, tx, weekID, participantID); err != nil {
			return err
		}
	}
	return nil
}

// discardParticipant discards one participant's partial ranking
// and queues their partial-progress notice (R24, R25), for a single
// participant found incomplete by discardIncomplete.
func discardParticipant(ctx context.Context, tx *sql.Tx, weekID, participantID int64) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM votes WHERE week_id = ? AND voter_id = ?
	`, weekID, participantID); err != nil {
		return fmt.Errorf("contest: discard partial ranking for %d: %w", participantID, err)
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
	return nil
}

// SubmissionResult is one song's final standing for the results
// announcement: its submitter, total points, familiarity outcome, and
// whether the submitter had no obligations in the contest by the time
// results were computed (R5) -- a separate reason from Disqualified, since
// the two aren't mutually exclusive.
type SubmissionResult struct {
	SubmitterName string
	URL           string
	Points        int
	KnownCount    int
	Disqualified  bool
	Departed      bool
}

// FinalResults computes the per-song results for a week: total points
// (derived from every vote cast for a song, zero for a disqualified or
// departed submitter's song instead), how many participants already knew
// each song, the submitter's name (results de-anonymize submitters, unlike
// the songs_collection publication, since points must be attributed to
// someone), and whether the submitter has since departed the contest
// (R5, R26, R27).
func FinalResults(ctx context.Context, db *sql.DB, weekID int64) ([]SubmissionResult, error) {
	points, err := submissionPoints(ctx, db, weekID)
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, `
		SELECT s.id, s.url, p.display_name,
			(SELECT COUNT(*) FROM quiz_answers q WHERE q.submission_id = s.id AND q.already_knew = 1),
			CASE WHEN cp.left_at IS NULL THEN 0 ELSE 1 END
		FROM submissions s
		JOIN participants p ON p.id = s.participant_id
		JOIN weeks w ON w.id = s.week_id
		LEFT JOIN contest_participants cp ON cp.contest_id = w.contest_id AND cp.participant_id = s.participant_id
		WHERE s.week_id = ?
		ORDER BY s.display_name
	`, weekID)
	if err != nil {
		return nil, fmt.Errorf("contest: query final results: %w", err)
	}
	defer rows.Close()

	var results []SubmissionResult
	for rows.Next() {
		var submissionID int64
		var r SubmissionResult
		if err := rows.Scan(&submissionID, &r.URL, &r.SubmitterName, &r.KnownCount, &r.Departed); err != nil {
			return nil, fmt.Errorf("contest: scan final result row: %w", err)
		}
		r.Disqualified = r.KnownCount >= disqualifyThreshold
		if !r.Disqualified && !r.Departed {
			r.Points = points[submissionID]
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(results, func(i, j int) bool { return results[i].Points > results[j].Points })
	return results, nil
}

// submissionPoints computes each submission's total points for a week by
// summing every vote cast for it, converting each vote's rank into points
// via that voter's own required-ranking count (R22, R23: required down to
// 1). Points are derived here rather than stored on votes, since they're a
// pure function of rank plus data (submissions) that already exists --
// storing them would just duplicate the same fact (see votes' schema
// comment). Computes the same "total submissions, minus one if the voter
// has their own" required-count formula as requiredVoteCount, but inline
// against a pre-fetched submitters map rather than calling it per vote --
// requiredVoteCount is per-participant (used by participantDone's
// completion check) and would mean one query per vote row here instead of
// one query for the whole week.
func submissionPoints(ctx context.Context, db *sql.DB, weekID int64) (map[int64]int, error) {
	submitters := make(map[int64]bool)
	subRows, err := db.QueryContext(ctx, `SELECT participant_id FROM submissions WHERE week_id = ?`, weekID)
	if err != nil {
		return nil, fmt.Errorf("contest: list submitters for scoring: %w", err)
	}
	for subRows.Next() {
		var participantID int64
		if err := subRows.Scan(&participantID); err != nil {
			subRows.Close()
			return nil, fmt.Errorf("contest: scan submitter for scoring: %w", err)
		}
		submitters[participantID] = true
	}
	if err := subRows.Err(); err != nil {
		subRows.Close()
		return nil, err
	}
	subRows.Close()
	// submissions has UNIQUE(week_id, participant_id), so this count is the
	// same total requiredVoteCount would query separately.
	total := len(submitters)

	rows, err := db.QueryContext(ctx, `SELECT voter_id, submission_id, rank FROM votes WHERE week_id = ?`, weekID)
	if err != nil {
		return nil, fmt.Errorf("contest: list votes for scoring: %w", err)
	}
	defer rows.Close()

	points := make(map[int64]int)
	for rows.Next() {
		var voterID, submissionID int64
		var rank int
		if err := rows.Scan(&voterID, &submissionID, &rank); err != nil {
			return nil, fmt.Errorf("contest: scan vote for scoring: %w", err)
		}
		required := total
		if submitters[voterID] {
			required--
		}
		votePoints := required - rank + 1
		if votePoints < 0 {
			votePoints = 0
		}
		points[submissionID] += votePoints
	}
	return points, rows.Err()
}

// RequiredVoteCount is requiredVoteCount, exported for tests.
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
		SELECT s.id, s.display_name, s.url FROM submissions s
		WHERE s.week_id = ? AND s.participant_id != ?
		AND s.id NOT IN (
			SELECT submission_id FROM quiz_answers WHERE week_id = ? AND participant_id = ?
		)
		ORDER BY s.display_name
	`)
}

// PendingRankingSubmissions lists the submissions (other than the
// participant's own) they haven't yet ranked, in display order.
func PendingRankingSubmissions(ctx context.Context, db *sql.DB, weekID, participantID int64) ([]SubmissionRef, error) {
	return pendingSubmissions(ctx, db, weekID, participantID, `
		SELECT s.id, s.display_name, s.url FROM submissions s
		WHERE s.week_id = ? AND s.participant_id != ?
		AND s.id NOT IN (
			SELECT submission_id FROM votes WHERE week_id = ? AND voter_id = ?
		)
		ORDER BY s.display_name
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
// next sequential rank (R22). Its points aren't computed here: submissionPoints
// derives them from rank at read time instead.
func RecordRankingPick(ctx context.Context, db *sql.DB, weekID, participantID, submissionID int64) error {
	var alreadyRanked int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM votes WHERE week_id = ? AND voter_id = ?
	`, weekID, participantID).Scan(&alreadyRanked); err != nil {
		return fmt.Errorf("contest: count existing votes: %w", err)
	}

	rank := alreadyRanked + 1

	if _, err := db.ExecContext(ctx, `
		INSERT INTO votes (week_id, voter_id, submission_id, rank) VALUES (?, ?, ?, ?)
	`, weekID, participantID, submissionID, rank); err != nil {
		return fmt.Errorf("contest: record ranking pick: %w", err)
	}
	return nil
}

// PublishDueResults processes pending publish_results outbox rows: building
// the final per-song/per-participant points and familiarity announcement
// (R26) and posting it to the group.
func PublishDueResults(ctx context.Context, db *sql.DB, notifier ResultsNotifier) error {
	actions, err := ListPendingOutboxActions(ctx, db, OutboxActionPublishResults)
	if err != nil {
		return err
	}

	for _, a := range actions {
		if err := processPublishResultsAction(ctx, db, notifier, a.ID, a.Payload); err != nil {
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

	if err := markOutboxInProgress(ctx, db, actionID); err != nil {
		return err
	}

	results, err := FinalResults(ctx, db, payload.WeekID)
	if err != nil {
		return err
	}

	text := "Results are in!\n"
	for i, r := range results {
		switch {
		case r.Disqualified && r.Departed:
			text += fmt.Sprintf("%d. %s (%s) -- disqualified, known by %d participants beforehand; submitter also left the contest\n", i+1, r.SubmitterName, r.URL, r.KnownCount)
		case r.Disqualified:
			text += fmt.Sprintf("%d. %s (%s) -- disqualified, known by %d participants beforehand\n", i+1, r.SubmitterName, r.URL, r.KnownCount)
		case r.Departed:
			text += fmt.Sprintf("%d. %s (%s) -- 0 points, submitter left the contest\n", i+1, r.SubmitterName, r.URL)
		default:
			text += fmt.Sprintf("%d. %s (%s) -- %d points, known by %d beforehand\n", i+1, r.SubmitterName, r.URL, r.Points, r.KnownCount)
		}
	}

	if err := notifier.SendGroupMessage(ctx, text); err != nil {
		return fmt.Errorf("contest: send publish_results message: %w", err)
	}

	return markOutboxDone(ctx, db, actionID)
}

// ProcessPartialNotices processes pending results_partial_notice outbox
// rows: privately telling a participant their incomplete ranking/
// questionnaire wasn't counted after a forced close (R25).
func ProcessPartialNotices(ctx context.Context, db *sql.DB, notifier ResultsNotifier) error {
	actions, err := ListPendingOutboxActions(ctx, db, OutboxActionPartialNotice)
	if err != nil {
		return err
	}

	for _, a := range actions {
		if err := processPartialNoticeAction(ctx, db, notifier, a.ID, a.Payload); err != nil {
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

	if err := markOutboxInProgress(ctx, db, actionID); err != nil {
		return err
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

	return markOutboxDone(ctx, db, actionID)
}
