package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type databaseSchema func(context.Context, *sql.DB) error

const historyDetectionsSchema = `
CREATE TABLE IF NOT EXISTS history_detections (
    job_id TEXT NOT NULL REFERENCES history_jobs(job_id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL CHECK(sequence >= 0),
    source_file TEXT NOT NULL,
    detection_reason TEXT NOT NULL,
    PRIMARY KEY(job_id, sequence)
);`

func openSQLiteDatabase(path string, initialize databaseSchema) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// _pragma values in the DSN are applied to every pooled connection; in
	// particular, foreign_keys is otherwise connection-local in SQLite.
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Each SQLite file has a single writer. A small connection pool plus WAL
	// keeps concurrent API reads responsive during short write transactions.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("initialize sqlite: %w", err)
		}
	}
	if err := initialize(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func openUserDatabase(path string) (*sql.DB, error) {
	return openSQLiteDatabase(path, initializeUserDatabase)
}

func openHistoryDatabase(path string) (*sql.DB, error) {
	return openSQLiteDatabase(path, initializeHistoryDatabase)
}

func initializeUserDatabase(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY CHECK(
        length(id)=8 AND
        id NOT GLOB '*[^a-z0-9]*' AND
        id GLOB '*[a-z]*' AND
        id GLOB '*[0-9]*'
    ),
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT,
    role TEXT NOT NULL CHECK(role IN ('user','admin')),
    timedock_account TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','disabled','deleting')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash BLOB NOT NULL UNIQUE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    idle_expires_at INTEGER NOT NULL,
    absolute_expires_at INTEGER NOT NULL,
    revoked_at INTEGER
);
CREATE INDEX IF NOT EXISTS sessions_token_active ON sessions(token_hash, revoked_at, absolute_expires_at);`
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize user database: %w", err)
	}
	if err := requireSQLiteColumnType(ctx, db, "users", "id", "TEXT"); err != nil {
		return fmt.Errorf("incompatible user database; recreate users.db: %w", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(2, unixepoch())"); err != nil {
		return fmt.Errorf("record user database schema: %w", err)
	}
	return nil
}

func initializeHistoryDatabase(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS history_jobs (
    job_id TEXT PRIMARY KEY,
    job_type TEXT NOT NULL CHECK(job_type IN ('manual','cron')),
    status TEXT NOT NULL,
    result TEXT NOT NULL,
    action TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    user_id TEXT NOT NULL,
    json_file TEXT NOT NULL UNIQUE,
    file_mtime_ns INTEGER NOT NULL,
    indexed_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS history_jobs_user_started ON history_jobs(user_id, started_at DESC, job_id DESC);`
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize history database: %w", err)
	}
	if err := requireSQLiteColumnType(ctx, db, "history_jobs", "user_id", "TEXT"); err != nil {
		return fmt.Errorf("incompatible history database; recreate history.db: %w", err)
	}
	version, err := historyDatabaseSchemaVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("read history database schema: %w", err)
	}
	if version == 0 {
		// A new database has no version 2 rows to backfill, so it can start at
		// schema 3 immediately. Existing version 2 databases are upgraded by the
		// history indexer after its non-destructive full refresh.
		if _, err := db.ExecContext(ctx, historyDetectionsSchema); err != nil {
			return fmt.Errorf("initialize history detections: %w", err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(3, unixepoch())"); err != nil {
			return fmt.Errorf("record history database schema: %w", err)
		}
		return nil
	}
	if version >= 3 {
		if _, err := db.ExecContext(ctx, historyDetectionsSchema); err != nil {
			return fmt.Errorf("initialize history detections: %w", err)
		}
	}
	return nil
}

func historyDatabaseSchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		return 0, err
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func requireSQLiteColumnType(ctx context.Context, db *sql.DB, table, column, wantType string) error {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			if !strings.EqualFold(columnType, wantType) {
				return fmt.Errorf("column %s.%s has type %s, want %s", table, column, columnType, wantType)
			}
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return fmt.Errorf("column %s.%s is missing", table, column)
}
