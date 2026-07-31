package bot

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/cristianat98/Music-Contest-Telegram/internal/storage"
)

func openTestApp(t *testing.T) *App {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("storage.Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &App{DB: db, ChatID: -100, SelfID: 999}
}

func countParticipants(t *testing.T, db *sql.DB, telegramUserID int64) (active bool, found bool) {
	t.Helper()
	var activeInt int
	err := db.QueryRow("SELECT active FROM participants WHERE telegram_user_id = ?", telegramUserID).Scan(&activeInt)
	if err == sql.ErrNoRows {
		return false, false
	}
	if err != nil {
		t.Fatalf("query participant: %v", err)
	}
	return activeInt == 1, true
}

func memberChatMember(userID int64, status models.ChatMemberType) models.ChatMember {
	user := &models.User{ID: userID, Username: "tester"}
	switch status {
	case models.ChatMemberTypeMember:
		return models.ChatMember{Type: status, Member: &models.ChatMemberMember{User: user}}
	case models.ChatMemberTypeLeft:
		return models.ChatMember{Type: status, Left: &models.ChatMemberLeft{User: user}}
	case models.ChatMemberTypeAdministrator:
		return models.ChatMember{Type: status, Administrator: &models.ChatMemberAdministrator{User: *user}}
	case models.ChatMemberTypeOwner:
		return models.ChatMember{Type: status, Owner: &models.ChatMemberOwner{User: user}}
	default:
		panic("unsupported status in test helper")
	}
}

func TestHandleChatMemberUpdate_NewMemberUpserts(t *testing.T) {
	app := openTestApp(t)
	ctx := context.Background()

	upd := &models.ChatMemberUpdated{
		NewChatMember: memberChatMember(12345, models.ChatMemberTypeMember),
	}
	app.handleChatMemberUpdate(ctx, upd)

	active, found := countParticipants(t, app.DB, 12345)
	if !found {
		t.Fatal("expected participant row to exist after chat_member update")
	}
	if !active {
		t.Error("expected participant to be active")
	}
}

func TestHandleChatMemberUpdate_LeftThenRejoined_OneRow(t *testing.T) {
	app := openTestApp(t)
	ctx := context.Background()

	app.handleChatMemberUpdate(ctx, &models.ChatMemberUpdated{
		NewChatMember: memberChatMember(555, models.ChatMemberTypeMember),
	})

	var firstID int64
	if err := app.DB.QueryRow("SELECT id FROM participants WHERE telegram_user_id = ?", 555).Scan(&firstID); err != nil {
		t.Fatalf("query participant id: %v", err)
	}

	app.handleChatMemberUpdate(ctx, &models.ChatMemberUpdated{
		NewChatMember: memberChatMember(555, models.ChatMemberTypeLeft),
	})
	app.handleChatMemberUpdate(ctx, &models.ChatMemberUpdated{
		NewChatMember: memberChatMember(555, models.ChatMemberTypeMember),
	})

	var count int
	if err := app.DB.QueryRow("SELECT COUNT(*) FROM participants WHERE telegram_user_id = ?", 555).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row for rejoined participant, got %d", count)
	}

	var secondID int64
	if err := app.DB.QueryRow("SELECT id FROM participants WHERE telegram_user_id = ?", 555).Scan(&secondID); err != nil {
		t.Fatalf("query participant id: %v", err)
	}
	if secondID != firstID {
		t.Errorf("participant id = %d, want %d (same roster row preserved across leave/rejoin)", secondID, firstID)
	}
}

func TestHandleChatMemberUpdate_Left_MarksInactive(t *testing.T) {
	app := openTestApp(t)
	ctx := context.Background()

	app.handleChatMemberUpdate(ctx, &models.ChatMemberUpdated{
		NewChatMember: memberChatMember(777, models.ChatMemberTypeMember),
	})
	app.handleChatMemberUpdate(ctx, &models.ChatMemberUpdated{
		NewChatMember: memberChatMember(777, models.ChatMemberTypeLeft),
	})

	active, found := countParticipants(t, app.DB, 777)
	if !found {
		t.Fatal("expected participant row to still exist")
	}
	if active {
		t.Error("expected participant to be marked inactive after leaving")
	}
}

func TestHandleChatMemberUpdate_Left_FlipsContestParticipantForActiveContest(t *testing.T) {
	app := openTestAppWithContest(t)
	ctx := context.Background()
	seedActiveSongsCollection(t, app, 555, 556)

	var contestID, participantID int64
	if err := app.DB.QueryRow("SELECT id FROM contests WHERE active = 1").Scan(&contestID); err != nil {
		t.Fatalf("query active contest: %v", err)
	}
	if err := app.DB.QueryRow("SELECT id FROM participants WHERE telegram_user_id = 555").Scan(&participantID); err != nil {
		t.Fatalf("query participant id: %v", err)
	}

	app.handleChatMemberUpdate(ctx, &models.ChatMemberUpdated{
		NewChatMember: memberChatMember(555, models.ChatMemberTypeLeft),
	})

	var leftAt sql.NullString
	if err := app.DB.QueryRow(
		"SELECT left_at FROM contest_participants WHERE contest_id = ? AND participant_id = ?", contestID, participantID,
	).Scan(&leftAt); err != nil {
		t.Fatalf("query contest_participants: %v", err)
	}
	if !leftAt.Valid || leftAt.String == "" {
		t.Error("expected contest_participants.left_at to be set on leave")
	}
}

