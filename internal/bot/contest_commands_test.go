package bot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/cristianat98/Music-Contest-Telegram/internal/contest"
	"github.com/cristianat98/Music-Contest-Telegram/internal/storage"
)

func TestCommandArgs(t *testing.T) {
	cases := map[string]string{
		"/startcontest Summer Jam": "Summer Jam",
		"/startcontest":            "",
		"/startweek":               "",
		"/modifylimit 3":           "3",
	}
	for input, want := range cases {
		if got := commandArgs(input); got != want {
			t.Errorf("commandArgs(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLifecycleErrorText_KnownSentinel(t *testing.T) {
	got := lifecycleErrorText(contest.ErrNotEnoughEligible)
	if got != contest.ErrNotEnoughEligible.Error() {
		t.Errorf("lifecycleErrorText() = %q, want %q", got, contest.ErrNotEnoughEligible.Error())
	}
}

// setupCommandApp builds an App wired with a real contest.Engine and a fake
// Telegram server (always reporting the sender as an admin, since AdminOnly
// isn't under test here), seeding eligibleCount active participants and one
// topic. Returns the app and a pointer to the last text sent via reply.
func setupCommandApp(t *testing.T, eligibleCount int) (*App, *string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("storage.Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })

	engine := contest.NewEngine(db)
	engine.SetSongsHooks(contest.NewSongsHooks(db))
	engine.SetResultsHooks(contest.NewResultsHooks(db))

	srv, lastText := fakeTelegramServer(t, models.ChatMemberTypeAdministrator)
	t.Cleanup(srv.Close)

	app := &App{DB: db, ChatID: -100, SelfID: 999, Contest: engine, TG: newTestTGBot(t, srv.URL)}

	for i := 0; i < eligibleCount; i++ {
		if _, err := db.Exec(
			"INSERT INTO participants (telegram_user_id, display_name, active) VALUES (?, ?, 1)",
			3000+i, fmtUser(i),
		); err != nil {
			t.Fatalf("seed participant: %v", err)
		}
	}
	if _, err := db.Exec("INSERT INTO topics (text) VALUES ('topic-a')"); err != nil {
		t.Fatalf("seed topic: %v", err)
	}
	return app, lastText
}

func TestHandleStartContest_EmptyName_UsageMessage(t *testing.T) {
	app, lastText := setupCommandApp(t, 0)
	app.handleStartContest(context.Background(), app.TG, messageUpdate(1, -100, "/startcontest"))
	if !strings.Contains(*lastText, "Usage") {
		t.Errorf("lastText = %q, want it to mention Usage", *lastText)
	}
}

func TestHandleStartContest_Success(t *testing.T) {
	app, lastText := setupCommandApp(t, 0)
	app.handleStartContest(context.Background(), app.TG, messageUpdate(1, -100, "/startcontest Summer Jam"))
	if !strings.Contains(*lastText, "Summer Jam") {
		t.Errorf("lastText = %q, want it to mention the contest name", *lastText)
	}
	var count int
	if err := app.DB.QueryRow("SELECT COUNT(*) FROM contests WHERE name = ?", "Summer Jam").Scan(&count); err != nil {
		t.Fatalf("count contests: %v", err)
	}
	if count != 1 {
		t.Errorf("contests row count = %d, want 1", count)
	}
}

func TestParseContestDurationArgs(t *testing.T) {
	cases := []struct {
		args               string
		wantName           string
		wantSongs, wantRes *int
	}{
		{"Season 2 3 5", "Season 2", intPtr(3), intPtr(5)},                             // KTD3: both trailing tokens parse -> durations
		{"Season 2", "Season 2", nil, nil},                                             // no trailing numbers at all
		{"Top 40", "Top 40", nil, nil},                                                 // single trailing digit stays part of the name
		{"Season 2 0 5", "Season 2", intPtr(0), intPtr(5)},                             // recognized as durations; positivity validated by the caller (KTD5)
		{"Eurovision Special 10 2026", "Eurovision Special", intPtr(10), intPtr(2026)}, // KTD3's documented limitation: a name ending in two integers is silently parsed as durations
	}
	for _, c := range cases {
		name, songs, res := parseContestDurationArgs(c.args)
		if name != c.wantName {
			t.Errorf("parseContestDurationArgs(%q) name = %q, want %q", c.args, name, c.wantName)
		}
		if !intPtrEqual(songs, c.wantSongs) {
			t.Errorf("parseContestDurationArgs(%q) songsDays = %v, want %v", c.args, derefOrNil(songs), derefOrNil(c.wantSongs))
		}
		if !intPtrEqual(res, c.wantRes) {
			t.Errorf("parseContestDurationArgs(%q) resultsDays = %v, want %v", c.args, derefOrNil(res), derefOrNil(c.wantRes))
		}
	}
}

func intPtr(n int) *int { return &n }

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func derefOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestHandleStartContest_WithDurations_EchoesBackAndPersists(t *testing.T) {
	app, lastText := setupCommandApp(t, 0)
	app.handleStartContest(context.Background(), app.TG, messageUpdate(1, -100, "/startcontest Season 2 3 5"))
	if !strings.Contains(*lastText, "3") || !strings.Contains(*lastText, "5") {
		t.Errorf("lastText = %q, want it to echo back both resolved durations", *lastText)
	}

	var songsDays, resultsDays int
	if err := app.DB.QueryRow(
		"SELECT songs_deadline_days, results_deadline_days FROM contests WHERE name = ?", "Season 2",
	).Scan(&songsDays, &resultsDays); err != nil {
		t.Fatalf("query contest durations: %v", err)
	}
	if songsDays != 3 || resultsDays != 5 {
		t.Errorf("stored durations = (%d, %d), want (3, 5)", songsDays, resultsDays)
	}
}

func TestHandleStartContest_NonPositiveDuration_UsageMessage_NoRowInserted(t *testing.T) {
	app, lastText := setupCommandApp(t, 0)
	app.handleStartContest(context.Background(), app.TG, messageUpdate(1, -100, "/startcontest Season 2 0 5"))
	if !strings.Contains(*lastText, "Usage") {
		t.Errorf("lastText = %q, want it to mention Usage", *lastText)
	}
	var count int
	if err := app.DB.QueryRow("SELECT COUNT(*) FROM contests").Scan(&count); err != nil {
		t.Fatalf("count contests: %v", err)
	}
	if count != 0 {
		t.Errorf("contests row count = %d, want 0 (rejected before insert)", count)
	}
}

func TestHandleFinishContest_WeekNotIdle_Error(t *testing.T) {
	app, lastText := setupCommandApp(t, 2)
	ctx := context.Background()
	if _, err := app.Contest.StartContest(ctx, "C"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := app.Contest.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	app.handleFinishContest(ctx, app.TG, messageUpdate(1, -100, "/finishcontest"))

	if !strings.Contains(*lastText, contest.ErrWeekNotIdle.Error()) {
		t.Errorf("lastText = %q, want it to mention %v", *lastText, contest.ErrWeekNotIdle)
	}
}

func TestHandleFinishContest_Success(t *testing.T) {
	app, lastText := setupCommandApp(t, 0)
	ctx := context.Background()
	if _, err := app.Contest.StartContest(ctx, "C"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}

	app.handleFinishContest(ctx, app.TG, messageUpdate(1, -100, "/finishcontest"))

	if !strings.Contains(*lastText, "finished") {
		t.Errorf("lastText = %q, want it to mention finished", *lastText)
	}
}

func TestHandleStartWeek_NotEnoughEligible_Error(t *testing.T) {
	app, lastText := setupCommandApp(t, 1)
	ctx := context.Background()
	if _, err := app.Contest.StartContest(ctx, "C"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}

	app.handleStartWeek(ctx, app.TG, messageUpdate(1, -100, "/startweek"))

	if !strings.Contains(*lastText, contest.ErrNotEnoughEligible.Error()) {
		t.Errorf("lastText = %q, want it to mention %v", *lastText, contest.ErrNotEnoughEligible)
	}
}

func TestHandleStartWeek_Success(t *testing.T) {
	app, lastText := setupCommandApp(t, 2)
	ctx := context.Background()
	if _, err := app.Contest.StartContest(ctx, "C"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}

	app.handleStartWeek(ctx, app.TG, messageUpdate(1, -100, "/startweek"))

	if !strings.Contains(*lastText, "Songs collection open") {
		t.Errorf("lastText = %q, want it to mention songs collection opening", *lastText)
	}
}

func TestHandleModifyLimit_InvalidArg_UsageMessage(t *testing.T) {
	app, lastText := setupCommandApp(t, 0)
	app.handleModifyLimit(context.Background(), app.TG, messageUpdate(1, -100, "/modifylimit abc"))
	if !strings.Contains(*lastText, "Usage") {
		t.Errorf("lastText = %q, want it to mention Usage", *lastText)
	}
}

func TestHandleModifyLimit_NoActiveState_Error(t *testing.T) {
	app, lastText := setupCommandApp(t, 0)
	ctx := context.Background()
	if _, err := app.Contest.StartContest(ctx, "C"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}

	app.handleModifyLimit(ctx, app.TG, messageUpdate(1, -100, "/modifylimit 3"))

	if !strings.Contains(*lastText, contest.ErrNoActiveState.Error()) {
		t.Errorf("lastText = %q, want it to mention %v", *lastText, contest.ErrNoActiveState)
	}
}

func TestHandleModifyLimit_Success(t *testing.T) {
	app, lastText := setupCommandApp(t, 2)
	ctx := context.Background()
	if _, err := app.Contest.StartContest(ctx, "C"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := app.Contest.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	app.handleModifyLimit(ctx, app.TG, messageUpdate(1, -100, "/modifylimit 3"))

	if !strings.Contains(*lastText, "Deadline") {
		t.Errorf("lastText = %q, want it to mention the updated deadline", *lastText)
	}
}

func TestHandleForceAdvance_NoActiveState_Error(t *testing.T) {
	app, lastText := setupCommandApp(t, 0)
	ctx := context.Background()
	if _, err := app.Contest.StartContest(ctx, "C"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}

	app.handleForceAdvance(ctx, app.TG, messageUpdate(1, -100, "/forceadvance"))

	if !strings.Contains(*lastText, contest.ErrNoActiveState.Error()) {
		t.Errorf("lastText = %q, want it to mention %v", *lastText, contest.ErrNoActiveState)
	}
}

func TestHandleForceAdvance_Success(t *testing.T) {
	app, lastText := setupCommandApp(t, 2)
	ctx := context.Background()
	if _, err := app.Contest.StartContest(ctx, "C"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := app.Contest.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	app.handleForceAdvance(ctx, app.TG, messageUpdate(1, -100, "/forceadvance"))

	if !strings.Contains(*lastText, "Advanced") {
		t.Errorf("lastText = %q, want it to mention Advanced", *lastText)
	}
}
