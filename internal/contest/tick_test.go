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

func TestTick_ResultsCollectionComplete_AdvancesToIdle(t *testing.T) {
	e, db, weekID, ids := setupResultsCollectionWeek(t, 2)
	ctx := context.Background()

	for _, p := range ids {
		answerAllQuizzes(t, ctx, db, weekID, p, false)
		rankAllRemaining(t, ctx, db, weekID, p)
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
