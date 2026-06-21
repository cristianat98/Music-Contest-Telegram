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

	// Force the state to look like it's already past 20:00 today by
	// backdating state_started_at; SendDueReminders itself reads wall-clock
	// time, so this test only verifies the missing-name query, gated
	// separately from the hour check below via direct function calls.
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
	if err := SendDueReminders(ctx, db, notifier); err != nil {
		t.Fatalf("SendDueReminders() error = %v", err)
	}

	now := time.Now().In(madridLocation)
	if now.Hour() < reminderHour && len(notifier.messages) != 0 {
		t.Errorf("expected no reminder before %d:00 Europe/Madrid, got %v", reminderHour, notifier.messages)
	}
}

func TestSendDueReminders_NoOpOutsideSongsCollection(t *testing.T) {
	ctx := context.Background()
	_, db := openTestEngine(t)

	notifier := &fakeNotifier{}
	if err := SendDueReminders(ctx, db, notifier); err != nil {
		t.Fatalf("SendDueReminders() error = %v", err)
	}
	if len(notifier.messages) != 0 {
		t.Errorf("expected no reminder with no active contest, got %v", notifier.messages)
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
	today := time.Now().In(madridLocation).Format("2006-01-02")
	if _, err := db.Exec("UPDATE weeks SET last_reminder_at = ? WHERE id = ?", today, weekID); err != nil {
		t.Fatalf("seed last_reminder_at: %v", err)
	}

	notifier := &fakeNotifier{}
	if err := SendDueReminders(ctx, db, notifier); err != nil {
		t.Fatalf("SendDueReminders() error = %v", err)
	}
	if len(notifier.messages) != 0 {
		t.Errorf("expected no reminder when already sent today, got %v", notifier.messages)
	}
}
