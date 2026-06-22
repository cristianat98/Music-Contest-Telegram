package storage

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" driver with database/sql
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens the SQLite database at path, tuned for the Pi Zero W's limited
// memory and flash, and applies any pending migrations.
//
// A single open connection is enforced (SetMaxOpenConns(1)) because
// modernc.org/sqlite is known to surface SQLITE_BUSY under concurrent
// connections even with WAL enabled; every caller shares this one *sql.DB.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=cache_size(-2000)",
		path,
	)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("storage: set goose dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("storage: apply migrations: %w", err)
	}
	return nil
}

// Checkpoint forces a WAL checkpoint so the -wal file doesn't grow unbounded
// between deploys. Call this during graceful shutdown.
func Checkpoint(db *sql.DB) error {
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE);"); err != nil {
		return fmt.Errorf("storage: checkpoint: %w", err)
	}
	return nil
}
