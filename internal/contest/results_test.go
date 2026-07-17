package contest

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

// setupResultsCollectionWeek starts a contest/week with n participants, has
// each submit a song, then force-advances into results_collection. Returns
// the engine, db, week ID, and participant IDs in insertion order.
func setupResultsCollectionWeek(t *testing.T, n int) (*Engine, *sql.DB, int64, []int64) {
	t.Helper()
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, n)
	seedTopic(t, db, "topic-a")
	e.SetSongsHooks(NewSongsHooks(db))
	e.SetResultsHooks(NewResultsHooks(db))

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	week, err := e.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}

	rows, _ := db.Query("SELECT id FROM participants ORDER BY id")
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	for i, id := range ids {
		seedSubmission(t, db, week.ID, id, fmt.Sprintf("https://youtu.be/song%d", i))
	}

	if _, err := e.ForceAdvance(ctx); err != nil {
		t.Fatalf("ForceAdvance() to results_collection error = %v", err)
	}

	got, err := e.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}
	if got.State != StateResultsCollection {
		t.Fatalf("week state = %q, want %q", got.State, StateResultsCollection)
	}

	return e, db, week.ID, ids
}

func answerAllQuizzes(t *testing.T, ctx context.Context, db *sql.DB, weekID, participantID int64, knew bool) {
	t.Helper()
	pending, err := PendingQuizSubmissions(ctx, db, weekID, participantID)
	if err != nil {
		t.Fatalf("PendingQuizSubmissions() error = %v", err)
	}
	for _, sub := range pending {
		if err := RecordQuizAnswer(ctx, db, weekID, participantID, sub.ID, knew); err != nil {
			t.Fatalf("RecordQuizAnswer() error = %v", err)
		}
	}
}

func rankAllRemaining(t *testing.T, ctx context.Context, db *sql.DB, weekID, participantID int64) {
	t.Helper()
	for {
		pending, err := PendingRankingSubmissions(ctx, db, weekID, participantID)
		if err != nil {
			t.Fatalf("PendingRankingSubmissions() error = %v", err)
		}
		if len(pending) == 0 {
			return
		}
		if err := RecordRankingPick(ctx, db, weekID, participantID, pending[0].ID); err != nil {
			t.Fatalf("RecordRankingPick() error = %v", err)
		}
	}
}

func TestRequiredVoteCount_ExcludesOwnSubmission(t *testing.T) {
	_, db, weekID, ids := setupResultsCollectionWeek(t, 3)
	ctx := context.Background()

	count, err := RequiredVoteCount(ctx, db, weekID, ids[0])
	if err != nil {
		t.Fatalf("RequiredVoteCount() error = %v", err)
	}
	if count != 2 {
		t.Errorf("RequiredVoteCount() = %d, want 2 (3 submissions minus own)", count)
	}
}

func TestRankingAndScoring_PointsDescendingByRank(t *testing.T) {
	_, db, weekID, ids := setupResultsCollectionWeek(t, 3)
	ctx := context.Background()

	// Participant 0 ranks: first pick = most preferred = highest points.
	pending, err := PendingRankingSubmissions(ctx, db, weekID, ids[0])
	if err != nil {
		t.Fatalf("PendingRankingSubmissions() error = %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending ranking count = %d, want 2", len(pending))
	}

	if err := RecordRankingPick(ctx, db, weekID, ids[0], pending[0].ID); err != nil {
		t.Fatalf("RecordRankingPick() #1 error = %v", err)
	}
	if err := RecordRankingPick(ctx, db, weekID, ids[0], pending[1].ID); err != nil {
		t.Fatalf("RecordRankingPick() #2 error = %v", err)
	}

	results, err := FinalResults(ctx, db, weekID)
	if err != nil {
		t.Fatalf("FinalResults() error = %v", err)
	}
	pointsByURL := make(map[string]int)
	for _, r := range results {
		pointsByURL[r.URL] = r.Points
	}

	var firstURL, secondURL string
	db.QueryRow("SELECT url FROM submissions WHERE id = ?", pending[0].ID).Scan(&firstURL)
	db.QueryRow("SELECT url FROM submissions WHERE id = ?", pending[1].ID).Scan(&secondURL)

	if pointsByURL[firstURL] != 2 {
		t.Errorf("first pick points = %d, want 2 (top of a 2-song ranking)", pointsByURL[firstURL])
	}
	if pointsByURL[secondURL] != 1 {
		t.Errorf("second pick points = %d, want 1", pointsByURL[secondURL])
	}
}

