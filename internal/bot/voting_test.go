package bot

import (
	"context"
	"testing"

	"github.com/cristianat98/Music-Contest-Telegram/internal/contest"
)

func TestHandleRankingPickCallback_RecordsAndAdvancesToCompletion(t *testing.T) {
	app, weekID, participantIDs := setupResultsCollectionApp(t, 2001, 2002)
	ctx := context.Background()

	// Answer the questionnaire first so ranking is reachable, matching the
	// real flow's ordering (questionnaire before ranking, R21).
	pendingQuiz, _ := contest.PendingQuizSubmissions(ctx, app.DB, weekID, participantIDs[0])
	for _, sub := range pendingQuiz {
		if err := contest.RecordQuizAnswer(ctx, app.DB, weekID, participantIDs[0], sub.ID, false); err != nil {
			t.Fatalf("RecordQuizAnswer() error = %v", err)
		}
	}

	pendingRanking, err := contest.PendingRankingSubmissions(ctx, app.DB, weekID, participantIDs[0])
	if err != nil {
		t.Fatalf("PendingRankingSubmissions() error = %v", err)
	}
	if len(pendingRanking) != 1 {
		t.Fatalf("pending ranking count = %d, want 1 (2 participants, 1 other song)", len(pendingRanking))
	}

	data := rankingCallbackData(weekID, participantIDs[0], pendingRanking[0].ID)
	app.handleRankingPickCallback(ctx, app.TG, callbackUpdate(2001, data))

	var voteCount int
	if err := app.DB.QueryRow(
		"SELECT COUNT(*) FROM votes WHERE week_id = ? AND voter_id = ?", weekID, participantIDs[0],
	).Scan(&voteCount); err != nil {
		t.Fatalf("query votes: %v", err)
	}
	if voteCount != 1 {
		t.Errorf("vote count = %d, want 1", voteCount)
	}

	var points int
	if err := app.DB.QueryRow(
		"SELECT points FROM votes WHERE week_id = ? AND voter_id = ?", weekID, participantIDs[0],
	).Scan(&points); err != nil {
		t.Fatalf("query points: %v", err)
	}
	if points != 1 {
		t.Errorf("points = %d, want 1 (the only song in a 1-song ranking)", points)
	}
}

func TestHandleRankingPickCallback_RejectsWrongSender(t *testing.T) {
	app, weekID, participantIDs := setupResultsCollectionApp(t, 2001, 2002)
	ctx := context.Background()

	pendingRanking, _ := contest.PendingRankingSubmissions(ctx, app.DB, weekID, participantIDs[0])
	data := rankingCallbackData(weekID, participantIDs[0], pendingRanking[0].ID)

	app.handleRankingPickCallback(ctx, app.TG, callbackUpdate(2002, data))

	var count int
	app.DB.QueryRow("SELECT COUNT(*) FROM votes WHERE week_id = ?", weekID).Scan(&count)
	if count != 0 {
		t.Errorf("vote count = %d, want 0 (mismatched sender must be rejected)", count)
	}
}

// completeQuizAndRanking answers every pending familiarity question and
// ranking pick for one participant, in the real flow's order (questionnaire
// before ranking, R21), driving them to results-collection completion.
func completeQuizAndRanking(t *testing.T, ctx context.Context, app *App, weekID, participantID int64) {
	t.Helper()

	for {
		pending, _ := contest.PendingQuizSubmissions(ctx, app.DB, weekID, participantID)
		if len(pending) == 0 {
			break
		}
		if err := contest.RecordQuizAnswer(ctx, app.DB, weekID, participantID, pending[0].ID, false); err != nil {
			t.Fatalf("RecordQuizAnswer() error = %v", err)
		}
	}
	for {
		pending, _ := contest.PendingRankingSubmissions(ctx, app.DB, weekID, participantID)
		if len(pending) == 0 {
			break
		}
		if err := contest.RecordRankingPick(ctx, app.DB, weekID, participantID, pending[0].ID); err != nil {
			t.Fatalf("RecordRankingPick() error = %v", err)
		}
	}
}

func TestProcessResultsNotifications_PublishesOnceWeekComplete(t *testing.T) {
	app, weekID, participantIDs := setupResultsCollectionApp(t, 2001, 2002)
	ctx := context.Background()

	for _, pid := range participantIDs {
		completeQuizAndRanking(t, ctx, app, weekID, pid)
	}

	if err := app.Contest.Tick(ctx); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}

	var state string
	app.DB.QueryRow("SELECT state FROM weeks WHERE id = ?", weekID).Scan(&state)
	if state != contest.StateIdle {
		t.Fatalf("week state = %q, want %q after natural completion", state, contest.StateIdle)
	}

	if err := app.ProcessResultsNotifications(ctx); err != nil {
		t.Fatalf("ProcessResultsNotifications() error = %v", err)
	}

	var status string
	if err := app.DB.QueryRow(
		"SELECT status FROM outbox_actions WHERE action_type = ?", contest.OutboxActionPublishResults,
	).Scan(&status); err != nil {
		t.Fatalf("query publish_results status: %v", err)
	}
	if status != "done" {
		t.Errorf("publish_results status = %q, want done", status)
	}
}
