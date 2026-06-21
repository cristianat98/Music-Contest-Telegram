package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// fakeTelegramServer serves just enough of the Bot API for AdminOnly tests:
// getChatMember (status driven by senderStatus) and sendMessage (records the
// last text sent so the test can assert on rejection messages).
func fakeTelegramServer(t *testing.T, senderStatus models.ChatMemberType) (*httptest.Server, *string) {
	t.Helper()
	var lastSentText string

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getChatMember"):
			result := map[string]any{"status": string(senderStatus), "user": map[string]any{"id": 1, "is_bot": false}}
			writeOKResult(w, result)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			r.ParseMultipartForm(1 << 20)
			lastSentText = r.FormValue("text")
			writeOKResult(w, map[string]any{"message_id": 1})
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			writeOKResult(w, true)
		default:
			writeOKResult(w, map[string]any{})
		}
	})

	return httptest.NewServer(mux), &lastSentText
}

func writeOKResult(w http.ResponseWriter, result any) {
	body, _ := json.Marshal(map[string]any{"ok": true, "result": result})
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func newTestTGBot(t *testing.T, serverURL string) *tgbot.Bot {
	t.Helper()
	b, err := tgbot.New("test-token", tgbot.WithServerURL(serverURL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("tgbot.New() error = %v", err)
	}
	return b
}

func messageUpdate(senderID, chatID int64, text string) *models.Update {
	return &models.Update{
		Message: &models.Message{
			From: &models.User{ID: senderID},
			Chat: models.Chat{ID: chatID},
			Text: text,
		},
	}
}

func TestAdminOnly_RejectsNonAdmin(t *testing.T) {
	srv, lastText := fakeTelegramServer(t, models.ChatMemberTypeMember)
	defer srv.Close()

	app := &App{ChatID: -100, TG: newTestTGBot(t, srv.URL)}

	called := false
	handler := AdminOnly(app, func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
		called = true
	})

	handler(context.Background(), app.TG, messageUpdate(42, -100, "/syncparticipants"))

	if called {
		t.Error("expected next handler not to be called for a non-admin sender")
	}
	if !strings.Contains(*lastText, "admin-only") {
		t.Errorf("expected a rejection message mentioning admin-only, got %q", *lastText)
	}
}

func TestAdminOnly_AllowsAdmin(t *testing.T) {
	srv, _ := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()

	app := &App{ChatID: -100, TG: newTestTGBot(t, srv.URL)}

	called := false
	handler := AdminOnly(app, func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
		called = true
	})

	handler(context.Background(), app.TG, messageUpdate(42, -100, "/syncparticipants"))

	if !called {
		t.Error("expected next handler to be called for an admin sender")
	}
}

func TestAdminOnly_AllowsOwner(t *testing.T) {
	srv, _ := fakeTelegramServer(t, models.ChatMemberTypeOwner)
	defer srv.Close()

	app := &App{ChatID: -100, TG: newTestTGBot(t, srv.URL)}

	called := false
	handler := AdminOnly(app, func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
		called = true
	})

	handler(context.Background(), app.TG, messageUpdate(42, -100, "/syncparticipants"))

	if !called {
		t.Error("expected next handler to be called for an owner sender")
	}
}
