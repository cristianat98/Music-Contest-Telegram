package bot

import (
	"context"
	"log"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/cristianat98/Music-Contest-Telegram/internal/contest"
)

// activeStatus reports whether a chat member status counts as currently
// present in the group (R13).
func activeStatus(t models.ChatMemberType) bool {
	switch t {
	case models.ChatMemberTypeOwner, models.ChatMemberTypeAdministrator,
		models.ChatMemberTypeMember, models.ChatMemberTypeRestricted:
		return true
	default: // left, banned
		return false
	}
}

func chatMemberUser(m models.ChatMember) *models.User {
	switch m.Type {
	case models.ChatMemberTypeOwner:
		return m.Owner.User
	case models.ChatMemberTypeAdministrator:
		return &m.Administrator.User
	case models.ChatMemberTypeMember:
		return m.Member.User
	case models.ChatMemberTypeRestricted:
		return m.Restricted.User
	case models.ChatMemberTypeLeft:
		return m.Left.User
	case models.ChatMemberTypeBanned:
		return m.Banned.User
	default:
		return nil
	}
}

// handleChatMemberUpdate keeps the local roster in sync with Telegram's
// chat_member events (R13), upserting by Telegram user ID so a participant
// who leaves and rejoins keeps one roster row and their strike history
// (KTD8).
func (a *App) handleChatMemberUpdate(ctx context.Context, upd *models.ChatMemberUpdated) {
	user := chatMemberUser(upd.NewChatMember)
	if user == nil {
		return
	}

	active := activeStatus(upd.NewChatMember.Type)
	if err := a.upsertParticipant(ctx, user.ID, displayName(user), active); err != nil {
		log.Printf("bot: failed to upsert participant %d from chat_member update: %v", user.ID, err)
	}
}

func displayName(u *models.User) string {
	if u.Username != "" {
		return u.Username
	}
	return u.FirstName
}

// upsertParticipant keeps the global Telegram-roster row in sync. When a
// participant transitions to inactive, it also flips their
// contest_participants row for whichever contest is currently active (R3) --
// a no-op if no contest is active, they're unknown, or they aren't enrolled
// in it. This covers both the live chat_member event path
// (handleChatMemberUpdate) and the /syncparticipants reconciliation path
// (refreshKnownParticipants), since both call upsertParticipant.
func (a *App) upsertParticipant(ctx context.Context, telegramUserID int64, name string, active bool) error {
	if _, err := a.DB.ExecContext(ctx, `
		INSERT INTO participants (telegram_user_id, display_name, active)
		VALUES (?, ?, ?)
		ON CONFLICT (telegram_user_id) DO UPDATE SET
			display_name = excluded.display_name,
			active = excluded.active
	`, telegramUserID, name, active); err != nil {
		return err
	}

	if !active {
		if err := contest.SetParticipantLeft(ctx, a.DB, telegramUserID); err != nil {
			return err
		}
	}
	return nil
}

// handleSyncParticipants reconciles the local roster against Telegram's live
// group member administrator/member list (R14). Telegram's Bot API does not
// expose a full non-admin member list, so reconciliation is scoped to
// chat administrators plus any participant already known locally, refreshed
// against their current status.
func (a *App) handleSyncParticipants(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	admins, err := b.GetChatAdministrators(ctx, &tgbot.GetChatAdministratorsParams{ChatID: a.ChatID})
	if err != nil {
		log.Printf("bot: syncparticipants failed to list admins: %v", err)
		a.reply(ctx, update.Message.Chat.ID, "Failed to sync participants: could not reach Telegram.")
		return
	}

	seen := make(map[int64]bool)
	for _, admin := range admins {
		user := chatMemberUser(admin)
		if user == nil {
			continue
		}
		seen[user.ID] = true
		if err := a.upsertParticipant(ctx, user.ID, displayName(user), true); err != nil {
			log.Printf("bot: syncparticipants failed to upsert %d: %v", user.ID, err)
		}
	}

	if err := a.refreshKnownParticipants(ctx, b, seen); err != nil {
		log.Printf("bot: syncparticipants failed to refresh known participants: %v", err)
	}

	a.reply(ctx, update.Message.Chat.ID, "Participants synced.")
}

// refreshKnownParticipants re-checks every locally known participant not
// already confirmed active via the admin list, marking inactive anyone no
// longer present in the chat.
func (a *App) refreshKnownParticipants(ctx context.Context, b *tgbot.Bot, alreadySeen map[int64]bool) error {
	rows, err := a.DB.QueryContext(ctx, `SELECT telegram_user_id FROM participants`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range ids {
		if alreadySeen[id] {
			continue
		}
		member, err := b.GetChatMember(ctx, &tgbot.GetChatMemberParams{ChatID: a.ChatID, UserID: id})
		if err != nil {
			log.Printf("bot: syncparticipants could not check status of %d: %v", id, err)
			continue
		}
		user := chatMemberUser(*member)
		if user == nil {
			continue
		}
		if err := a.upsertParticipant(ctx, id, displayName(user), activeStatus(member.Type)); err != nil {
			log.Printf("bot: syncparticipants failed to update %d: %v", id, err)
		}
	}
	return nil
}
