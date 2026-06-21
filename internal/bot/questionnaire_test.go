package bot

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/cristianat98/Music-Contest-Telegram/internal/contest"
	"github.com/cristianat98/Music-Contest-Telegram/internal/storage"
)

// setupResultsCollectionApp starts a contest/week with n participants, has
// each submit a song, and force-advances into results_collection so the
// questionnaire/ranking flow can be exercised. Returns the app, week ID,
// and participant IDs (insertion order matches the supplied telegram IDs).
func setupResultsCollectionApp(t *testing.T, telegramIDs ...int64) (*App, int64, []int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("storage.Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })

	engine := contest.NewEngine(db)
	engine.SetSongsHooks(contest.NewSongsHooks(db))
	engine.SetResultsHooks(contest.NewResultsHooks(db))

	srv, _ := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	t.Cleanup(srv.Close)
	app := &App{DB: db, ChatID: -100, SelfID: 999, Contest: engine, TG: newTestTGBot(t, srv.URL)}

	ctx := context.Background()
	for i, id := range telegramIDs {
		if _, err := db.Exec(
			"INSERT INTO participants (telegram_user_id, display_name, active) VALUES (?, ?, 1)",
			id, fmtUser(i),
		); err != nil {
			t.Fatalf("seed participant: %v", err)
		}
	}
	if _, err := db.Exec("INSERT INTO topics (text) VALUES ('topic-a')"); err != nil {
		t.Fatalf("seed topic: %v", err)
	}
	if _, err := engine.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := engine.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	week, err := engine.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}

	rows, _ := db.Query("SELECT id FROM participants ORDER BY id")
	var participantIDs []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		participantIDs = append(participantIDs, id)
	}
	rows.Close()

	for i, pid := range participantIDs {
		if _, err := db.Exec(
			"INSERT INTO submissions (week_id, participant_id, url) VALUES (?, ?, ?)",
			week.ID, pid, fmtURL(i),
		); err != nil {
			t.Fatalf("seed submission: %v", err)
		}
	}

	if _, err := engine.ForceAdvance(ctx); err != nil {
		t.Fatalf("ForceAdvance() to results_collection error = %v", err)
	}

	return app, week.ID, participantIDs
}

func fmtUser(i int) string { return "user" + string(rune('0'+i)) }
func fmtURL(i int) string  { return "https://youtu.be/song" + string(rune('a'+i)) }

func callbackUpdate(senderID int64, data string) *models.Update {
	return &models.Update{
		CallbackQuery: &models.CallbackQuery{
			ID:   "cbid",
			From: models.User{ID: senderID},
			Data: data,
		},
	}
}

func TestProcessResultsPrompts_SendsFirstQuizQuestion(t *testing.T) {
	app, weekID, participantIDs := setupResultsCollectionApp(t, 2001, 2002)
	ctx := context.Background()

	var pendingCount int
	app.DB.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ?", contest.OutboxActionStartResultsPrompt,
	).Scan(&pendingCount)
	if pendingCount != len(participantIDs) {
		t.Fatalf("start_results_prompt rows = %d, want %d", pendingCount, len(participantIDs))
	}

	if err := app.ProcessResultsPrompts(ctx); err != nil {
		t.Fatalf("ProcessResultsPrompts() error = %v", err)
	}

	var doneCount int
	app.DB.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ? AND status = 'done'", contest.OutboxActionStartResultsPrompt,
	).Scan(&doneCount)
	if doneCount != len(participantIDs) {
		t.Errorf("done start_results_prompt rows = %d, want %d", doneCount, len(participantIDs))
	}
	_ = weekID
}

func TestHandleQuizAnswerCallback_RecordsAndAdvances(t *testing.T) {
	app, weekID, participantIDs := setupResultsCollectionApp(t, 2001, 2002)
	ctx := context.Background()

	pending, err := contest.PendingQuizSubmissions(ctx, app.DB, weekID, participantIDs[0])
	if err != nil {
		t.Fatalf("PendingQuizSubmissions() error = %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending quiz count = %d, want 1 (2 participants, 1 other song)", len(pending))
	}

	data := quizCallbackData(weekID, participantIDs[0], pending[0].ID, true)
	app.handleQuizAnswerCallback(ctx, app.TG, callbackUpdate(2001, data))

	var knew int
	if err := app.DB.QueryRow(
		"SELECT already_knew FROM quiz_answers WHERE week_id = ? AND participant_id = ? AND submission_id = ?",
		weekID, participantIDs[0], pending[0].ID,
	).Scan(&knew); err != nil {
		t.Fatalf("query quiz_answers: %v", err)
	}
	if knew != 1 {
		t.Errorf("already_knew = %d, want 1", knew)
	}

	remaining, err := contest.PendingQuizSubmissions(ctx, app.DB, weekID, participantIDs[0])
	if err != nil {
		t.Fatalf("PendingQuizSubmissions() after answer error = %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("remaining quiz questions = %d, want 0", len(remaining))
	}
}

func TestHandleQuizAnswerCallback_RejectsWrongSender(t *testing.T) {
	app, weekID, participantIDs := setupResultsCollectionApp(t, 2001, 2002)
	ctx := context.Background()

	pending, _ := contest.PendingQuizSubmissions(ctx, app.DB, weekID, participantIDs[0])
	data := quizCallbackData(weekID, participantIDs[0], pending[0].ID, true)

	// Sender 2002 tries to answer on behalf of participant 0 (telegram ID 2001).
	app.handleQuizAnswerCallback(ctx, app.TG, callbackUpdate(2002, data))

	var count int
	app.DB.QueryRow("SELECT COUNT(*) FROM quiz_answers WHERE week_id = ?", weekID).Scan(&count)
	if count != 0 {
		t.Errorf("quiz_answers count = %d, want 0 (mismatched sender must be rejected)", count)
	}
}
