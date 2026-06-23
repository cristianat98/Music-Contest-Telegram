package bot

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/cristianat98/Music-Contest-Telegram/internal/contest"
	"github.com/cristianat98/Music-Contest-Telegram/internal/storage"
)

func openTestAppWithContest(t *testing.T) *App {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("storage.Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })

	engine := contest.NewEngine(db)
	engine.SetSongsHooks(contest.NewSongsHooks(db))

	srv, _ := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	t.Cleanup(srv.Close)

	return &App{DB: db, ChatID: -100, SelfID: 999, Contest: engine, TG: newTestTGBot(t, srv.URL)}
}

func seedActiveSongsCollection(t *testing.T, app *App, participantTelegramIDs ...int64) int64 {
	t.Helper()
	ctx := context.Background()

	for i, id := range participantTelegramIDs {
		if _, err := app.DB.Exec(
			"INSERT INTO participants (telegram_user_id, display_name, active) VALUES (?, ?, 1)",
			id, fmt.Sprintf("user%d", i),
		); err != nil {
			t.Fatalf("seed participant: %v", err)
		}
	}
	if _, err := app.DB.Exec("INSERT INTO topics (text) VALUES ('topic-a')"); err != nil {
		t.Fatalf("seed topic: %v", err)
	}
	if _, err := app.Contest.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := app.Contest.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	week, err := app.Contest.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}
	return week.ID
}

func privateMessageUpdate(senderID int64, text string) *models.Update {
	return &models.Update{
		Message: &models.Message{
			From: &models.User{ID: senderID, Username: "submitter"},
			Chat: models.Chat{ID: senderID, Type: models.ChatTypePrivate},
			Text: text,
		},
	}
}

func TestHandlePrivateMessage_ValidYouTubeURL_Accepted(t *testing.T) {
	app := openTestAppWithContest(t)
	weekID := seedActiveSongsCollection(t, app, 1001, 1002)

	app.handlePrivateMessage(context.Background(), nil, privateMessageUpdate(1001, "https://youtu.be/dQw4w9WgXcQ"))

	var count int
	if err := app.DB.QueryRow("SELECT COUNT(*) FROM submissions WHERE week_id = ?", weekID).Scan(&count); err != nil {
		t.Fatalf("query submissions: %v", err)
	}
	if count != 1 {
		t.Errorf("submissions = %d, want 1", count)
	}
}

func TestHandlePrivateMessage_EmptyText_Ignored(t *testing.T) {
	app := openTestAppWithContest(t)
	seedActiveSongsCollection(t, app, 1001, 1002)

	app.handlePrivateMessage(context.Background(), nil, privateMessageUpdate(1001, "   "))

	var count int
	app.DB.QueryRow("SELECT COUNT(*) FROM submissions").Scan(&count)
	if count != 0 {
		t.Errorf("submissions = %d, want 0 for an empty message", count)
	}
}

func TestHandlePrivateMessage_SlashCommand_Ignored(t *testing.T) {
	app := openTestAppWithContest(t)
	seedActiveSongsCollection(t, app, 1001, 1002)

	app.handlePrivateMessage(context.Background(), nil, privateMessageUpdate(1001, "/start"))

	var count int
	app.DB.QueryRow("SELECT COUNT(*) FROM submissions").Scan(&count)
	if count != 0 {
		t.Errorf("submissions = %d, want 0 for a slash command", count)
	}
}

func TestHandlePrivateMessage_NotSongsCollection_Rejected(t *testing.T) {
	app := openTestAppWithContest(t)
	srv, lastText := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()
	app.TG = newTestTGBot(t, srv.URL)

	app.handlePrivateMessage(context.Background(), nil, privateMessageUpdate(1001, "https://youtu.be/abc"))

	if !strings.Contains(*lastText, "aren't being collected") {
		t.Errorf("lastText = %q, want it to mention songs aren't being collected", *lastText)
	}
}

func TestHandlePrivateMessage_UnknownParticipant_Rejected(t *testing.T) {
	app := openTestAppWithContest(t)
	seedActiveSongsCollection(t, app, 1001, 1002)
	srv, lastText := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()
	app.TG = newTestTGBot(t, srv.URL)

	app.handlePrivateMessage(context.Background(), nil, privateMessageUpdate(9999, "https://youtu.be/abc"))

	if !strings.Contains(*lastText, "not a recognized participant") {
		t.Errorf("lastText = %q, want it to mention the sender isn't recognized", *lastText)
	}
}

