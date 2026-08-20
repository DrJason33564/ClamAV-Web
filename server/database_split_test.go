package main

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestUserAndHistorySchemasAreSeparated(t *testing.T) {
	tmp := t.TempDir()
	userDB, err := openUserDatabase(filepath.Join(tmp, "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer userDB.Close()
	historyDB, err := openHistoryDatabase(filepath.Join(tmp, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer historyDB.Close()

	for _, table := range []string{"users", "sessions"} {
		if !sqliteTableExists(t, userDB, table) || sqliteTableExists(t, historyDB, table) {
			t.Fatalf("expected %s to exist only in the user database", table)
		}
	}
	if !sqliteTableExists(t, historyDB, "history_jobs") || sqliteTableExists(t, userDB, "history_jobs") {
		t.Fatal("expected history_jobs to exist only in the history database")
	}
	for name, db := range map[string]*sql.DB{"user": userDB, "history": historyDB} {
		var version int
		if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil || version != 2 {
			t.Fatalf("unexpected %s database schema version: %d err=%v", name, version, err)
		}
	}
}

func TestLoadConfigUsesSeparateDatabaseFiles(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("USER_DATABASE_FILE", "")
	t.Setenv("HISTORY_DATABASE_FILE", "")
	t.Setenv("IS_TIMEDOCK", "N")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UserDatabaseFile != filepath.Join(dataDir, "users.db") {
		t.Fatalf("unexpected user database path: %s", cfg.UserDatabaseFile)
	}
	if cfg.HistoryDatabaseFile != filepath.Join(dataDir, "history.db") {
		t.Fatalf("unexpected history database path: %s", cfg.HistoryDatabaseFile)
	}
}

func TestCleanDeleteUserAcrossSeparatedDatabases(t *testing.T) {
	s := newDatabaseTestServer(t)
	now := time.Now().Unix()
	_, err := s.userDB.Exec(`INSERT INTO users(id,username,password_hash,role,created_at,updated_at) VALUES(?,'bob',NULL,'user',?,?)`, testBobUserID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.historyDB.Exec(`INSERT INTO history_jobs(job_id,job_type,status,result,action,started_at,finished_at,user_id,json_file,file_mtime_ns,indexed_at) VALUES('manual-1783433000','manual','finished','clean','warn',1783433000,1783433010,?,'/state/jobs/manual-1783433000.json',1,?)`, testBobUserID, now); err != nil {
		t.Fatal(err)
	}
	// Keep this test focused on cross-database cleanup. The history indexer's
	// file reconciliation is covered separately and would remove the fixture.
	s.history = nil
	if err := s.cleanDeleteUser(t.Context(), "bob"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.userDB.QueryRow("SELECT COUNT(*) FROM users WHERE username='bob'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("user was not removed: count=%d err=%v", count, err)
	}
	if err := s.historyDB.QueryRow("SELECT COUNT(*) FROM history_jobs WHERE user_id=?", testBobUserID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("history index was not removed: count=%d err=%v", count, err)
	}
}

func sqliteTableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var found string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return found == name
}
