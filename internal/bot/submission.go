package bot

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/cristianat98/Music-Contest-Telegram/internal/contest"
)

// youtubeURLPattern validates URL *format* only (R16) -- no API call to
// confirm the video exists.
var youtubeURLPattern = regexp.MustCompile(`^https?://(www\.)?(youtube\.com/watch\?v=|youtu\.be/)[\w-]+`)

// handlePrivateMessage treats a private text message during songs_collection
// as a song submission attempt (R16). Any other private message (no active
// songs_collection state) is told why it wasn't accepted.
func (a *App) handlePrivateMessage(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	text := strings.TrimSpace(update.Message.Text)
	if text == "" || strings.HasPrefix(text, "/") {
		return
	}

	chatID := update.Message.Chat.ID

	week, err := a.Contest.CurrentWeek(ctx)
	if err != nil {
		a.reply(ctx, chatID, "Could not check contest state. Please try again.")
		return
	}
	if week.State != contest.StateSongsCollection {
		a.reply(ctx, chatID, "Songs aren't being collected right now.")
		return
	}

	if !youtubeURLPattern.MatchString(text) {
		a.reply(ctx, chatID, "That doesn't look like a YouTube URL. Send a youtube.com or youtu.be link.")
		return
	}

	participantID, err := a.participantIDByTelegramUserID(ctx, update.Message.From.ID)
	if errors.Is(err, sql.ErrNoRows) {
		a.reply(ctx, chatID, "You're not a recognized participant. Ask an admin to run /syncparticipants.")
		return
	}
	if err != nil {
		a.reply(ctx, chatID, "Could not verify your participant status. Please try again.")
		return
	}

	updated, err := a.upsertSubmission(ctx, week.ID, participantID, text)
	if err != nil {
		a.reply(ctx, chatID, "Could not save your submission. Please try again.")
		return
	}
	if updated {
		a.reply(ctx, chatID, "Submission updated.")
	} else {
		a.reply(ctx, chatID, "Submission received.")
	}
}

func (a *App) participantIDByTelegramUserID(ctx context.Context, telegramUserID int64) (int64, error) {
	var id int64
	err := a.DB.QueryRowContext(ctx, `SELECT id FROM participants WHERE telegram_user_id = ?`, telegramUserID).Scan(&id)
	return id, err
}

// upsertSubmission stores a participant's submission for a week, returning
// whether it replaced an existing one. Resubmission is allowed and simply
// overwrites the previous URL.
func (a *App) upsertSubmission(ctx context.Context, weekID, participantID int64, url string) (replaced bool, err error) {
	var existed int
	if err := a.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM submissions WHERE week_id = ? AND participant_id = ?
	`, weekID, participantID).Scan(&existed); err != nil {
		return false, err
	}

	_, err = a.DB.ExecContext(ctx, `
		INSERT INTO submissions (week_id, participant_id, url) VALUES (?, ?, ?)
		ON CONFLICT (week_id, participant_id) DO UPDATE SET url = excluded.url, created_at = datetime('now')
	`, weekID, participantID, url)
	if err != nil {
		return false, err
	}
	return existed > 0, nil
}

func (a *App) handleFixSubmission(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	username, url, ok := parseUsernameAndRest(commandArgs(update.Message.Text))
	if !ok || url == "" {
		a.reply(ctx, chatID, "Usage: /fixsubmission @user <url>")
		return
	}
	if !youtubeURLPattern.MatchString(url) {
		a.reply(ctx, chatID, "That doesn't look like a YouTube URL.")
		return
	}

	week, err := a.Contest.CurrentWeek(ctx)
	if err != nil || week.State != contest.StateSongsCollection {
		a.reply(ctx, chatID, "Submissions can only be fixed while songs are being collected.")
		return
	}

	participantID, err := a.participantIDByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		a.reply(ctx, chatID, "No participant found with that username.")
		return
	}
	if err != nil {
		a.reply(ctx, chatID, "Could not look up that participant. Please try again.")
		return
	}

	if _, err := a.upsertSubmission(ctx, week.ID, participantID, url); err != nil {
		a.reply(ctx, chatID, "Could not update the submission. Please try again.")
		return
	}
	a.reply(ctx, chatID, "Submission updated for "+username+".")
}

func (a *App) handleRemoveSubmission(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	username := strings.TrimPrefix(commandArgs(update.Message.Text), "@")
	if username == "" {
		a.reply(ctx, chatID, "Usage: /removesubmission @user")
		return
	}

	week, err := a.Contest.CurrentWeek(ctx)
	if err != nil || week.State != contest.StateSongsCollection {
		a.reply(ctx, chatID, "Submissions can only be removed while songs are being collected.")
		return
	}

	participantID, err := a.participantIDByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		a.reply(ctx, chatID, "No participant found with that username.")
		return
	}
	if err != nil {
		a.reply(ctx, chatID, "Could not look up that participant. Please try again.")
		return
	}

	if _, err := a.DB.ExecContext(ctx, `
		DELETE FROM submissions WHERE week_id = ? AND participant_id = ?
	`, week.ID, participantID); err != nil {
		a.reply(ctx, chatID, "Could not remove the submission. Please try again.")
		return
	}
	a.reply(ctx, chatID, "Submission removed for "+username+".")
}

func (a *App) participantIDByUsername(ctx context.Context, username string) (int64, error) {
	var id int64
	err := a.DB.QueryRowContext(ctx, `SELECT id FROM participants WHERE display_name = ?`, username).Scan(&id)
	return id, err
}

// parseUsernameAndRest splits "@user rest of the text" into ("user", "rest
// of the text", ok). ok is false if the first token isn't an @-mention.
func parseUsernameAndRest(args string) (username, rest string, ok bool) {
	parts := strings.SplitN(args, " ", 2)
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "@") {
		return "", "", false
	}
	username = strings.TrimPrefix(parts[0], "@")
	if len(parts) == 2 {
		rest = strings.TrimSpace(parts[1])
	}
	return username, rest, true
}
