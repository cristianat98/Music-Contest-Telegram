package bot

import (
	"context"
	"database/sql"
	"path/filepath"
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

	if _, err := app.DB.Exec("UPDATE participants SET strikes = 2 WHERE telegram_user_id = ?", 555); err != nil {
		t.Fatalf("seed strikes: %v", err)
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

	var strikes int
	if err := app.DB.QueryRow("SELECT strikes FROM participants WHERE telegram_user_id = ?", 555).Scan(&strikes); err != nil {
		t.Fatalf("query strikes: %v", err)
	}
	if strikes != 2 {
		t.Errorf("strikes = %d, want 2 (preserved across leave/rejoin)", strikes)
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
