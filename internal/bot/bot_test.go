package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

func TestSendPrivateMessage_SendsToGivenUser(t *testing.T) {
	srv, lastText := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()

	app := &App{TG: newTestTGBot(t, srv.URL)}

	if err := app.SendPrivateMessage(context.Background(), 42, "hello there"); err != nil {
		t.Fatalf("SendPrivateMessage() error = %v", err)
	}
	if *lastText != "hello there" {
		t.Errorf("lastText = %q, want %q", *lastText, "hello there")
	}
}

func TestDefaultHandler_ChatMemberUpdate_UpsertsParticipant(t *testing.T) {
	app := openTestApp(t)
	ctx := context.Background()

	app.defaultHandler(ctx, nil, &models.Update{
		ChatMember: &models.ChatMemberUpdated{
			NewChatMember: memberChatMember(33, models.ChatMemberTypeMember),
		},
	})

	if _, found := countParticipants(t, app.DB, 33); !found {
		t.Error("expected defaultHandler to dispatch chat_member updates to handleChatMemberUpdate")
	}
}

func TestDefaultHandler_MyChatMemberUpdate_ChecksSelfAdminStatus(t *testing.T) {
	app := openTestApp(t)
	ctx := context.Background()

	app.defaultHandler(ctx, nil, &models.Update{
		MyChatMember: &models.ChatMemberUpdated{
			NewChatMember: memberChatMember(app.SelfID, models.ChatMemberTypeMember),
		},
	})

	var count int
	if err := app.DB.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ?", outboxActionAdminLost,
	).Scan(&count); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if count != 1 {
		t.Errorf("admin_status_lost rows = %d, want 1 (defaultHandler must dispatch my_chat_member updates)", count)
	}
}

func TestDefaultHandler_UnhandledUpdate_DoesNotPanic(t *testing.T) {
	app := openTestApp(t)
	app.defaultHandler(context.Background(), nil, &models.Update{ID: 1})
}

func TestRegisterHandlers_DoesNotPanic(t *testing.T) {
	srv, _ := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()

	app := &App{ChatID: -100, TG: newTestTGBot(t, srv.URL)}
	app.registerHandlers()
}

func TestStart_StopsWhenContextCanceled(t *testing.T) {
	srv, _ := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	defer srv.Close()

	app := &App{ChatID: -100, TG: newTestTGBot(t, srv.URL)}
	app.registerHandlers()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		app.Start(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start() did not return after context was canceled")
	}
}

func TestDisplayName_PrefersUsername(t *testing.T) {
	cases := []struct {
		user *models.User
		want string
	}{
		{&models.User{Username: "alice", FirstName: "A"}, "alice"},
		{&models.User{FirstName: "Bob"}, "Bob"},
	}
	for _, c := range cases {
		if got := displayName(c.user); got != c.want {
			t.Errorf("displayName(%+v) = %q, want %q", c.user, got, c.want)
		}
	}
}

func TestActiveStatus(t *testing.T) {
	active := []models.ChatMemberType{
		models.ChatMemberTypeOwner, models.ChatMemberTypeAdministrator,
		models.ChatMemberTypeMember, models.ChatMemberTypeRestricted,
	}
	for _, s := range active {
		if !activeStatus(s) {
			t.Errorf("activeStatus(%v) = false, want true", s)
		}
	}
	inactive := []models.ChatMemberType{models.ChatMemberTypeLeft, models.ChatMemberTypeBanned}
	for _, s := range inactive {
		if activeStatus(s) {
			t.Errorf("activeStatus(%v) = true, want false", s)
		}
	}
}

func TestChatMemberUser_AllStatuses(t *testing.T) {
	for _, status := range []models.ChatMemberType{
		models.ChatMemberTypeOwner, models.ChatMemberTypeAdministrator, models.ChatMemberTypeMember,
		models.ChatMemberTypeLeft,
	} {
		m := memberChatMember(7, status)
		user := chatMemberUser(m)
		if user == nil || user.ID != 7 {
			t.Errorf("chatMemberUser() for status %v = %v, want user with ID 7", status, user)
		}
	}
}

func TestChatMemberUser_UnknownType_ReturnsNil(t *testing.T) {
	if user := chatMemberUser(models.ChatMember{Type: models.ChatMemberType("unsupported")}); user != nil {
		t.Errorf("chatMemberUser() for an unrecognized status = %v, want nil", user)
	}
}

func TestLifecycleErrorText_UnknownError_FallsBackToErrorText(t *testing.T) {
	err := errUnwrapped("some db error")
	got := lifecycleErrorText(err)
	if !strings.Contains(got, "some db error") {
		t.Errorf("lifecycleErrorText() = %q, want it to contain the original error text", got)
	}
}

type errUnwrapped string

func (e errUnwrapped) Error() string { return string(e) }
