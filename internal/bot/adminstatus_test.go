package bot

import (
	"context"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestCheckAdminStatus_StillAdmin_NoAlert(t *testing.T) {
	srv, _ := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()

	app := openTestApp(t)
	app.TG = newTestTGBot(t, srv.URL)

	if err := app.CheckAdminStatus(context.Background()); err != nil {
		t.Fatalf("CheckAdminStatus() error = %v", err)
	}

	var count int
	if err := app.DB.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ?", outboxActionAdminLost,
	).Scan(&count); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if count != 0 {
		t.Errorf("admin_status_lost rows = %d, want 0 (bot is still admin)", count)
	}
}

func TestCheckAdminStatus_LostAdmin_EnqueuesAlert(t *testing.T) {
	srv, _ := fakeTelegramServer(t, models.ChatMemberTypeMember)
	defer srv.Close()

	app := openTestApp(t)
	app.TG = newTestTGBot(t, srv.URL)

	if err := app.CheckAdminStatus(context.Background()); err != nil {
		t.Fatalf("CheckAdminStatus() error = %v", err)
	}

	var count int
	if err := app.DB.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ?", outboxActionAdminLost,
	).Scan(&count); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if count != 1 {
		t.Errorf("admin_status_lost rows = %d, want 1 (bot lost admin)", count)
	}
}

func TestHandleSelfChatMemberUpdate_StillAdmin_NoAlert(t *testing.T) {
	app := openTestApp(t)
	ctx := context.Background()

	app.handleSelfChatMemberUpdate(ctx, &models.ChatMemberUpdated{
		NewChatMember: memberChatMember(999, models.ChatMemberTypeAdministrator),
	})

	var count int
	if err := app.DB.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ?", outboxActionAdminLost,
	).Scan(&count); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if count != 0 {
		t.Errorf("admin_status_lost rows = %d, want 0", count)
	}
}

func TestHandleSelfChatMemberUpdate_LostAdmin_EnqueuesAlert(t *testing.T) {
	app := openTestApp(t)
	ctx := context.Background()

	app.handleSelfChatMemberUpdate(ctx, &models.ChatMemberUpdated{
		NewChatMember: memberChatMember(999, models.ChatMemberTypeMember),
	})

	var count int
	if err := app.DB.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ?", outboxActionAdminLost,
	).Scan(&count); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if count != 1 {
		t.Errorf("admin_status_lost rows = %d, want 1", count)
	}
}