func TestRankingExcludesOwnSong(t *testing.T) {
	_, db, weekID, ids := setupResultsCollectionWeek(t, 3)
	ctx := context.Background()

	pending, err := PendingRankingSubmissions(ctx, db, weekID, ids[0])
	if err != nil {
		t.Fatalf("PendingRankingSubmissions() error = %v", err)
	}
	for _, p := range pending {
		var ownerID int64
		db.QueryRow("SELECT participant_id FROM submissions WHERE id = ?", p.ID).Scan(&ownerID)
		if ownerID == ids[0] {
			t.Errorf("own submission %d appeared in ranking list", p.ID)
		}
	}
}

func TestDisqualification_KnownByThreeOrMore(t *testing.T) {
	e, db, weekID, ids := setupResultsCollectionWeek(t, 5)
	ctx := context.Background()

	var targetSubmission int64
	db.QueryRow("SELECT id FROM submissions WHERE week_id = ? AND participant_id = ?", weekID, ids[0]).Scan(&targetSubmission)

	// 3 of the other 4 participants say they already knew ids[0]'s song.
	knowers := []int64{ids[1], ids[2], ids[3]}
	for _, p := range knowers {
		if err := RecordQuizAnswer(ctx, db, weekID, p, targetSubmission, true); err != nil {
			t.Fatalf("RecordQuizAnswer() error = %v", err)
		}
	}
	// Everyone answers their remaining quizzes and ranks fully so the week
	// can naturally close.
	for _, p := range ids {
		answerAllQuizzes(t, ctx, db, weekID, p, false)
		rankAllRemaining(t, ctx, db, weekID, p)
	}

	if _, err := e.ForceAdvance(ctx); err != nil { // results_collection -> idle
		t.Fatalf("ForceAdvance() error = %v", err)
	}

	results, err := FinalResults(ctx, db, weekID)
	if err != nil {
		t.Fatalf("FinalResults() error = %v", err)
	}

	var target *SubmissionResult
	for i := range results {
		var ownerID int64
		// Match by submitter name lookup isn't unique enough; re-derive via DB.
		db.QueryRow("SELECT participant_id FROM submissions WHERE url = ?", results[i].URL).Scan(&ownerID)
		if ownerID == ids[0] {
			target = &results[i]
		}
	}
	if target == nil {
		t.Fatal("could not find target submission in final results")
	}
	if !target.Disqualified {
		t.Error("expected song known by 3+ participants to be disqualified")
	}
	if target.Points != 0 {
		t.Errorf("disqualified song points = %d, want 0 (no redistribution)", target.Points)
	}
}

func TestForcedClose_DiscardsPartialRankingAndStrikesOnce(t *testing.T) {
	e, db, weekID, ids := setupResultsCollectionWeek(t, 3)
	ctx := context.Background()

	// ids[0] completes everything; ids[1] only half-ranks and never
	// answers the questionnaire; ids[2] does nothing.
	answerAllQuizzes(t, ctx, db, weekID, ids[0], false)
	rankAllRemaining(t, ctx, db, weekID, ids[0])

	pending, _ := PendingRankingSubmissions(ctx, db, weekID, ids[1])
	if len(pending) > 0 {
		if err := RecordRankingPick(ctx, db, weekID, ids[1], pending[0].ID); err != nil {
			t.Fatalf("partial RecordRankingPick() error = %v", err)
		}
	}

	if _, err := e.ForceAdvance(ctx); err != nil {
		t.Fatalf("ForceAdvance() error = %v", err)
	}

	var contestID int64
	if err := db.QueryRow("SELECT id FROM contests WHERE active = 1").Scan(&contestID); err != nil {
		t.Fatalf("query active contest: %v", err)
	}

	for _, strugglerID := range []int64{ids[1], ids[2]} {
		var voteCount int
		db.QueryRow("SELECT COUNT(*) FROM votes WHERE week_id = ? AND voter_id = ?", weekID, strugglerID).Scan(&voteCount)

		strikes, err := StrikesForParticipant(ctx, db, contestID, strugglerID)
		if err != nil {
			t.Fatalf("StrikesForParticipant() error = %v", err)
		}
		if strikes != 1 {
			t.Errorf("participant %d strikes = %d, want exactly 1 (R24: one strike regardless of which parts missing)", strugglerID, strikes)
		}
		if voteCount != 0 {
			t.Errorf("participant %d has %d leftover votes, want 0 (R24: partial ranking discarded entirely)", strugglerID, voteCount)
		}
	}

	completerStrikes, err := StrikesForParticipant(ctx, db, contestID, ids[0])
	if err != nil {
		t.Fatalf("StrikesForParticipant() error = %v", err)
	}
	if completerStrikes != 0 {
		t.Errorf("completer strikes = %d, want 0", completerStrikes)
	}

	var noticeCount int
	db.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ? AND status = 'pending'", OutboxActionPartialNotice,
	).Scan(&noticeCount)
	if noticeCount != 2 {
		t.Errorf("pending partial notices = %d, want 2", noticeCount)
	}
}

