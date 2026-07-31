package contest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// seedCompleteSongsWeek starts a contest/week with 2 participants and has
// both submit, leaving state_started_at at "now" so the phase's default
// deadline is still days away.
func seedCompleteSongsWeek(t *testing.T) (*Engine, *sql.DB, int64) {
	t.Helper()
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

	rows, _ := db.Query("SELECT id FROM participants ORDER BY id")
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for i, id := range ids {
		seedSubmission(t, db, week.ID, id, fmt.Sprintf("https://youtu.be/song%d", i))
	}

	return e, db, week.ID
}

// TestEarlyFinishNotice_EnqueuedAndDrainedOnce covers AE1: completion
// before the deadline enqueues exactly one early-finish outbox row, and
// draining it sends exactly one group message naming the deadline.
func TestEarlyFinishNotice_EnqueuedAndDrainedOnce(t *testing.T) {
	ctx := context.Background()
	e, db, _ := seedCompleteSongsWeek(t)

	if err := e.Tick(ctx); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}

	var pendingCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ? AND status = 'pending'", OutboxActionSongsEarlyFinish,
	).Scan(&pendingCount); err != nil {
		t.Fatalf("query pending early-finish rows: %v", err)
	}
	if pendingCount != 1 {
		t.Fatalf("pending %s rows = %d, want 1", OutboxActionSongsEarlyFinish, pendingCount)
	}

	// The week must not have advanced -- completion alone isn't enough.
	got, err := e.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}
	if got.State != StateSongsCollection {
		t.Errorf("week state = %q, want %q (early finish must not publish)", got.State, StateSongsCollection)
	}

	notifier := &fakeNotifier{}
	if err := ProcessEarlyFinishNotices(ctx, db, notifier); err != nil {
		t.Fatalf("ProcessEarlyFinishNotices() error = %v", err)
	}
	if len(notifier.messages) != 1 {
		t.Fatalf("messages sent = %d, want 1", len(notifier.messages))
	}
	if !strings.Contains(notifier.messages[0], "songs") {
		t.Errorf("message = %q, want it to name the songs phase", notifier.messages[0])
	}

	var status string
	if err := db.QueryRow(
		"SELECT status FROM outbox_actions WHERE action_type = ?", OutboxActionSongsEarlyFinish,
	).Scan(&status); err != nil {
		t.Fatalf("query outbox status: %v", err)
	}
	if status != "done" {
		t.Errorf("outbox status = %q, want %q", status, "done")
	}
}

// TestEarlyFinishNotice_RepeatedTicksDoNotDuplicate covers AE6: Tick()
// re-evaluating the same complete-but-not-due phase on later cycles must
// not enqueue a second notice.
func TestEarlyFinishNotice_RepeatedTicksDoNotDuplicate(t *testing.T) {
	ctx := context.Background()
	e, db, _ := seedCompleteSongsWeek(t)

	for i := 0; i < 3; i++ {
		if err := e.Tick(ctx); err != nil {
			t.Fatalf("Tick() call %d error = %v", i, err)
		}
	}

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ?", OutboxActionSongsEarlyFinish,
	).Scan(&count); err != nil {
		t.Fatalf("query early-finish rows: %v", err)
	}
	if count != 1 {
		t.Errorf("early-finish outbox rows after 3 ticks = %d, want 1 (guarded insert must not duplicate)", count)
	}

	// Draining once, then ticking again, must not resend either.
	if err := ProcessEarlyFinishNotices(ctx, db, &fakeNotifier{}); err != nil {
		t.Fatalf("ProcessEarlyFinishNotices() error = %v", err)
	}
	if err := e.Tick(ctx); err != nil {
		t.Fatalf("Tick() after drain error = %v", err)
	}
	notifier := &fakeNotifier{}
	if err := ProcessEarlyFinishNotices(ctx, db, notifier); err != nil {
		t.Fatalf("second ProcessEarlyFinishNotices() error = %v", err)
	}
	if len(notifier.messages) != 0 {
		t.Errorf("messages sent on second drain = %d, want 0 (already done)", len(notifier.messages))
	}
}