func TestHandlePrivateMessage_MalformedURL_Rejected(t *testing.T) {
	app := openTestAppWithContest(t)
	seedActiveSongsCollection(t, app, 1001, 1002)

	app.handlePrivateMessage(context.Background(), nil, privateMessageUpdate(1001, "not a url"))

	var count int
	if err := app.DB.QueryRow("SELECT COUNT(*) FROM submissions").Scan(&count); err != nil {
		t.Fatalf("query submissions: %v", err)
	}
	if count != 0 {
		t.Errorf("submissions = %d, want 0 for malformed URL", count)
	}
}

func TestHandlePrivateMessage_Resubmission_Replaces(t *testing.T) {
	app := openTestAppWithContest(t)
	weekID := seedActiveSongsCollection(t, app, 1001, 1002)
	ctx := context.Background()

	app.handlePrivateMessage(ctx, nil, privateMessageUpdate(1001, "https://youtu.be/first"))
	app.handlePrivateMessage(ctx, nil, privateMessageUpdate(1001, "https://youtu.be/second"))

	var count int
	var url string
	app.DB.QueryRow("SELECT COUNT(*) FROM submissions WHERE week_id = ?", weekID).Scan(&count)
	app.DB.QueryRow("SELECT url FROM submissions WHERE week_id = ?", weekID).Scan(&url)

	if count != 1 {
		t.Errorf("submissions = %d, want 1 (resubmission replaces, not duplicates)", count)
	}
	if url != "https://youtu.be/second" {
		t.Errorf("url = %q, want the latest submission", url)
	}
}

func TestHandlePrivateMessage_AllSubmitted_PublishesShuffledNoAttribution(t *testing.T) {
	app := openTestAppWithContest(t)
	weekID := seedActiveSongsCollection(t, app, 1001, 1002)
	ctx := context.Background()

	app.handlePrivateMessage(ctx, nil, privateMessageUpdate(1001, "https://youtu.be/aaa"))
	app.handlePrivateMessage(ctx, nil, privateMessageUpdate(1002, "https://youtu.be/bbb"))

	complete, err := contest.NewSongsHooks(app.DB).SongsCollectionComplete(ctx, app.DB, weekID)
	if err != nil {
		t.Fatalf("SongsCollectionComplete() error = %v", err)
	}
	if !complete {
		t.Fatal("expected songs_collection to be complete once both participants submitted")
	}

	if err := app.Contest.Tick(ctx); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}

	var state string
	app.DB.QueryRow("SELECT state FROM weeks WHERE id = ?", weekID).Scan(&state)
	if state != contest.StateResultsCollection {
		t.Errorf("week state = %q, want %q after natural completion", state, contest.StateResultsCollection)
	}

	var outboxCount int
	app.DB.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ? AND status = 'pending'", contest.OutboxActionPublishSongs,
	).Scan(&outboxCount)
	if outboxCount != 1 {
		t.Errorf("pending publish_songs rows = %d, want 1", outboxCount)
	}

	notifier := &fakeAppNotifier{}
	if err := contest.PublishDueSongs(ctx, app.DB, notifier); err != nil {
		t.Fatalf("PublishDueSongs() error = %v", err)
	}
	if len(notifier.messages) != 1 {
		t.Fatalf("messages sent = %d, want 1", len(notifier.messages))
	}
	msg := notifier.messages[0]
	if !strings.Contains(msg, "https://youtu.be/aaa") || !strings.Contains(msg, "https://youtu.be/bbb") {
		t.Errorf("published message missing expected URLs: %q", msg)
	}
}

type fakeAppNotifier struct {
	messages []string
}

func (f *fakeAppNotifier) SendGroupMessage(ctx context.Context, text string) error {
	f.messages = append(f.messages, text)
	return nil
}

func TestHandleFixSubmission_ReplacesExisting(t *testing.T) {
	app := openTestAppWithContest(t)
	weekID := seedActiveSongsCollection(t, app, 1001, 1002)
	ctx := context.Background()

	app.handlePrivateMessage(ctx, nil, privateMessageUpdate(1001, "https://youtu.be/original"))

	app.handleFixSubmission(ctx, nil, &models.Update{
		Message: &models.Message{
			From: &models.User{ID: 9999},
			Chat: models.Chat{ID: -100},
			Text: "/fixsubmission @user0 https://youtu.be/fixed",
		},
	})

	var url string
	app.DB.QueryRow("SELECT url FROM submissions WHERE week_id = ?", weekID).Scan(&url)
	if url != "https://youtu.be/fixed" {
		t.Errorf("url = %q, want the fixed submission", url)
	}
}

