package contest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cristianat98/Music-Contest-Telegram/internal/storage"
)

func openTestEngine(t *testing.T) (*Engine, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("storage.Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewEngine(db), db
}

func seedParticipants(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := db.Exec(
			"INSERT INTO participants (telegram_user_id, display_name, active) VALUES (?, ?, 1)",
			1000+i, fmt.Sprintf("user%d", i),
		); err != nil {
			t.Fatalf("seed participant: %v", err)
		}
	}
}

func seedTopic(t *testing.T, db *sql.DB, text string) {
	t.Helper()
	if _, err := db.Exec("INSERT INTO topics (text) VALUES (?)", text); err != nil {
		t.Fatalf("seed topic: %v", err)
	}
}

func TestStartContest_DeactivatesPreviousAndResetsStrikesAndTopics(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)

	seedParticipants(t, db, 2)
	seedTopic(t, db, "topic-a")

	if _, err := e.StartContest(ctx, "Contest One"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := db.Exec("UPDATE participants SET strikes = 3"); err != nil {
		t.Fatalf("seed strikes: %v", err)
	}
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	// AE6: starting a new contest deactivates the old one and resets strikes
	// and the topic pool.
	if _, err := e.StartContest(ctx, "Contest Two"); err != nil {
		t.Fatalf("second StartContest() error = %v", err)
	}

	var activeCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM contests WHERE active = 1").Scan(&activeCount); err != nil {
		t.Fatalf("count active contests: %v", err)
	}
	if activeCount != 1 {
		t.Errorf("active contest count = %d, want 1", activeCount)
	}

	var strikes int
	if err := db.QueryRow("SELECT strikes FROM participants LIMIT 1").Scan(&strikes); err != nil {
		t.Fatalf("query strikes: %v", err)
	}
	if strikes != 0 {
		t.Errorf("strikes = %d, want 0 after new contest start", strikes)
	}

	// The topic used in Contest One's week must be available again in
	// Contest Two's pool.
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() on new contest error = %v", err)
	}
}

func TestStartWeek_RejectsBelowMinimumParticipants(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 1)
	seedTopic(t, db, "topic-a")

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}

	_, err := e.StartWeek(ctx)
	if !errors.Is(err, ErrNotEnoughEligible) {
		t.Fatalf("StartWeek() error = %v, want %v", err, ErrNotEnoughEligible)
	}
}

func TestStartWeek_RejectsWithNoActiveContest(t *testing.T) {
	ctx := context.Background()
	e, _ := openTestEngine(t)

	_, err := e.StartWeek(ctx)
	if !errors.Is(err, ErrNoActiveContest) {
		t.Fatalf("StartWeek() error = %v, want %v", err, ErrNoActiveContest)
	}
}

// closedDBEngine returns an Engine whose underlying DB connection is
// already closed, so every db/tx operation a method attempts fails
// immediately -- a cheap way to exercise DB-error branches without
// per-call fault injection.
func closedDBEngine(t *testing.T) *Engine {
	t.Helper()
	e, db := openTestEngine(t)
	db.Close()
	return e
}

func TestStartContest_DBError(t *testing.T) {
	if _, err := closedDBEngine(t).StartContest(context.Background(), "Contest"); err == nil {
		t.Error("StartContest() error = nil, want an error from the closed DB")
	}
}

func TestStartWeek_DBError(t *testing.T) {
	if _, err := closedDBEngine(t).StartWeek(context.Background()); err == nil {
		t.Error("StartWeek() error = nil, want an error from the closed DB")
	}
}

func TestFinishContest_DBError(t *testing.T) {
	if _, err := closedDBEngine(t).FinishContest(context.Background()); err == nil {
		t.Error("FinishContest() error = nil, want an error from the closed DB")
	}
}

func TestModifyLimit_DBError(t *testing.T) {
	if _, err := closedDBEngine(t).ModifyLimit(context.Background(), 3); err == nil {
		t.Error("ModifyLimit() error = nil, want an error from the closed DB")
	}
}

func TestForceAdvance_DBError(t *testing.T) {
	if _, err := closedDBEngine(t).ForceAdvance(context.Background()); err == nil {
		t.Error("ForceAdvance() error = nil, want an error from the closed DB")
	}
}

func TestCurrentWeek_DBError(t *testing.T) {
	if _, err := closedDBEngine(t).CurrentWeek(context.Background()); err == nil {
		t.Error("CurrentWeek() error = nil, want an error from the closed DB")
	}
}

