package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/cristianat98/Music-Contest-Telegram/internal/bot"
	"github.com/cristianat98/Music-Contest-Telegram/internal/config"
	"github.com/cristianat98/Music-Contest-Telegram/internal/contest"
	"github.com/cristianat98/Music-Contest-Telegram/internal/storage"
)

// tickInterval drives reminders, deadline checks, and state transitions
// (R30): a periodic re-evaluation rather than per-event triggers, so a
// crash-and-restart never depends on having seen a specific event.
const tickInterval = 15 * time.Minute

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	db, err := storage.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := bot.New(ctx, cfg.BotToken, cfg.ChatID, db)
	if err != nil {
		log.Fatalf("failed to create bot: %v", err)
	}

	go runTickLoop(ctx, app)

	log.Println("musiccontestbot started")
	app.Start(ctx) // blocks until ctx is canceled by SIGINT/SIGTERM

	log.Println("shutting down: checkpointing WAL and closing database")
	if err := storage.Checkpoint(db); err != nil {
		log.Printf("checkpoint error: %v", err)
	}
	if err := db.Close(); err != nil {
		log.Printf("db close error: %v", err)
	}
}

// runTickLoop runs the tick immediately on startup (so a restart doesn't
// wait up to tickInterval to reconcile state) and then every tickInterval
// until ctx is canceled.
func runTickLoop(ctx context.Context, app *bot.App) {
	runTick(ctx, app)

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runTick(ctx, app)
		}
	}
}

// runTick fans out to every per-unit tick action. Each call is independent
// and logs its own failure rather than aborting the rest, since a stall in
// one (e.g. a Telegram API error) shouldn't block the others.
func runTick(ctx context.Context, app *bot.App) {
	if err := app.Contest.Tick(ctx); err != nil {
		log.Printf("tick: lifecycle error: %v", err)
	}
	if err := contest.SendDueReminders(ctx, app.DB, app, time.Now()); err != nil {
		log.Printf("tick: reminders error: %v", err)
	}
	if err := contest.PublishDueSongs(ctx, app.DB, app); err != nil {
		log.Printf("tick: publish songs error: %v", err)
	}
	if err := contest.ProcessEarlyFinishNotices(ctx, app.DB, app); err != nil {
		log.Printf("tick: early-finish notices error: %v", err)
	}
	if err := app.ProcessResultsPrompts(ctx); err != nil {
		log.Printf("tick: results prompts error: %v", err)
	}
	if err := app.ProcessResultsNotifications(ctx); err != nil {
		log.Printf("tick: results notifications error: %v", err)
	}
	if err := app.CheckAdminStatus(ctx); err != nil {
		log.Printf("tick: admin status check error: %v", err)
	}
}