func TestFinalResults_DepartedSubmitterZeroedAndMarked(t *testing.T) {
	e, db, weekID, ids := setupResultsCollectionWeek(t, 3)
	ctx := context.Background()

	var contestID int64
	db.QueryRow("SELECT id FROM contests WHERE active = 1").Scan(&contestID)

	// ids[1] departs the contest after submitting and being ranked by others,
	// but before results close.
	if _, err := db.Exec(
		"UPDATE contest_participants SET left_at = datetime('now') WHERE contest_id = ? AND participant_id = ?",
		contestID, ids[1],
	); err != nil {
		t.Fatalf("mark participant departed: %v", err)
	}

	// Only the still-active participants (ids[0], ids[2]) are required to
	// finish for a natural close.
	for _, p := range []int64{ids[0], ids[2]} {
		answerAllQuizzes(t, ctx, db, weekID, p, false)
		rankAllRemaining(t, ctx, db, weekID, p)
	}

	hooks := NewResultsHooks(db)
	complete, err := hooks.ResultsCollectionComplete(ctx, db, weekID)
	if err != nil {
		t.Fatalf("ResultsCollectionComplete() error = %v", err)
	}
	if !complete {
		t.Fatal("expected complete: the departed participant should not block natural close")
	}

	if _, err := e.ForceAdvance(ctx); err != nil {
		t.Fatalf("ForceAdvance() error = %v", err)
	}

	results, err := FinalResults(ctx, db, weekID)
	if err != nil {
		t.Fatalf("FinalResults() error = %v", err)
	}

	var departedResult *SubmissionResult
	for i := range results {
		var ownerID int64
		db.QueryRow("SELECT participant_id FROM submissions WHERE url = ?", results[i].URL).Scan(&ownerID)
		if ownerID == ids[1] {
			departedResult = &results[i]
		}
	}
	if departedResult == nil {
		t.Fatal("could not find departed participant's submission in final results")
	}
	if !departedResult.Departed {
		t.Error("expected departed submitter's result to be marked Departed")
	}
	if departedResult.Points != 0 {
		t.Errorf("departed submitter's points = %d, want 0", departedResult.Points)
	}
}

func TestResultsCollectionComplete_NaturalCloseOnceAllDone(t *testing.T) {
	_, db, weekID, ids := setupResultsCollectionWeek(t, 2)
	ctx := context.Background()
	hooks := NewResultsHooks(db)

	complete, err := hooks.ResultsCollectionComplete(ctx, db, weekID)
	if err != nil {
		t.Fatalf("ResultsCollectionComplete() error = %v", err)
	}
	if complete {
		t.Error("expected incomplete before anyone has answered")
	}

	for _, p := range ids {
		answerAllQuizzes(t, ctx, db, weekID, p, false)
		rankAllRemaining(t, ctx, db, weekID, p)
	}

	complete, err = hooks.ResultsCollectionComplete(ctx, db, weekID)
	if err != nil {
		t.Fatalf("ResultsCollectionComplete() error = %v", err)
	}
	if !complete {
		t.Error("expected complete once everyone answered and ranked")
	}
}

func TestPublishDueResults_SendsAndMarksDone(t *testing.T) {
	e, db, weekID, ids := setupResultsCollectionWeek(t, 2)
	ctx := context.Background()

	for _, p := range ids {
		answerAllQuizzes(t, ctx, db, weekID, p, false)
		rankAllRemaining(t, ctx, db, weekID, p)
	}

	if _, err := e.ForceAdvance(ctx); err != nil {
		t.Fatalf("ForceAdvance() error = %v", err)
	}

	notifier := &fakeNotifier{}
	if err := PublishDueResults(ctx, db, notifier); err != nil {
		t.Fatalf("PublishDueResults() error = %v", err)
	}
	if len(notifier.messages) != 1 {
		t.Fatalf("messages sent = %d, want 1", len(notifier.messages))
	}

	var status string
	db.QueryRow("SELECT status FROM outbox_actions WHERE action_type = ?", OutboxActionPublishResults).Scan(&status)
	if status != "done" {
		t.Errorf("outbox status = %q, want done", status)
	}
}

func TestProcessPartialNotices_SendsAndMarksDone(t *testing.T) {
	e, db, weekID, ids := setupResultsCollectionWeek(t, 3)
	ctx := context.Background()

	answerAllQuizzes(t, ctx, db, weekID, ids[0], false)
	rankAllRemaining(t, ctx, db, weekID, ids[0])
	// ids[1] and ids[2] leave their ranking/questionnaire incomplete.

	if _, err := e.ForceAdvance(ctx); err != nil {
		t.Fatalf("ForceAdvance() error = %v", err)
	}

	notifier := &fakeNotifier{}
	if err := ProcessPartialNotices(ctx, db, notifier); err != nil {
		t.Fatalf("ProcessPartialNotices() error = %v", err)
	}

	if len(notifier.messages) != 2 {
		t.Fatalf("messages sent = %d, want 2", len(notifier.messages))
	}

	var doneCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ? AND status = 'done'", OutboxActionPartialNotice,
	).Scan(&doneCount); err != nil {
		t.Fatalf("count done partial notices: %v", err)
	}
	if doneCount != 2 {
		t.Errorf("done partial notices = %d, want 2", doneCount)
	}
}