func TestFinishContest_RejectedWhileWeekNotIdle(t *testing.T) {
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

	_, err := e.FinishContest(ctx)
	if !errors.Is(err, ErrWeekNotIdle) {
		t.Fatalf("FinishContest() error = %v, want %v", err, ErrWeekNotIdle)
	}

	// Force the week back to idle, then finishing should succeed.
	if _, err := e.ForceAdvance(ctx); err != nil { // songs_collection -> results_collection
		t.Fatalf("ForceAdvance() #1 error = %v", err)
	}
	if _, err := e.ForceAdvance(ctx); err != nil { // results_collection -> idle
		t.Fatalf("ForceAdvance() #2 error = %v", err)
	}

	if _, err := e.FinishContest(ctx); err != nil {
		t.Fatalf("FinishContest() after idle error = %v", err)
	}
}

func TestForceAdvance_RejectsWithNoActiveState(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}

	_, err := e.ForceAdvance(ctx)
	if !errors.Is(err, ErrNoActiveState) {
		t.Fatalf("ForceAdvance() error = %v, want %v", err, ErrNoActiveState)
	}
	_ = db
}

func TestNoopResultsHooks_ResultsCollectionComplete_AlwaysFalse(t *testing.T) {
	complete, err := noopResultsHooks{}.ResultsCollectionComplete(context.Background(), nil, 1)
	if err != nil {
		t.Fatalf("ResultsCollectionComplete() error = %v", err)
	}
	if complete {
		t.Error("expected the default no-op hooks to never report complete")
	}
}

func TestModifyLimit_Success(t *testing.T) {
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

	msg, err := e.ModifyLimit(ctx, 5)
	if err != nil {
		t.Fatalf("ModifyLimit() error = %v", err)
	}
	if !strings.Contains(msg, "Deadline") {
		t.Errorf("ModifyLimit() message = %q, want it to mention the updated deadline", msg)
	}

	var days int
	if err := db.QueryRow("SELECT deadline_override_days FROM weeks ORDER BY id DESC LIMIT 1").Scan(&days); err != nil {
		t.Fatalf("query deadline_override_days: %v", err)
	}
	if days != 5 {
		t.Errorf("deadline_override_days = %d, want 5", days)
	}
}

func TestModifyLimit_RejectsWithNoActiveState(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}

	_, err := e.ModifyLimit(ctx, 5)
	if !errors.Is(err, ErrNoActiveState) {
		t.Fatalf("ModifyLimit() error = %v, want %v", err, ErrNoActiveState)
	}
}

// countingHooks counts how many times each close hook actually runs, so a
// test can prove a single state ("songs_collection" for this week) was
// closed exactly once even when multiple ForceAdvance calls race for it.
type countingHooks struct {
	mu            sync.Mutex
	songsCloses   int
	resultsCloses int
}

func (h *countingHooks) CloseSongsCollection(ctx context.Context, tx *sql.Tx, weekID int64, forced bool) error {
	h.mu.Lock()
	h.songsCloses++
	h.mu.Unlock()
	return nil
}

func (h *countingHooks) OpenResultsCollection(ctx context.Context, tx *sql.Tx, weekID int64) error {
	return nil
}

func (h *countingHooks) CloseResultsCollection(ctx context.Context, tx *sql.Tx, weekID int64, forced bool) error {
	h.mu.Lock()
	h.resultsCloses++
	h.mu.Unlock()
	return nil
}

func (h *countingHooks) SongsCollectionComplete(ctx context.Context, db *sql.DB, weekID int64) (bool, error) {
	return false, nil
}

func (h *countingHooks) ResultsCollectionComplete(ctx context.Context, db *sql.DB, weekID int64) (bool, error) {
	return false, nil
}

func TestForceAdvance_ConcurrentCallsCloseEachStateExactlyOnce(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)
	seedTopic(t, db, "topic-a")

	hooks := &countingHooks{}
	e.SetSongsHooks(hooks)
	e.SetResultsHooks(hooks)

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v", err)
	}

	// Two concurrent /forceadvance calls against the same songs_collection
	// week: the lock (KTD5, resolving doc-review finding A1) must ensure
	// CloseSongsCollection runs exactly once, even though both calls will
	// eventually succeed sequentially (the second legitimately advances the
	// resulting results_collection state to idle).
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, results[idx] = e.ForceAdvance(ctx)
		}(i)
	}
	wg.Wait()

	for i, err := range results {
		if err != nil {
			t.Errorf("ForceAdvance() call %d error = %v, want nil", i, err)
		}
	}

	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	if hooks.songsCloses != 1 {
		t.Errorf("CloseSongsCollection call count = %d, want exactly 1", hooks.songsCloses)
	}
	if hooks.resultsCloses != 1 {
		t.Errorf("CloseResultsCollection call count = %d, want exactly 1", hooks.resultsCloses)
	}

	var state string
	if err := db.QueryRow("SELECT state FROM weeks ORDER BY id DESC LIMIT 1").Scan(&state); err != nil {
		t.Fatalf("query week state: %v", err)
	}
	if state != StateIdle {
		t.Errorf("week state = %q, want %q after both transitions applied sequentially", state, StateIdle)
	}
}
