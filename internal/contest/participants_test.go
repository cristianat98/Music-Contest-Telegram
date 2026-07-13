package contest

import (
	"context"
	"testing"
)

func TestStartContest_EnrollsOnlyActiveParticipants(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)

	seedParticipants(t, db, 2)
	if _, err := db.Exec(
		"INSERT INTO participants (telegram_user_id, display_name, active) VALUES (?, ?, 0)",
		2000, "inactive-user",
	); err != nil {
		t.Fatalf("seed inactive participant: %v", err)
	}

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}

	var contestID int64
	if err := db.QueryRow("SELECT id FROM contests WHERE active = 1").Scan(&contestID); err != nil {
		t.Fatalf("query active contest: %v", err)
	}

	var enrolledCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM contest_participants WHERE contest_id = ? AND left_at IS NULL", contestID,
	).Scan(&enrolledCount); err != nil {
		t.Fatalf("count enrolled participants: %v", err)
	}
	if enrolledCount != 2 {
		t.Errorf("enrolled count = %d, want 2 (only active participants)", enrolledCount)
	}

	var inactiveEnrolled int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM contest_participants cp JOIN participants p ON p.id = cp.participant_id WHERE p.telegram_user_id = 2000",
	).Scan(&inactiveEnrolled); err != nil {
		t.Fatalf("check inactive participant enrollment: %v", err)
	}
	if inactiveEnrolled != 0 {
		t.Error("inactive participant should not have been enrolled")
	}
}

func TestStrikesForParticipant_CleanRecord(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)
	seedTopic(t, db, "topic-a")

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	var contestID, participantID int64
	db.QueryRow("SELECT id FROM contests WHERE active = 1").Scan(&contestID)
	db.QueryRow("SELECT id FROM participants LIMIT 1").Scan(&participantID)

	strikes, err := StrikesForParticipant(ctx, db, contestID, participantID)
	if err != nil {
		t.Fatalf("StrikesForParticipant() error = %v", err)
	}
	if strikes != 0 {
		t.Errorf("strikes = %d, want 0 for a participant with no weeks yet", strikes)
	}
}

func TestStrikesForParticipant_ExcludesFutureWeek(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 3)
	seedTopic(t, db, "topic-a")

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	var contestID, participantID int64
	db.QueryRow("SELECT id FROM contests WHERE active = 1").Scan(&contestID)
	db.QueryRow("SELECT id FROM participants LIMIT 1").Scan(&participantID)

	// Mark the participant as having left before any week was ever created.
	// left_at must use the same SQLite datetime() format as weeks.created_at
	// (both are SQL-generated, unlike weeks.state_started_at which the Go
	// code writes in RFC3339) for the string comparison in
	// StrikesForParticipant to compare correctly.
	if _, err := db.Exec(
		"UPDATE contest_participants SET left_at = datetime('now', '-1 hour') WHERE contest_id = ? AND participant_id = ?",
		contestID, participantID,
	); err != nil {
		t.Fatalf("seed left_at: %v", err)
	}

	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}
	if _, err := e.ForceAdvance(ctx); err != nil {
		t.Fatalf("ForceAdvance() error = %v", err)
	}

	strikes, err := StrikesForParticipant(ctx, db, contestID, participantID)
	if err != nil {
		t.Fatalf("StrikesForParticipant() error = %v", err)
	}
	if strikes != 0 {
		t.Errorf("strikes = %d, want 0 (participant left before this week was created, so not obligated)", strikes)
	}
}

func TestStrikesForParticipant_LeftMidWeek_StillStruckForThatWeek(t *testing.T) {
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

	var contestID, participantID int64
	db.QueryRow("SELECT id FROM contests WHERE active = 1").Scan(&contestID)
	db.QueryRow("SELECT id FROM participants LIMIT 1").Scan(&participantID)

	// Participant leaves after the week already started (created_at is
	// earlier than left_at), so they were obligated for this week.
	if _, err := db.Exec(
		"UPDATE contest_participants SET left_at = datetime('now', '+1 second') WHERE contest_id = ? AND participant_id = ?",
		contestID, participantID,
	); err != nil {
		t.Fatalf("seed left_at: %v", err)
	}

	if _, err := e.ForceAdvance(ctx); err != nil {
		t.Fatalf("ForceAdvance() error = %v", err)
	}
	if _, err := e.ForceAdvance(ctx); err != nil {
		t.Fatalf("second ForceAdvance() error = %v", err)
	}

	strikes, err := StrikesForParticipant(ctx, db, contestID, participantID)
	if err != nil {
		t.Fatalf("StrikesForParticipant() error = %v", err)
	}
	if strikes == 0 {
		t.Error("expected a strike for the week that was already open when the participant left")
	}
}

func TestForcedClose_IgnoresInactiveParticipant(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)
	seedTopic(t, db, "topic-a")
	e.SetSongsHooks(NewSongsHooks(db))

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	var contestID, firstParticipant, secondParticipant int64
	db.QueryRow("SELECT id FROM contests WHERE active = 1").Scan(&contestID)
	rows, _ := db.Query("SELECT id FROM participants ORDER BY id")
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	firstParticipant, secondParticipant = ids[0], ids[1]

	seedSubmission(t, db, mustCurrentWeekID(t, ctx, e), firstParticipant, "https://youtu.be/abc")

	if _, err := db.Exec(
		"UPDATE contest_participants SET left_at = datetime('now') WHERE contest_id = ? AND participant_id = ?",
		contestID, secondParticipant,
	); err != nil {
		t.Fatalf("mark second participant inactive: %v", err)
	}

	hooks := NewSongsHooks(db)
	weekID := mustCurrentWeekID(t, ctx, e)
	complete, err := hooks.SongsCollectionComplete(ctx, db, weekID)
	if err != nil {
		t.Fatalf("SongsCollectionComplete() error = %v", err)
	}
	if !complete {
		t.Error("expected complete: only active participant is required, and they submitted")
	}
}

func mustCurrentWeekID(t *testing.T, ctx context.Context, e *Engine) int64 {
	t.Helper()
	week, err := e.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}
	return week.ID
}