// TestEarlyFinishNotice_NotEnqueuedWhenCompleteAfterDeadline covers R9:
// completion happening only after the deadline has already passed
// publishes immediately (U4) instead of ever posting a notice.
func TestEarlyFinishNotice_NotEnqueuedWhenCompleteAfterDeadline(t *testing.T) {
	ctx := context.Background()
	e, db, weekID := seedCompleteSongsWeek(t)

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
	if got.State != StateResultsCollection {
		t.Fatalf("week state = %q, want %q (deadline already past + complete must publish immediately)", got.State, StateResultsCollection)
	}

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ?", OutboxActionSongsEarlyFinish,
	).Scan(&count); err != nil {
		t.Fatalf("query early-finish rows: %v", err)
	}
	if count != 0 {
		t.Errorf("early-finish outbox rows = %d, want 0 (no notice when already past the deadline)", count)
	}
}

// TestEarlyFinishNotice_ResultsPhase_NamesResultsNotSongs covers the
// results_collection half of AE1: the songs-phase test above only
// exercises OutboxActionSongsEarlyFinish, so this proves the parallel
// OutboxActionResultsEarlyFinish path enqueues and drains with the
// correct "results" phase name rather than silently reusing "songs".
func TestEarlyFinishNotice_ResultsPhase_NamesResultsNotSongs(t *testing.T) {
	e, db, weekID, ids := setupResultsCollectionWeek(t, 2)
	ctx := context.Background()

	for _, p := range ids {
		answerAllQuizzes(t, ctx, db, weekID, p, false)
		rankAllRemaining(t, ctx, db, weekID, p)
	}
	// state_started_at for results_collection is left at "now" by
	// setupResultsCollectionWeek's ForceAdvance transition, so the
	// default deadline is still days away.

	if err := e.Tick(ctx); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}

	var pendingCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ? AND status = 'pending'", OutboxActionResultsEarlyFinish,
	).Scan(&pendingCount); err != nil {
		t.Fatalf("query pending early-finish rows: %v", err)
	}
	if pendingCount != 1 {
		t.Fatalf("pending %s rows = %d, want 1", OutboxActionResultsEarlyFinish, pendingCount)
	}

	got, err := e.CurrentWeek(ctx)
	if err != nil {
		t.Fatalf("CurrentWeek() error = %v", err)
	}
	if got.State != StateResultsCollection {
		t.Errorf("week state = %q, want %q (early finish must not publish)", got.State, StateResultsCollection)
	}

	notifier := &fakeNotifier{}
	if err := ProcessEarlyFinishNotices(ctx, db, notifier); err != nil {
		t.Fatalf("ProcessEarlyFinishNotices() error = %v", err)
	}
	if len(notifier.messages) != 1 {
		t.Fatalf("messages sent = %d, want 1", len(notifier.messages))
	}
	if !strings.Contains(notifier.messages[0], "results") {
		t.Errorf("message = %q, want it to name the results phase", notifier.messages[0])
	}
	if strings.Contains(notifier.messages[0], "songs") {
		t.Errorf("message = %q, must not name the songs phase for a results-phase notice", notifier.messages[0])
	}
}

func TestProcessEarlyFinishNotices_DBError(t *testing.T) {
	_, db := openTestEngine(t)
	db.Close()
	if err := ProcessEarlyFinishNotices(context.Background(), db, &fakeNotifier{}); err == nil {
		t.Error("ProcessEarlyFinishNotices() error = nil, want an error from the closed DB")
	}
}

func TestProcessEarlyFinishAction_DBError(t *testing.T) {
	_, db := openTestEngine(t)
	db.Close()
	if err := processEarlyFinishAction(
		context.Background(), db, &fakeNotifier{}, OutboxActionSongsEarlyFinish, 1,
		`{"week_id":1,"deadline":"2026-01-01T12:00:00+01:00"}`,
	); err == nil {
		t.Error("processEarlyFinishAction() error = nil, want an error from the closed DB")
	}
}
