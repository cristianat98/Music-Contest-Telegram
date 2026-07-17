package bot

import (
	"context"
	"errors"
	"strconv"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/cristianat98/Music-Contest-Telegram/internal/contest"
)

// commandArgs returns the text following the /command token, trimmed.
func commandArgs(text string) string {
	parts := strings.SplitN(text, " ", 2)
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// parseContestDurationArgs splits /startcontest's raw args into a name and,
// only when the last two whitespace-separated tokens both parse as
// integers, the songs/results phase durations (KTD3: both or neither -- a
// lone trailing number stays part of the name, since one token alone can't
// disambiguate a duration from a name that happens to end in a digit).
// Positivity is validated separately by the caller (KTD5) so recognition
// and validation stay independent steps.
func parseContestDurationArgs(args string) (name string, songsDays, resultsDays *int) {
	tokens := strings.Fields(args)
	if len(tokens) >= 3 {
		if s, errS := strconv.Atoi(tokens[len(tokens)-2]); errS == nil {
			if r, errR := strconv.Atoi(tokens[len(tokens)-1]); errR == nil {
				return strings.Join(tokens[:len(tokens)-2], " "), &s, &r
			}
		}
	}
	return strings.Join(tokens, " "), nil, nil
}

func (a *App) handleStartContest(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	args := commandArgs(update.Message.Text)
	if args == "" {
		a.reply(ctx, update.Message.Chat.ID, "Usage: /startcontest <name> [songsDays resultsDays]")
		return
	}

	name, songsDays, resultsDays := parseContestDurationArgs(args)
	if name == "" {
		a.reply(ctx, update.Message.Chat.ID, "Usage: /startcontest <name> [songsDays resultsDays]")
		return
	}
	if (songsDays != nil && *songsDays < 1) || (resultsDays != nil && *resultsDays < 1) {
		a.reply(ctx, update.Message.Chat.ID, "Usage: /startcontest <name> [songsDays resultsDays] (both must be positive integers, day 1 = the day the phase opened)")
		return
	}

	msg, err := a.Contest.StartContest(ctx, name, songsDays, resultsDays)
	if err != nil {
		a.reply(ctx, update.Message.Chat.ID, "Could not start contest: "+err.Error())
		return
	}
	a.reply(ctx, update.Message.Chat.ID, msg)
}

func (a *App) handleFinishContest(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	msg, err := a.Contest.FinishContest(ctx)
	if err != nil {
		a.reply(ctx, update.Message.Chat.ID, "Could not finish contest: "+lifecycleErrorText(err))
		return
	}
	a.reply(ctx, update.Message.Chat.ID, msg)
}

func (a *App) handleStartWeek(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	msg, err := a.Contest.StartWeek(ctx)
	if err != nil {
		a.reply(ctx, update.Message.Chat.ID, "Could not start week: "+lifecycleErrorText(err))
		return
	}
	a.reply(ctx, update.Message.Chat.ID, msg)
}

func (a *App) handleModifyLimit(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	arg := commandArgs(update.Message.Text)
	days, err := strconv.Atoi(arg)
	if err != nil || days < 1 {
		a.reply(ctx, update.Message.Chat.ID, "Usage: /modifylimit <days> (positive integer, day 1 = the day the state opened)")
		return
	}

	msg, err := a.Contest.ModifyLimit(ctx, days)
	if err != nil {
		a.reply(ctx, update.Message.Chat.ID, "Could not modify limit: "+lifecycleErrorText(err))
		return
	}
	a.reply(ctx, update.Message.Chat.ID, msg)
}

func (a *App) handleForceAdvance(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	msg, err := a.Contest.ForceAdvance(ctx)
	if err != nil {
		a.reply(ctx, update.Message.Chat.ID, "Could not force advance: "+lifecycleErrorText(err))
		return
	}
	a.reply(ctx, update.Message.Chat.ID, msg)
}

// lifecycleErrorText maps the contest package's sentinel errors to their
// own message text, falling back to the wrapped error's text for anything
// unexpected (e.g. a DB error).
func lifecycleErrorText(err error) string {
	for _, sentinel := range []error{
		contest.ErrNoActiveContest,
		contest.ErrWeekInProgress,
		contest.ErrNotEnoughEligible,
		contest.ErrNoTopicsAvailable,
		contest.ErrNoActiveState,
		contest.ErrWeekNotIdle,
	} {
		if errors.Is(err, sentinel) {
			return sentinel.Error()
		}
	}
	return err.Error()
}
