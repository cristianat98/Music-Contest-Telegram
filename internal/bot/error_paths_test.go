package bot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestParseQuizCallbackData_MalformedInputs(t *testing.T) {
	cases := []string{
		"q|1|2|3",   // only 3 fields, want 4
		"q|x|2|3|1", // non-numeric weekID
		"q|1|x|3|1", // non-numeric participantID
		"q|1|2|x|1", // non-numeric submissionID
	}
	for _, data := range cases {
		if _, _, _, _, err := parseQuizCallbackData(data); err == nil {
			t.Errorf("parseQuizCallbackData(%q) error = nil, want an error", data)
		}
	}
}

func TestParseRankingCallbackData_MalformedInputs(t *testing.T) {
	cases := []string{
		"r|1|2",   // only 2 fields, want 3
		"r|x|2|3", // non-numeric weekID
		"r|1|x|3", // non-numeric participantID
		"r|1|2|x", // non-numeric submissionID
	}
	for _, data := range cases {
		if _, _, _, err := parseRankingCallbackData(data); err == nil {
			t.Errorf("parseRankingCallbackData(%q) error = nil, want an error", data)
		}
	}
}

func TestParseWeekParticipantPayload_MalformedJSON(t *testing.T) {
	if _, _, err := parseWeekParticipantPayload("not json"); err == nil {
		t.Error("parseWeekParticipantPayload() error = nil, want an error for malformed JSON")
	}
}

func TestHandleQuizAnswerCallback_MalformedData_NoPanic(t *testing.T) {
	app := openTestApp(t)
	app.handleQuizAnswerCallback(context.Background(), nil, callbackUpdate(1, "q|not|valid"))
}

func TestHandleRankingPickCallback_MalformedData_NoPanic(t *testing.T) {
	app := openTestApp(t)
	app.handleRankingPickCallback(context.Background(), nil, callbackUpdate(1, "r|not|valid"))
}

func TestProcessResultsNotifications_DBError(t *testing.T) {
	app := openTestApp(t)
	app.DB.Close()

	if err := app.ProcessResultsNotifications(context.Background()); err == nil {
		t.Error("ProcessResultsNotifications() error = nil, want an error from the closed DB")
	}
}

func TestProcessResultsPrompts_DBError(t *testing.T) {
	app := openTestApp(t)
	app.DB.Close()

	if err := app.ProcessResultsPrompts(context.Background()); err == nil {
		t.Error("ProcessResultsPrompts() error = nil, want an error from the closed DB")
	}
}

func TestHandleStartContest_DBError(t *testing.T) {
	app, lastText := setupCommandApp(t, 0)
	app.DB.Close()

	app.handleStartContest(context.Background(), app.TG, messageUpdate(1, -100, "/startcontest Summer"))

	if !strings.Contains(*lastText, "Could not start contest") {
		t.Errorf("lastText = %q, want it to mention a failure starting the contest", *lastText)
	}
}

// errorTelegramServer returns ok:false for every Bot API call, used to
// exercise AdminOnly's and reply's error-logging branches.
func errorTelegramServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"error_code":500,"description":"internal error"}`))
	})
	return httptest.NewServer(mux)
}

func TestAdminOnly_GetChatMemberError_Rejects(t *testing.T) {
	srv := errorTelegramServer(t)
	defer srv.Close()

	app := &App{ChatID: -100, TG: newTestTGBot(t, srv.URL)}

	called := false
	handler := AdminOnly(app, func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
		called = true
	})

	handler(context.Background(), app.TG, messageUpdate(42, -100, "/syncparticipants"))

	if called {
		t.Error("expected next handler not to be called when admin status check fails")
	}
}

func TestReply_SendMessageError_LogsButDoesNotPanic(t *testing.T) {
	srv := errorTelegramServer(t)
	defer srv.Close()

	app := &App{TG: newTestTGBot(t, srv.URL)}
	app.reply(context.Background(), -100, "hello")
}

func TestAnswerCallback_Error_LogsButDoesNotPanic(t *testing.T) {
	srv := errorTelegramServer(t)
	defer srv.Close()

	app := &App{TG: newTestTGBot(t, srv.URL)}
	app.answerCallback(context.Background(), "cbid", "text")
}

func TestEnqueueAdminLostAlert_DBError(t *testing.T) {
	app := openTestApp(t)
	app.DB.Close()

	if err := app.enqueueAdminLostAlert(context.Background()); err == nil {
		t.Error("enqueueAdminLostAlert() error = nil, want an error from the closed DB")
	}
}

func TestHandleChatMemberUpdate_UnrecognizedStatus_NoOp(t *testing.T) {
	app := openTestApp(t)
	ctx := context.Background()

	app.handleChatMemberUpdate(ctx, &models.ChatMemberUpdated{
		NewChatMember: models.ChatMember{Type: models.ChatMemberType("unsupported")},
	})

	var count int
	app.DB.QueryRow("SELECT COUNT(*) FROM participants").Scan(&count)
	if count != 0 {
		t.Errorf("participants = %d, want 0 for an unrecognized status", count)
	}
}
