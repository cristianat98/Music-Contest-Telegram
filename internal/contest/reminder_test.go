package contest

import (
	"context"
	"testing"
	"time"
)

func TestSendDueReminders_NamesOnlyStillMissing(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)
	seedTopic(t, db, "topic-a")

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	var weekID int64
	db.QueryRow("SELECT id FROM weeks ORDER BY id DESC LIMIT 1").Scan(&weekID)
	var firstParticipant int64
	var firstName string
	db.QueryRow("SELECT id, display_name FROM participants ORDER BY id LIMIT 1").Scan(&firstParticipant, &firstName)
	seedSubmission(t, db, weekID, firstParticipant, "https://youtu.be/abc")

	missing, err := missingSubmitterNames(ctx, db, weekID)
	if err != nil {
		t.Fatalf("missingSubmitterNames() error = %v", err)
	}
	if len(missing) != 1 {
		t.Fatalf("missing = %v, want exactly 1 name", missing)
	}
	if missing[0] == firstName {
		t.Errorf("missing list should exclude the participant who already submitted, got %v", missing)
	}
}

func TestSendDueReminders_NoOpBeforeReminderHour(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)
	seedTopic(t, db, "topic-a")

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	notifier := &fakeNotifier{}
	before := time.Date(2026, 1, 15, 19, 0, 0, 0, madridLocation)
	if err := SendDueReminders(ctx, db, notifier, before); err != nil {
		t.Fatalf("SendDueReminders() error = %v", err)
	}
	if len(notifier.messages) != 0 {
		t.Errorf("expected no reminder before %d:00 Europe/Madrid, got %v", reminderHour, notifier.messages)
	}
}

func TestSendDueReminders_NoOpOutsideSongsCollection(t *testing.T) {
	ctx := context.Background()
	_, db := openTestEngine(t)

	notifier := &fakeNotifier{}
	if err := SendDueReminders(ctx, db, notifier, time.Now()); err != nil {
		t.Fatalf("SendDueReminders() error = %v", err)
	}
	if len(notifier.messages) != 0 {
		t.Errorf("expected no reminder with no active contest, got %v", notifier.messages)
	}
}

func TestSendDueReminders_SendsAfterReminderHour(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)
	seedTopic(t, db, "topic-a")

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	var weekID int64
	db.QueryRow("SELECT id FROM weeks ORDER BY id DESC LIMIT 1").Scan(&weekID)
	var firstParticipant int64
	db.QueryRow("SELECT id FROM participants ORDER BY id LIMIT 1").Scan(&firstParticipant)
	seedSubmission(t, db, weekID, firstParticipant, "https://youtu.be/abc")

	notifier := &fakeNotifier{}
	due := time.Date(2026, 1, 15, 21, 0, 0, 0, madridLocation)
	if err := SendDueReminders(ctx, db, notifier, due); err != nil {
		t.Fatalf("SendDueReminders() error = %v", err)
	}
	if len(notifier.messages) != 1 {
		t.Fatalf("messages sent = %d, want 1", len(notifier.messages))
	}

	var lastReminderAt string
	if err := db.QueryRow("SELECT last_reminder_at FROM weeks WHERE id = ?", weekID).Scan(&lastReminderAt); err != nil {
		t.Fatalf("query last_reminder_at: %v", err)
	}
	if lastReminderAt != "2026-01-15" {
		t.Errorf("last_reminder_at = %q, want %q", lastReminderAt, "2026-01-15")
	}
}

func TestSendDueReminders_SkipsIfAlreadySentToday(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)
	seedTopic(t, db, "topic-a")

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	var weekID int64
	db.QueryRow("SELECT id FROM weeks ORDER BY id DESC LIMIT 1").Scan(&weekID)
	if _, err := db.Exec("UPDATE weeks SET last_reminder_at = ? WHERE id = ?", "2026-01-15", weekID); err != nil {
		t.Fatalf("seed last_reminder_at: %v", err)
	}

	notifier := &fakeNotifier{}
	due := time.Date(2026, 1, 15, 21, 0, 0, 0, madridLocation)
	if err := SendDueReminders(ctx, db, notifier, due); err != nil {
		t.Fatalf("SendDueReminders() error = %v", err)
	}
	if len(notifier.messages) != 0 {
		t.Errorf("expected no reminder when already sent today, got %v", notifier.messages)
	}
}
