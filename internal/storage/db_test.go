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
		"contests", "topics", "topic_usage", "participants", "weeks", "week_participants",
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
