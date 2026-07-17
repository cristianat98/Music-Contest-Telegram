package contest

import (
	"context"
	"fmt"
	"testing"
)

func TestTick_NoActiveContest_NoError(t *testing.T) {
	ctx := context.Background()
	e, _ := openTestEngine(t)

	if err := e.Tick(ctx); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
}

func TestTick_IdleWeek_NoOp(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)
	seedTopic(t, db, "topic-a")

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}

	if err := e.Tick(ctx); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
}

// TestTick_DefaultNoopHooks_NeverCompletes exercises the noopSongsHooks
// default installed by NewEngine before any SetSongsHooks call, proving
// Tick safely no-ops rather than panicking or wrongly advancing.
func TestTick_DefaultNoopHooks_NeverCompletes(t *testing.T) {
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

	if err := e.Tick(ctx); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}

	var state string
	if err := db.QueryRow("SELECT state FROM weeks ORDER BY id DESC LIMIT 1").Scan(&state); err != nil {
		t.Fatalf("query week state: %v", err)
	}
	if state != StateSongsCollection {
		t.Errorf("week state = %q, want %q (default no-op hooks never report complete)", state, StateSongsCollection)
	}
}

func TestTick_SongsCollectionComplete_AdvancesToResultsCollection(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)
	seedTopic(t, db, "topic-a")
	e.SetSongsHooks(NewSongsHooks(db))
	e.SetResultsHooks(NewResultsHooks(db))

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	week, err := e.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}

	rows, err := db.Query("SELECT id FROM participants ORDER BY id")
	if err != nil {
		t.Fatalf("query participants: %v", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan participant id: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	for i, id := range ids {
		seedSubmission(t, db, week.ID, id, fmt.Sprintf("https://youtu.be/song%d", i))
	}

	// Backdate state_started_at so this phase's deadline has already
	// passed -- Tick() now requires both completion and the deadline,
	// not completion alone (U4).
	if _, err := db.Exec(`UPDATE weeks SET state_started_at = ? WHERE id = ?`, "2000-01-01T00:00:00+01:00", week.ID); err != nil {
		t.Fatalf("backdate state_started_at: %v", err)
	}

	if err := e.Tick(ctx); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}

	got, err := e.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}
	if got.State != StateResultsCollection {
		t.Errorf("week state = %q, want %q after Tick detects natural completion", got.State, StateResultsCollection)
	}
}

// TestTick_CompleteBeforeDeadline_DoesNotAdvance covers AE1: everyone
// finishes early, but Tick() must wait for the deadline rather than
// publishing on completion alone (R4).
func TestTick_CompleteBeforeDeadline_DoesNotAdvance(t *testing.T) {
	ctx := context.Background()
	// state_started_at is left at "now" by seedCompleteSongsWeek (the
	// default DefaultDeadlineDays=4 deadline is days away) -- everyone's
	// submitted, but the phase isn't due.
	e, _, _ := seedCompleteSongsWeek(t)

	if err := e.Tick(ctx); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}

	got, err := e.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}
	if got.State != StateSongsCollection {
		t.Errorf("week state = %q, want %q (complete but deadline not reached must not publish)", got.State, StateSongsCollection)
	}
}

// TestTick_IncompleteAfterDeadline_DoesNotAdvance covers F2/AE2's other
// half: the deadline passing with a straggler still missing keeps waiting
// rather than force-closing -- unchanged from before this gate existed.
func TestTick_IncompleteAfterDeadline_DoesNotAdvance(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)
	seedTopic(t, db, "topic-a")
	e.SetSongsHooks(NewSongsHooks(db))
	e.SetResultsHooks(NewResultsHooks(db))

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	week, err := e.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}
	if _, err := db.Exec(`UPDATE weeks SET state_started_at = ? WHERE id = ?`, "2000-01-01T00:00:00+01:00", week.ID); err != nil {
		t.Fatalf("backdate state_started_at: %v", err)
	}
	// No submissions seeded -- songs_collection is not complete.

	if err := e.Tick(ctx); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}

	got, err := e.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}
	if got.State != StateSongsCollection {
		t.Errorf("week state = %q, want %q (deadline passed but still incomplete must not publish)", got.State, StateSongsCollection)
	}
}

// TestForceAdvance_BypassesDeadlineGate covers R7: /forceadvance still
// closes the phase immediately even when it is complete but not yet due,
// unaffected by U4's gating.
func TestForceAdvance_BypassesDeadlineGate(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)
	seedTopic(t, db, "topic-a")
	e.SetSongsHooks(NewSongsHooks(db))
	e.SetResultsHooks(NewResultsHooks(db))

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}
	// No submissions -- incomplete, and deadline days away -- neither
	// condition Tick() requires holds, but ForceAdvance ignores both.

	if _, err := e.ForceAdvance(ctx); err != nil {
		t.Fatalf("ForceAdvance() error = %v", err)
	}

	got, err := e.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}
	if got.State != StateResultsCollection {
		t.Errorf("week state = %q, want %q (ForceAdvance bypasses completion and deadline)", got.State, StateResultsCollection)
	}
}

func TestTick_ResultsCollectionComplete_AdvancesToIdle(t *testing.T) {
	e, db, weekID, ids := setupResultsCollectionWeek(t, 2)
	ctx := context.Background()

	for _, p := range ids {
		answerAllQuizzes(t, ctx, db, weekID, p, false)
		rankAllRemaining(t, ctx, db, weekID, p)
	}

	// Backdate state_started_at so this phase's deadline has already
	// passed -- Tick() now requires both completion and the deadline,
	// not completion alone (U4).
	if _, err := db.Exec(`UPDATE weeks SET state_started_at = ? WHERE id = ?`, "2000-01-01T00:00:00+01:00", weekID); err != nil {
		t.Fatalf("backdate state_started_at: %v", err)
	}

	if err := e.Tick(ctx); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}

	got, err := e.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}
	if got.State != StateIdle {
		t.Errorf("week state = %q, want %q after Tick detects natural completion", got.State, StateIdle)
	}
}
