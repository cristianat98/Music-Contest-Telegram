package contest

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
)

type fakeNotifier struct {
	mu       sync.Mutex
	messages []string
}

func (f *fakeNotifier) SendGroupMessage(ctx context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, text)
	return nil
}

func seedSubmission(t *testing.T, db *sql.DB, weekID, participantID int64, url string) {
	t.Helper()
	if _, err := db.Exec(
		"INSERT INTO submissions (week_id, participant_id, url) VALUES (?, ?, ?)",
		weekID, participantID, url,
	); err != nil {
		t.Fatalf("seed submission: %v", err)
	}
}

func TestSongsHooks_SongsCollectionComplete(t *testing.T) {
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
	if err := db.QueryRow("SELECT id FROM weeks ORDER BY id DESC LIMIT 1").Scan(&weekID); err != nil {
		t.Fatalf("query week id: %v", err)
	}

	hooks := NewSongsHooks(db)

	complete, err := hooks.SongsCollectionComplete(ctx, db, weekID)
	if err != nil {
		t.Fatalf("SongsCollectionComplete() error = %v", err)
	}
	if complete {
		t.Error("expected incomplete with no submissions yet")
	}

	var participantIDs []int64
	rows, _ := db.Query("SELECT id FROM participants")
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		participantIDs = append(participantIDs, id)
	}
	rows.Close()

	seedSubmission(t, db, weekID, participantIDs[0], "https://youtu.be/abc")

	complete, err = hooks.SongsCollectionComplete(ctx, db, weekID)
	if err != nil {
		t.Fatalf("SongsCollectionComplete() error = %v", err)
	}
	if complete {
		t.Error("expected incomplete with 1 of 2 submitted")
	}

	seedSubmission(t, db, weekID, participantIDs[1], "https://youtu.be/def")

	complete, err = hooks.SongsCollectionComplete(ctx, db, weekID)
	if err != nil {
		t.Fatalf("SongsCollectionComplete() error = %v", err)
	}
	if !complete {
		t.Error("expected complete once all required participants submitted")
	}
}

func TestSongsHooks_CloseForced_StrikesMissingAndEnqueuesPublish(t *testing.T) {
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
	e.SetSongsHooks(NewSongsHooks(db))

	var weekID int64
	db.QueryRow("SELECT id FROM weeks ORDER BY id DESC LIMIT 1").Scan(&weekID)
	var firstParticipant int64
	db.QueryRow("SELECT id FROM participants ORDER BY id LIMIT 1").Scan(&firstParticipant)
	seedSubmission(t, db, weekID, firstParticipant, "https://youtu.be/abc")

	// AE3: forced advance with a straggler publishes what exists and
	// strikes the missing participant.
	if _, err := e.ForceAdvance(ctx); err != nil {
		t.Fatalf("ForceAdvance() error = %v", err)
	}

	var strikes int
	if err := db.QueryRow("SELECT strikes FROM participants WHERE id != ?", firstParticipant).Scan(&strikes); err != nil {
		t.Fatalf("query strikes: %v", err)
	}
	if strikes != 1 {
		t.Errorf("strikes for straggler = %d, want 1", strikes)
	}

	var outboxCount int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM outbox_actions WHERE action_type = ? AND status = 'pending'", OutboxActionPublishSongs,
	).Scan(&outboxCount); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if outboxCount != 1 {
		t.Errorf("pending publish_songs outbox rows = %d, want 1", outboxCount)
	}
}

func TestPublishDueSongs_SendsAndMarksDone(t *testing.T) {
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

	var weekID int64
	db.QueryRow("SELECT id FROM weeks ORDER BY id DESC LIMIT 1").Scan(&weekID)
	rows, _ := db.Query("SELECT id FROM participants")
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	seedSubmission(t, db, weekID, ids[0], "https://youtu.be/abc")
	seedSubmission(t, db, weekID, ids[1], "https://youtu.be/def")

	if _, err := e.ForceAdvance(ctx); err != nil {
		t.Fatalf("ForceAdvance() error = %v", err)
	}

	notifier := &fakeNotifier{}
	if err := PublishDueSongs(ctx, db, notifier); err != nil {
		t.Fatalf("PublishDueSongs() error = %v", err)
	}

	if len(notifier.messages) != 1 {
		t.Fatalf("messages sent = %d, want 1", len(notifier.messages))
	}
	if !strings.Contains(notifier.messages[0], "https://youtu.be/abc") || !strings.Contains(notifier.messages[0], "https://youtu.be/def") {
		t.Errorf("published message missing expected URLs: %q", notifier.messages[0])
	}

	var status string
	if err := db.QueryRow(
		"SELECT status FROM outbox_actions WHERE action_type = ?", OutboxActionPublishSongs,
	).Scan(&status); err != nil {
		t.Fatalf("query outbox status: %v", err)
	}
	if status != "done" {
		t.Errorf("outbox status = %q, want %q", status, "done")
	}

	// Re-running PublishDueSongs after status=done must not resend.
	if err := PublishDueSongs(ctx, db, notifier); err != nil {
		t.Fatalf("second PublishDueSongs() error = %v", err)
	}
	if len(notifier.messages) != 1 {
		t.Errorf("messages sent after re-run = %d, want still 1 (done rows aren't reprocessed)", len(notifier.messages))
	}
}

func TestEnsureDisplayOrderAssigned_StableAcrossRetries(t *testing.T) {
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
	rows, _ := db.Query("SELECT id FROM participants")
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	seedSubmission(t, db, weekID, ids[0], "https://youtu.be/abc")
	seedSubmission(t, db, weekID, ids[1], "https://youtu.be/def")

	if err := ensureDisplayOrderAssigned(ctx, db, weekID); err != nil {
		t.Fatalf("first ensureDisplayOrderAssigned() error = %v", err)
	}
	first, err := orderedSubmissionURLs(ctx, db, weekID)
	if err != nil {
		t.Fatalf("orderedSubmissionURLs() error = %v", err)
	}

	if err := ensureDisplayOrderAssigned(ctx, db, weekID); err != nil {
		t.Fatalf("second ensureDisplayOrderAssigned() error = %v", err)
	}
	second, err := orderedSubmissionURLs(ctx, db, weekID)
	if err != nil {
		t.Fatalf("orderedSubmissionURLs() error = %v", err)
	}

	if len(first) != len(second) {
		t.Fatalf("order length changed between calls: %v vs %v", first, second)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("display_order changed on retry at index %d: %q vs %q", i, first[i], second[i])
		}
	}
}
