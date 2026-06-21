package bot

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/cristianat98/Music-Contest-Telegram/internal/contest"
)

const rankingCallbackPrefix = "r|"

// sendNextRankingPrompt presents the participant's remaining unranked
// songs as buttons; picking one removes it and the next call presents
// what's left, until a strict order is produced (R22). Reached once a
// participant's questionnaire is exhausted (questionnaire.go).
func (a *App) sendNextRankingPrompt(ctx context.Context, weekID, participantID int64) error {
	pending, err := contest.PendingRankingSubmissions(ctx, a.DB, weekID, participantID)
	if err != nil {
		return err
	}

	telegramUserID, err := a.telegramUserIDByParticipantID(ctx, participantID)
	if err != nil {
		return err
	}

	if len(pending) == 0 {
		_, err := a.TG.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID: telegramUserID,
			Text:   "Thanks! Your ranking and questionnaire are complete.",
		})
		return err
	}

	var buttons [][]models.InlineKeyboardButton
	for _, sub := range pending {
		buttons = append(buttons, []models.InlineKeyboardButton{{
			Text:         fmt.Sprintf("Song %d", sub.DisplayOrder),
			CallbackData: rankingCallbackData(weekID, participantID, sub.ID),
		}})
	}

	_, err = a.TG.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      telegramUserID,
		Text:        "Pick your next favorite among the remaining songs:",
		ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: buttons},
	})
	if err != nil {
		return fmt.Errorf("bot: send ranking prompt: %w", err)
	}
	return nil
}

func rankingCallbackData(weekID, participantID, submissionID int64) string {
	return fmt.Sprintf("%s%d|%d|%d", rankingCallbackPrefix, weekID, participantID, submissionID)
}

// handleRankingPickCallback records a ranking pick and advances the flow.
func (a *App) handleRankingPickCallback(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	cq := update.CallbackQuery
	weekID, participantID, submissionID, err := parseRankingCallbackData(cq.Data)
	if err != nil {
		log.Printf("bot: malformed ranking callback data %q: %v", cq.Data, err)
		return
	}

	if !a.callbackSenderMatchesParticipant(ctx, cq.From.ID, participantID) {
		a.answerCallback(ctx, cq.ID, "This isn't your ranking to make.")
		return
	}

	if err := contest.RecordRankingPick(ctx, a.DB, weekID, participantID, submissionID); err != nil {
		log.Printf("bot: failed to record ranking pick: %v", err)
		a.answerCallback(ctx, cq.ID, "Could not record your pick. Please try again.")
		return
	}
	a.answerCallback(ctx, cq.ID, "")

	if err := a.sendNextRankingPrompt(ctx, weekID, participantID); err != nil {
		log.Printf("bot: failed to advance ranking: %v", err)
	}
}

func parseRankingCallbackData(data string) (weekID, participantID, submissionID int64, err error) {
	rest := strings.TrimPrefix(data, rankingCallbackPrefix)
	parts := strings.Split(rest, "|")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("expected 3 fields, got %d", len(parts))
	}
	weekID, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	participantID, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	submissionID, err = strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	return weekID, participantID, submissionID, nil
}

// ProcessResultsNotifications drains the partial-progress and final-results
// outbox actions (R25, R26).
func (a *App) ProcessResultsNotifications(ctx context.Context) error {
	if err := contest.ProcessPartialNotices(ctx, a.DB, a); err != nil {
		return fmt.Errorf("bot: process partial notices: %w", err)
	}
	if err := contest.PublishDueResults(ctx, a.DB, a); err != nil {
		return fmt.Errorf("bot: publish due results: %w", err)
	}
	return nil
}
