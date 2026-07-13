package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpen_AppliesMigrations(t *testing.T) {
	db := openTestDB(t)

	tables := []string{
		"contests", "topics", "topic_usage", "participants", "contest_participants", "weeks",
		"submissions", "votes", "quiz_answers", "outbox_actions",
	}
	for _, table := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found after migration: %v", table, err)
		}
	}
}

func TestOpen_SeedsDefaultNormalTopic(t *testing.T) {
	db := openTestDB(t)

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM topics WHERE text = 'normal'").Scan(&count); err != nil {
		t.Fatalf("query seeded topic: %v", err)
	}
	if count != 1 {
		t.Errorf("seeded 'normal' topic count = %d, want 1", count)
	}
}

func hasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notNull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}

func TestOpen_ParticipantsColumns_NoStrikes(t *testing.T) {
	db := openTestDB(t)
	if hasColumn(t, db, "participants", "strikes") {
		t.Error("participants.strikes should not exist after migration")
	}
}

func TestOpen_TopicUsageColumns_SelectableNotUsed(t *testing.T) {
	db := openTestDB(t)
	if hasColumn(t, db, "topic_usage", "used") {
		t.Error("topic_usage.used should not exist after migration")
	}
	if !hasColumn(t, db, "topic_usage", "selectable") {
		t.Error("topic_usage.selectable should exist after migration")
	}
}

func TestOpen_SubmissionsColumns_DisplayNameNotDisplayOrder(t *testing.T) {
	db := openTestDB(t)
	if hasColumn(t, db, "submissions", "display_order") {
		t.Error("submissions.display_order should not exist after migration")
	}
	if !hasColumn(t, db, "submissions", "display_name") {
		t.Error("submissions.display_name should exist after migration")
	}
}

func TestOpen_ContestParticipantsColumns(t *testing.T) {
	db := openTestDB(t)
	for _, col := range []string{"contest_id", "participant_id", "left_at"} {
		if !hasColumn(t, db, "contest_participants", col) {
			t.Errorf("contest_participants.%s should exist after migration", col)
		}
	}
	// No separate active flag: obligated is exactly left_at IS NULL, so
	// there's nothing to keep in sync with it.
	if hasColumn(t, db, "contest_participants", "active") {
		t.Error("contest_participants.active should not exist -- obligation is derived from left_at")
	}
}

func TestOpen_ReRunIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	db1.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer db2.Close()

	var count int
	if err := db2.QueryRow("SELECT COUNT(*) FROM contests").Scan(&count); err != nil {
		t.Fatalf("query after re-open failed: %v", err)
	}
}

func TestOutboxActions_RoundTrip(t *testing.T) {
	db := openTestDB(t)

	res, err := db.Exec(
		"INSERT INTO outbox_actions (action_type, payload_json, status) VALUES (?, ?, ?)",
		"publish_songs", `{"week_id":1}`, "pending",
	)
	if err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	if _, err := db.Exec("UPDATE outbox_actions SET status = 'in_progress' WHERE id = ?", id); err != nil {
		t.Fatalf("update to in_progress: %v", err)
	}

	if _, err := db.Exec(
		"UPDATE outbox_actions SET status = 'done', completed_at = datetime('now') WHERE id = ?", id,
	); err != nil {
		t.Fatalf("update to done: %v", err)
	}

	var status string
	if err := db.QueryRow("SELECT status FROM outbox_actions WHERE id = ?", id).Scan(&status); err != nil {
		t.Fatalf("select status: %v", err)
	}
	if status != "done" {
		t.Errorf("status = %q, want %q", status, "done")
	}
}

func TestCheckpoint_Succeeds(t *testing.T) {
	db := openTestDB(t)

	if err := Checkpoint(db); err != nil {
		t.Fatalf("Checkpoint() error = %v", err)
	}
}

func TestCheckpoint_DBError(t *testing.T) {
	db := openTestDB(t)
	db.Close()

	if err := Checkpoint(db); err == nil {
		t.Error("Checkpoint() error = nil, want an error from the closed DB")
	}
}

func TestOpen_MigrationFailure_ClosesDBAndReturnsError(t *testing.T) {
	// A directory can't be opened as a SQLite file, so the first real I/O
	// migrate() performs fails -- exercising Open's db.Close()-then-return
	// error path.
	dir := filepath.Join(t.TempDir(), "subdir")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := Open(dir); err == nil {
		t.Error("Open() error = nil, want an error for a directory path")
	}
}
