package bot

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/cristianat98/Music-Contest-Telegram/internal/contest"
)

// App wires the Telegram bot client to the storage layer and the contest
// lifecycle engine, and holds the runtime identity (own user ID, target
// chat ID) needed by middleware and the tick-driven lifecycle engine.
type App struct {
	TG      *tgbot.Bot
	DB      *sql.DB
	ChatID  int64
	SelfID  int64
	Contest *contest.Engine
}

// New creates the Telegram bot client, registers command handlers and the
// chat_member default-handler dispatch, and resolves the bot's own user ID.
func New(ctx context.Context, token string, chatID int64, db *sql.DB) (*App, error) {
	app := &App{DB: db, ChatID: chatID, Contest: contest.NewEngine(db)}

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
	app.Contest.SetSongsHooks(contest.NewSongsHooks(db))

	app.registerHandlers()

	return app, nil
}

// SendGroupMessage implements contest.GroupNotifier, letting the contest
// package post to the target chat without importing go-telegram/bot.
func (a *App) SendGroupMessage(ctx context.Context, text string) error {
	_, err := a.TG.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: a.ChatID,
		Text:   text,
	})
	return err
}

func (a *App) registerHandlers() {
	a.TG.RegisterHandler(tgbot.HandlerTypeMessageText, "syncparticipants", tgbot.MatchTypeCommand,
		AdminOnly(a, a.handleSyncParticipants))

	a.TG.RegisterHandler(tgbot.HandlerTypeMessageText, "startcontest", tgbot.MatchTypeCommand,
		AdminOnly(a, a.handleStartContest))
	a.TG.RegisterHandler(tgbot.HandlerTypeMessageText, "finishcontest", tgbot.MatchTypeCommand,
		AdminOnly(a, a.handleFinishContest))
	a.TG.RegisterHandler(tgbot.HandlerTypeMessageText, "startweek", tgbot.MatchTypeCommand,
		AdminOnly(a, a.handleStartWeek))
	a.TG.RegisterHandler(tgbot.HandlerTypeMessageText, "modifylimit", tgbot.MatchTypeCommand,
		AdminOnly(a, a.handleModifyLimit))
	a.TG.RegisterHandler(tgbot.HandlerTypeMessageText, "forceadvance", tgbot.MatchTypeCommand,
		AdminOnly(a, a.handleForceAdvance))

	a.TG.RegisterHandler(tgbot.HandlerTypeMessageText, "fixsubmission", tgbot.MatchTypeCommand,
		AdminOnly(a, a.handleFixSubmission))
	a.TG.RegisterHandler(tgbot.HandlerTypeMessageText, "removesubmission", tgbot.MatchTypeCommand,
		AdminOnly(a, a.handleRemoveSubmission))
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
	case update.Message != nil && update.Message.Chat.Type == models.ChatTypePrivate:
		a.handlePrivateMessage(ctx, b, update)
	default:
		log.Printf("bot: unhandled update id=%d", update.ID)
	}
}