func adminMessageUpdate(text string) *models.Update {
	return &models.Update{
		Message: &models.Message{
			From: &models.User{ID: 9999},
			Chat: models.Chat{ID: -100},
			Text: text,
		},
	}
}

func TestHandleFixSubmission_UsageMessage(t *testing.T) {
	app := openTestAppWithContest(t)
	srv, lastText := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()
	app.TG = newTestTGBot(t, srv.URL)

	app.handleFixSubmission(context.Background(), nil, adminMessageUpdate("/fixsubmission"))

	if !strings.Contains(*lastText, "Usage") {
		t.Errorf("lastText = %q, want it to mention Usage", *lastText)
	}
}

func TestHandleFixSubmission_MalformedURL_Rejected(t *testing.T) {
	app := openTestAppWithContest(t)
	srv, lastText := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()
	app.TG = newTestTGBot(t, srv.URL)

	app.handleFixSubmission(context.Background(), nil, adminMessageUpdate("/fixsubmission @user0 not-a-url"))

	if !strings.Contains(*lastText, "YouTube") {
		t.Errorf("lastText = %q, want it to mention an invalid YouTube URL", *lastText)
	}
}

func TestHandleFixSubmission_NoActiveWeek_Rejected(t *testing.T) {
	app := openTestAppWithContest(t)
	srv, lastText := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()
	app.TG = newTestTGBot(t, srv.URL)

	app.handleFixSubmission(context.Background(), nil, adminMessageUpdate("/fixsubmission @user0 https://youtu.be/fixed"))

	if !strings.Contains(*lastText, "no active week") {
		t.Errorf("lastText = %q, want it to mention no active week", *lastText)
	}
}

func TestHandleFixSubmission_UnknownUsername_Rejected(t *testing.T) {
	app := openTestAppWithContest(t)
	seedActiveSongsCollection(t, app, 1001, 1002)
	srv, lastText := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()
	app.TG = newTestTGBot(t, srv.URL)

	app.handleFixSubmission(context.Background(), nil, adminMessageUpdate("/fixsubmission @nobody https://youtu.be/fixed"))

	if !strings.Contains(*lastText, "No participant found") {
		t.Errorf("lastText = %q, want it to mention no participant found", *lastText)
	}
}

func TestHandleRemoveSubmission_UsageMessage(t *testing.T) {
	app := openTestAppWithContest(t)
	srv, lastText := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()
	app.TG = newTestTGBot(t, srv.URL)

	app.handleRemoveSubmission(context.Background(), nil, adminMessageUpdate("/removesubmission"))

	if !strings.Contains(*lastText, "Usage") {
		t.Errorf("lastText = %q, want it to mention Usage", *lastText)
	}
}

func TestHandleRemoveSubmission_NoActiveWeek_Rejected(t *testing.T) {
	app := openTestAppWithContest(t)
	srv, lastText := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()
	app.TG = newTestTGBot(t, srv.URL)

	app.handleRemoveSubmission(context.Background(), nil, adminMessageUpdate("/removesubmission @user0"))

	if !strings.Contains(*lastText, "no active week") {
		t.Errorf("lastText = %q, want it to mention no active week", *lastText)
	}
}

func TestHandleRemoveSubmission_UnknownUsername_Rejected(t *testing.T) {
	app := openTestAppWithContest(t)
	seedActiveSongsCollection(t, app, 1001, 1002)
	srv, lastText := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()
	app.TG = newTestTGBot(t, srv.URL)

	app.handleRemoveSubmission(context.Background(), nil, adminMessageUpdate("/removesubmission @nobody"))

	if !strings.Contains(*lastText, "No participant found") {
		t.Errorf("lastText = %q, want it to mention no participant found", *lastText)
	}
}

func TestHandleRemoveSubmission_DeletesExisting(t *testing.T) {
	app := openTestAppWithContest(t)
	weekID := seedActiveSongsCollection(t, app, 1001, 1002)
	ctx := context.Background()

	app.handlePrivateMessage(ctx, nil, privateMessageUpdate(1001, "https://youtu.be/original"))

	app.handleRemoveSubmission(ctx, nil, &models.Update{
		Message: &models.Message{
			From: &models.User{ID: 9999},
			Chat: models.Chat{ID: -100},
			Text: "/removesubmission @user0",
		},
	})

	var count int
	app.DB.QueryRow("SELECT COUNT(*) FROM submissions WHERE week_id = ?", weekID).Scan(&count)
	if count != 0 {
		t.Errorf("submissions = %d, want 0 after removal", count)
	}
}
