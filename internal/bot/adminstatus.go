package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const outboxActionAdminLost = "admin_status_lost"

// CheckAdminStatus checks whether the bot still holds admin rights in the
// target chat and, if it has lost them, queues a durable group alert via the
// outbox (R15) -- losing admin status otherwise degrades silently, since
// chat_member events for non-admins aren't delivered to a non-admin bot.
// Intended to be called once per tick (wired in U4).
func (a *App) CheckAdminStatus(ctx context.Context) error {
	member, err := a.TG.GetChatMember(ctx, &tgbot.GetChatMemberParams{
		ChatID: a.ChatID,
		UserID: a.SelfID,
	})
	if err != nil {
		return fmt.Errorf("bot: check own admin status: %w", err)
	}

	if member.Type == models.ChatMemberTypeOwner || member.Type == models.ChatMemberTypeAdministrator {
		return nil
	}

	return a.enqueueAdminLostAlert(ctx)
}

func (a *App) enqueueAdminLostAlert(ctx context.Context) error {
	payload, err := json.Marshal(map[string]any{"chat_id": a.ChatID})
	if err != nil {
		return fmt.Errorf("bot: marshal admin-lost alert payload: %w", err)
	}

	_, err = a.DB.ExecContext(ctx, `
		INSERT INTO outbox_actions (action_type, payload_json, status)
		VALUES (?, ?, 'pending')
	`, outboxActionAdminLost, string(payload))
	if err != nil {
		return fmt.Errorf("bot: enqueue admin-lost alert: %w", err)
	}
	return nil
}

func (a *App) handleSelfChatMemberUpdate(ctx context.Context, upd *models.ChatMemberUpdated) {
	if upd.NewChatMember.Type == models.ChatMemberTypeOwner || upd.NewChatMember.Type == models.ChatMemberTypeAdministrator {
		return
	}
	if err := a.enqueueAdminLostAlert(ctx); err != nil {
		log.Printf("bot: failed to enqueue admin-lost alert: %v", err)
	}
}
