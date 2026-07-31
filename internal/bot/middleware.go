package bot

import (
	"context"
	"log"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// AdminOnly wraps a handler so it only runs for senders who are an owner or
// administrator of the target chat. On any error resolving admin status it
// fails closed -- the command is rejected rather than allowed through.
func AdminOnly(a *App, next tgbot.HandlerFunc) tgbot.HandlerFunc {
	return func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
		if update.Message == nil {
			return
		}

		senderID := update.Message.From.ID
		member, err := b.GetChatMember(ctx, &tgbot.GetChatMemberParams{
			ChatID: a.ChatID,
			UserID: senderID,
		})
		if err != nil {
			log.Printf("bot: admin check failed for user %d: %v", senderID, err)
			a.reply(ctx, update.Message.Chat.ID, "Could not verify admin status. Please try again.")
			return
		}

		if member.Type != models.ChatMemberTypeOwner && member.Type != models.ChatMemberTypeAdministrator {
			a.reply(ctx, update.Message.Chat.ID, "This command is admin-only.")
			return
		}

		next(ctx, b, update)
	}
}

func (a *App) reply(ctx context.Context, chatID int64, text string) {
	_, err := a.TG.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
	if err != nil {
		log.Printf("bot: failed to send reply to chat %d: %v", chatID, err)
	}
}
