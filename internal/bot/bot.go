package bot

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// App wires the Telegram bot client to the storage layer and holds the
// runtime identity (own user ID, target chat ID) needed by middleware and
// the tick-driven lifecycle engine added in later units.
type App struct {
	TG     *tgbot.Bot
	DB     *sql.DB
	ChatID int64
	SelfID int64
}

// New creates the Telegram bot client, registers command handlers and the
// chat_member default-handler dispatch, and resolves the bot's own user ID.
func New(ctx context.Context, token string, chatID int64, db *sql.DB) (*App, error) {
	app := &App{DB: db, ChatID: chatID}

	opts := []tgbot.Option{
		tgbot.WithDefaultHandler(app.defaultHandler),
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{
			models.AllowedUpdateMessage,
			models.AllowedUpdateChatMember,
			models.AllowedUpdateCallbackQuery,
		}),
	}

	b, err := tgbot.New(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("bot: create telegram client: %w", err)
	}
	app.TG = b

	me, err := b.GetMe(ctx)
	if err != nil {
		return nil, fmt.Errorf("bot: get bot identity: %w", err)
	}
	app.SelfID = me.ID

	app.registerHandlers()

	return app, nil
}

func (a *App) registerHandlers() {
	a.TG.RegisterHandler(tgbot.HandlerTypeMessageText, "syncparticipants", tgbot.MatchTypeCommand,
		AdminOnly(a, a.handleSyncParticipants))
}

// Start begins long-polling for updates. It blocks until ctx is canceled.
func (a *App) Start(ctx context.Context) {
	a.TG.Start(ctx)
}

func (a *App) defaultHandler(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	switch {
	case update.ChatMember != nil:
		a.handleChatMemberUpdate(ctx, update.ChatMember)
	case update.MyChatMember != nil:
		a.handleSelfChatMemberUpdate(ctx, update.MyChatMember)
	default:
		log.Printf("bot: unhandled update id=%d", update.ID)
	}
}
