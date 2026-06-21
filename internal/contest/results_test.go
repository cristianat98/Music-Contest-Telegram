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

	var firstPoints, secondPoints int
	db.QueryRow("SELECT points FROM votes WHERE week_id = ? AND voter_id = ? AND submission_id = ?", weekID, ids[0], pending[0].ID).Scan(&firstPoints)
	db.QueryRow("SELECT points FROM votes WHERE week_id = ? AND voter_id = ? AND submission_id = ?", weekID, ids[0], pending[1].ID).Scan(&secondPoints)

	if firstPoints != 2 {
		t.Errorf("first pick points = %d, want 2 (top of a 2-song ranking)", firstPoints)
	}
	if secondPoints != 1 {
		t.Errorf("second pick points = %d, want 1", secondPoints)
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

	for _, strugglerID := range []int64{ids[1], ids[2]} {
		var strikes, voteCount int
		db.QueryRow("SELECT strikes FROM participants WHERE id = ?", strugglerID).Scan(&strikes)
		db.QueryRow("SELECT COUNT(*) FROM votes WHERE week_id = ? AND voter_id = ?", weekID, strugglerID).Scan(&voteCount)

		if strikes != 1 {
			t.Errorf("participant %d strikes = %d, want exactly 1 (R24: one strike regardless of which parts missing)", strugglerID, strikes)
		}
		if voteCount != 0 {
			t.Errorf("participant %d has %d leftover votes, want 0 (R24: partial ranking discarded entirely)", strugglerID, voteCount)
		}
	}

	var completerStrikes int
	db.QueryRow("SELECT strikes FROM participants WHERE id = ?", ids[0]).Scan(&completerStrikes)
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

func (f *fakeNotifier) SendPrivateMessage(ctx context.Context, telegramUserID int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, text)
	return nil
}