func TestOpenResultsCollection_TxError(t *testing.T) {
	_, db := openTestEngine(t)
	if err := (&ResultsHooks{}).OpenResultsCollection(context.Background(), committedTx(t, db), 1); err == nil {
		t.Error("OpenResultsCollection() error = nil, want an error from the finalized tx")
	}
}

func TestCloseResultsCollection_TxError(t *testing.T) {
	_, db := openTestEngine(t)
	ctx := context.Background()

	if err := (&ResultsHooks{}).CloseResultsCollection(ctx, committedTx(t, db), 1, true); err == nil {
		t.Error("CloseResultsCollection(forced) error = nil, want an error from the finalized tx")
	}
	if err := (&ResultsHooks{}).CloseResultsCollection(ctx, committedTx(t, db), 1, false); err == nil {
		t.Error("CloseResultsCollection(unforced) error = nil, want an error from the finalized tx")
	}
}

func TestResultsCollectionComplete_DBError(t *testing.T) {
	_, db := openTestEngine(t)
	db.Close()
	if _, err := (&ResultsHooks{}).ResultsCollectionComplete(context.Background(), db, 1); err == nil {
		t.Error("ResultsCollectionComplete() error = nil, want an error from the closed DB")
	}
}

func TestRecordQuizAnswer_DBError(t *testing.T) {
	_, db := openTestEngine(t)
	db.Close()
	if err := RecordQuizAnswer(context.Background(), db, 1, 1, 1, false); err == nil {
		t.Error("RecordQuizAnswer() error = nil, want an error from the closed DB")
	}
}

func TestRecordRankingPick_DBError(t *testing.T) {
	_, db := openTestEngine(t)
	db.Close()
	if err := RecordRankingPick(context.Background(), db, 1, 1, 1); err == nil {
		t.Error("RecordRankingPick() error = nil, want an error from the closed DB")
	}
}

func TestPublishDueResults_DBError(t *testing.T) {
	_, db := openTestEngine(t)
	db.Close()
	if err := PublishDueResults(context.Background(), db, &fakeNotifier{}); err == nil {
		t.Error("PublishDueResults() error = nil, want an error from the closed DB")
	}
}

func TestProcessPartialNotices_DBError(t *testing.T) {
	_, db := openTestEngine(t)
	db.Close()
	if err := ProcessPartialNotices(context.Background(), db, &fakeNotifier{}); err == nil {
		t.Error("ProcessPartialNotices() error = nil, want an error from the closed DB")
	}
}

func (f *fakeNotifier) SendPrivateMessage(ctx context.Context, telegramUserID int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, text)
	return nil
}

func TestParticipantDone_DBError(t *testing.T) {
	_, db := openTestEngine(t)
	db.Close()
	if _, err := participantDone(context.Background(), db, 1, 1); err == nil {
		t.Error("participantDone() error = nil, want an error from the closed DB")
	}
}

func TestProcessPublishResultsAction_DBError(t *testing.T) {
	_, db := openTestEngine(t)
	db.Close()
	if err := processPublishResultsAction(context.Background(), db, &fakeNotifier{}, 1, `{"week_id":1}`); err == nil {
		t.Error("processPublishResultsAction() error = nil, want an error from the closed DB")
	}
}

func TestProcessPartialNoticeAction_DBError(t *testing.T) {
	_, db := openTestEngine(t)
	db.Close()
	if err := processPartialNoticeAction(context.Background(), db, &fakeNotifier{}, 1, `{"week_id":1,"participant_id":1}`); err == nil {
		t.Error("processPartialNoticeAction() error = nil, want an error from the closed DB")
	}
}

func TestDiscardAndStrikeParticipant_TxError(t *testing.T) {
	_, db := openTestEngine(t)
	if err := discardParticipant(context.Background(), committedTx(t, db), 1, 1); err == nil {
		t.Error("discardParticipant() error = nil, want an error from the finalized tx")
	}
}

func TestSubmissionPoints_DBError(t *testing.T) {
	_, db := openTestEngine(t)
	db.Close()
	if _, err := submissionPoints(context.Background(), db, 1); err == nil {
		t.Error("submissionPoints() error = nil, want an error from the closed DB")
	}
}
