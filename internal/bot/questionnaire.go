package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/cristianat98/Music-Contest-Telegram/internal/contest"
)

const quizCallbackPrefix = "q|"

// ProcessResultsPrompts dequeues start_results_prompt outbox rows (enqueued
// by ResultsHooks.OpenResultsCollection when results_collection opens) and
// sends each required participant their first familiarity question (R21).
// The questionnaire runs before the ranking flow; sendNextQuizQuestion
// chains into sendNextRankingPrompt once a participant's questions are
// exhausted.
func (a *App) ProcessResultsPrompts(ctx context.Context) error {
	type pending struct {
		id      int64
		payload string
	}
	rows, err := a.DB.QueryContext(ctx, `
		SELECT id, payload_json FROM outbox_actions WHERE action_type = ? AND status IN ('pending', 'in_progress')
	`, contest.OutboxActionStartResultsPrompt)
	if err != nil {
		return fmt.Errorf("bot: list pending start_results_prompt actions: %w", err)
	}
	var actions []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.payload); err != nil {
			rows.Close()
			return fmt.Errorf("bot: scan start_results_prompt action: %w", err)
		}
		actions = append(actions, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, act := range actions {
		if err := a.processStartResultsPromptAction(ctx, act.id, act.payload); err != nil {
			log.Printf("bot: start_results_prompt action %d failed: %v", act.id, err)
			continue
		}
	}
	return nil
}

func (a *App) processStartResultsPromptAction(ctx context.Context, actionID int64, payloadJSON string) error {
	weekID, participantID, err := parseWeekParticipantPayload(payloadJSON)
	if err != nil {
		return err
	}

	if _, err := a.DB.ExecContext(ctx, `UPDATE outbox_actions SET status = 'in_progress' WHERE id = ?`, actionID); err != nil {
		return fmt.Errorf("bot: mark start_results_prompt in_progress: %w", err)
	}

	if err := a.sendNextQuizQuestion(ctx, weekID, participantID); err != nil {
		return err
	}

	if _, err := a.DB.ExecContext(ctx, `
		UPDATE outbox_actions SET status = 'done', completed_at = datetime('now') WHERE id = ?
	`, actionID); err != nil {
		return fmt.Errorf("bot: mark start_results_prompt done: %w", err)
	}
	return nil
}

func (a *App) sendNextQuizQuestion(ctx context.Context, weekID, participantID int64) error {
	pending, err := contest.PendingQuizSubmissions(ctx, a.DB, weekID, participantID)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return a.sendNextRankingPrompt(ctx, weekID, participantID)
	}

	telegramUserID, err := a.telegramUserIDByParticipantID(ctx, participantID)
	if err != nil {
		return err
	}

	next := pending[0]
	text := fmt.Sprintf("Song %d: %s\nDid you already know this song before it was submitted?", next.DisplayOrder, next.URL)
	keyboard := models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "Yes", CallbackData: quizCallbackData(weekID, participantID, next.ID, true)},
			{Text: "No", CallbackData: quizCallbackData(weekID, participantID, next.ID, false)},
		}},
	}

	_, err = a.TG.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      telegramUserID,
		Text:        text,
		ReplyMarkup: keyboard,
	})
	if err != nil {
		return fmt.Errorf("bot: send quiz question: %w", err)
	}
	return nil
}

func quizCallbackData(weekID, participantID, submissionID int64, knew bool) string {
	k := "0"
	if knew {
		k = "1"
	}
	return fmt.Sprintf("%s%d|%d|%d|%s", quizCallbackPrefix, weekID, participantID, submissionID, k)
}

// handleQuizAnswerCallback records a familiarity answer and advances the
// questionnaire, then the ranking flow once questions are exhausted.
func (a *App) handleQuizAnswerCallback(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	cq := update.CallbackQuery
	weekID, participantID, submissionID, knew, err := parseQuizCallbackData(cq.Data)
	if err != nil {
		log.Printf("bot: malformed quiz callback data %q: %v", cq.Data, err)
		return
	}

	if !a.callbackSenderMatchesParticipant(ctx, cq.From.ID, participantID) {
		a.answerCallback(ctx, cq.ID, "This isn't your question to answer.")
		return
	}

	if err := contest.RecordQuizAnswer(ctx, a.DB, weekID, participantID, submissionID, knew); err != nil {
		log.Printf("bot: failed to record quiz answer: %v", err)
		a.answerCallback(ctx, cq.ID, "Could not record your answer. Please try again.")
		return
	}
	a.answerCallback(ctx, cq.ID, "")

	if err := a.sendNextQuizQuestion(ctx, weekID, participantID); err != nil {
		log.Printf("bot: failed to advance questionnaire: %v", err)
	}
}

func (a *App) answerCallback(ctx context.Context, callbackQueryID, text string) {
	if _, err := a.TG.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackQueryID,
		Text:            text,
	}); err != nil {
		log.Printf("bot: failed to answer callback query: %v", err)
	}
}

func (a *App) telegramUserIDByParticipantID(ctx context.Context, participantID int64) (int64, error) {
	var id int64
	err := a.DB.QueryRowContext(ctx, `SELECT telegram_user_id FROM participants WHERE id = ?`, participantID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("bot: look up telegram_user_id for participant %d: %w", participantID, err)
	}
	return id, nil
}

func (a *App) callbackSenderMatchesParticipant(ctx context.Context, senderTelegramID, participantID int64) bool {
	telegramUserID, err := a.telegramUserIDByParticipantID(ctx, participantID)
	if err != nil {
		return false
	}
	return telegramUserID == senderTelegramID
}

func parseWeekParticipantPayload(payloadJSON string) (weekID, participantID int64, err error) {
	var payload struct {
		WeekID        int64 `json:"week_id"`
		ParticipantID int64 `json:"participant_id"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return 0, 0, err
	}
	return payload.WeekID, payload.ParticipantID, nil
}

func parseQuizCallbackData(data string) (weekID, participantID, submissionID int64, knew bool, err error) {
	rest := strings.TrimPrefix(data, quizCallbackPrefix)
	parts := strings.Split(rest, "|")
	if len(parts) != 4 {
		return 0, 0, 0, false, fmt.Errorf("expected 4 fields, got %d", len(parts))
	}
	weekID, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, 0, false, err
	}
	participantID, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, 0, false, err
	}
	submissionID, err = strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, 0, 0, false, err
	}
	knew = parts[3] == "1"
	return weekID, participantID, submissionID, knew, nil
}
