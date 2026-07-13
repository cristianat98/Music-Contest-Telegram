package contest

import (
	"context"
	"errors"
	"testing"
)

func TestStartContest_AssociatesDefaultTopicEvenWithNoneSeeded(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}

	var contestID int64
	db.QueryRow("SELECT id FROM contests WHERE active = 1").Scan(&contestID)

	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM topic_usage tu
		JOIN topics t ON t.id = tu.topic_id
		WHERE tu.contest_id = ? AND t.text = 'normal'
	`, contestID).Scan(&count); err != nil {
		t.Fatalf("query default topic association: %v", err)
	}
	if count != 1 {
		t.Errorf("default topic association count = %d, want 1", count)
	}

	// StartWeek must succeed even with no manually-seeded topics.
	if _, err := e.StartWeek(ctx); err != nil {
		t.Fatalf("StartWeek() error = %v, want it to pick the default topic", err)
	}
}

func TestPickTopic_FallsBackToRepeatOnceExhausted(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)

	if _, err := e.StartContest(ctx, "Contest"); err != nil {
		t.Fatalf("StartContest() error = %v", err)
	}
	var contestID int64
	db.QueryRow("SELECT id FROM contests WHERE active = 1").Scan(&contestID)

	// Only the seeded "normal" topic is associated with this contest.
	// Exhaust it, then confirm a repeat pick still succeeds.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	firstID, _, err := pickTopic(ctx, tx, contestID)
	if err != nil {
		t.Fatalf("first pickTopic() error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var selectable int
	db.QueryRow("SELECT selectable FROM topic_usage WHERE contest_id = ? AND topic_id = ?", contestID, firstID).Scan(&selectable)
	if selectable != 0 {
		t.Errorf("selectable = %d after picking, want 0", selectable)
	}

	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx2.Rollback()
	secondID, _, err := pickTopic(ctx, tx2, contestID)
	if err != nil {
		t.Fatalf("second pickTopic() error = %v, want a repeat fallback instead of ErrNoTopicsAvailable", err)
	}
	if secondID != firstID {
		t.Errorf("second pick = %d, want a repeat of %d (only topic associated)", secondID, firstID)
	}
}

func TestPickTopic_ErrorsWhenContestHasNoAssociatedTopics(t *testing.T) {
	ctx := context.Background()
	_, db := openTestEngine(t)

	// A contest row with zero topic_usage rows at all -- the defensive path.
	res, err := db.Exec("INSERT INTO contests (name, active) VALUES (?, 1)", "Empty Contest")
	if err != nil {
		t.Fatalf("insert contest: %v", err)
	}
	contestID, _ := res.LastInsertId()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	if _, _, err := pickTopic(ctx, tx, contestID); !errors.Is(err, ErrNoTopicsAvailable) {
		t.Errorf("pickTopic() error = %v, want ErrNoTopicsAvailable", err)
	}
}

func TestPickTopic_SameTopicIndependentAcrossContests(t *testing.T) {
	ctx := context.Background()
	e, db := openTestEngine(t)
	seedParticipants(t, db, 2)

	if _, err := e.StartContest(ctx, "Contest A"); err != nil {
		t.Fatalf("StartContest(A) error = %v", err)
	}
	var contestA int64
	db.QueryRow("SELECT id FROM contests WHERE active = 1").Scan(&contestA)

	if _, err := e.StartContest(ctx, "Contest B"); err != nil {
		t.Fatalf("StartContest(B) error = %v", err)
	}
	var contestB int64
	db.QueryRow("SELECT id FROM contests WHERE active = 1").Scan(&contestB)

	// Pick (and exhaust) the default topic in contest B only.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, _, err := pickTopic(ctx, tx, contestB); err != nil {
		t.Fatalf("pickTopic(B) error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var selectableInA int
	if err := db.QueryRow(`
		SELECT tu.selectable FROM topic_usage tu
		JOIN topics t ON t.id = tu.topic_id
		WHERE tu.contest_id = ? AND t.text = 'normal'
	`, contestA).Scan(&selectableInA); err != nil {
		t.Fatalf("query contest A's default topic association: %v", err)
	}
	if selectableInA != 1 {
		t.Errorf("selectable in contest A = %d, want 1 (picking in contest B must not affect contest A)", selectableInA)
	}
}