func TestHandleChatMemberUpdate_Left_NoActiveContest_NoOp(t *testing.T) {
	app := openTestApp(t)
	ctx := context.Background()

	app.handleChatMemberUpdate(ctx, &models.ChatMemberUpdated{
		NewChatMember: memberChatMember(888, models.ChatMemberTypeMember),
	})
	app.handleChatMemberUpdate(ctx, &models.ChatMemberUpdated{
		NewChatMember: memberChatMember(888, models.ChatMemberTypeLeft),
	})

	var count int
	if err := app.DB.QueryRow("SELECT COUNT(*) FROM contest_participants").Scan(&count); err != nil {
		t.Fatalf("count contest_participants: %v", err)
	}
	if count != 0 {
		t.Errorf("contest_participants row count = %d, want 0 (no active contest to flip)", count)
	}
}

// fakeSyncServer serves getChatAdministrators (returning admins),
// getChatMember (status looked up per user_id in memberStatuses, defaulting
// to "left" for unknown users), and sendMessage, for handleSyncParticipants
// and refreshKnownParticipants tests.
func fakeSyncServer(t *testing.T, admins []models.ChatMember, memberStatuses map[int64]models.ChatMemberType) (*httptest.Server, *string) {
	t.Helper()
	var lastSentText string

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getChatAdministrators"):
			writeOKResult(w, admins)
		case strings.HasSuffix(r.URL.Path, "/getChatMember"):
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse getChatMember form: %v", err)
			}
			userID, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
			status, ok := memberStatuses[userID]
			if !ok {
				status = models.ChatMemberTypeLeft
			}
			member := memberChatMember(userID, status)
			writeOKResult(w, &member)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse sendMessage form: %v", err)
			}
			lastSentText = r.FormValue("text")
			writeOKResult(w, map[string]any{"message_id": 1})
		default:
			writeOKResult(w, map[string]any{})
		}
	})

	return httptest.NewServer(mux), &lastSentText
}

func TestHandleSyncParticipants_UpsertsAdminsAndReplies(t *testing.T) {
	app := openTestApp(t)
	ctx := context.Background()

	admins := []models.ChatMember{memberChatMember(11, models.ChatMemberTypeAdministrator)}
	srv, lastText := fakeSyncServer(t, admins, nil)
	defer srv.Close()
	app.TG = newTestTGBot(t, srv.URL)

	app.handleSyncParticipants(ctx, app.TG, messageUpdate(1, -100, "/syncparticipants"))

	active, found := countParticipants(t, app.DB, 11)
	if !found {
		t.Fatal("expected admin to be upserted as a participant")
	}
	if !active {
		t.Error("expected admin to be marked active")
	}
	if !strings.Contains(*lastText, "synced") {
		t.Errorf("lastText = %q, want it to confirm the sync", *lastText)
	}
}

func TestHandleSyncParticipants_RefreshesKnownParticipantsNotInAdminList(t *testing.T) {
	app := openTestApp(t)
	ctx := context.Background()

	// 22 is already known locally (e.g. from a past chat_member event) but
	// isn't in the current admin list, so it must be refreshed via
	// refreshKnownParticipants against its live status.
	app.handleChatMemberUpdate(ctx, &models.ChatMemberUpdated{
		NewChatMember: memberChatMember(22, models.ChatMemberTypeMember),
	})

	srv, _ := fakeSyncServer(t, nil, map[int64]models.ChatMemberType{22: models.ChatMemberTypeLeft})
	defer srv.Close()
	app.TG = newTestTGBot(t, srv.URL)

	app.handleSyncParticipants(ctx, app.TG, messageUpdate(1, -100, "/syncparticipants"))

	active, found := countParticipants(t, app.DB, 22)
	if !found {
		t.Fatal("expected participant 22 to still exist")
	}
	if active {
		t.Error("expected participant 22 to be marked inactive after refresh found them left")
	}
}

func TestHandleSyncParticipants_AdminListFailure_Replies(t *testing.T) {
	app := openTestApp(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getChatAdministrators") {
			w.Write([]byte(`{"ok":false,"error_code":400,"description":"bad request"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			r.ParseMultipartForm(1 << 20)
			writeOKResult(w, map[string]any{"message_id": 1})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	app.TG = newTestTGBot(t, srv.URL)

	app.handleSyncParticipants(context.Background(), app.TG, messageUpdate(1, -100, "/syncparticipants"))
}
