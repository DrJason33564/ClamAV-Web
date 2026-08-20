package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

var errCleanupRowsIteration = errors.New("injected history row iteration error")
var cleanupRowsDriverNumber atomic.Uint64

type cleanupRowsErrorDriver struct {
	jsonFile  string
	execCalls atomic.Int32
}

func (d *cleanupRowsErrorDriver) Open(string) (driver.Conn, error) {
	return &cleanupRowsErrorConn{owner: d}, nil
}

type cleanupRowsErrorConn struct {
	owner *cleanupRowsErrorDriver
}

func (c *cleanupRowsErrorConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (c *cleanupRowsErrorConn) Close() error { return nil }

func (c *cleanupRowsErrorConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c *cleanupRowsErrorConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &cleanupRowsErrorRows{jsonFile: c.owner.jsonFile}, nil
}

func (c *cleanupRowsErrorConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.owner.execCalls.Add(1)
	return driver.RowsAffected(1), nil
}

type cleanupRowsErrorRows struct {
	jsonFile string
	step     int
}

func (r *cleanupRowsErrorRows) Columns() []string { return []string{"job_id", "json_file"} }
func (r *cleanupRowsErrorRows) Close() error      { return nil }

func (r *cleanupRowsErrorRows) Next(values []driver.Value) error {
	switch r.step {
	case 0:
		r.step++
		values[0] = "manual-1783433000"
		values[1] = r.jsonFile
		return nil
	case 1:
		r.step++
		return errCleanupRowsIteration
	default:
		return io.EOF
	}
}

func TestCleanUserResultsStopsBeforeFileDeletionOnRowsError(t *testing.T) {
	tmp := t.TempDir()
	jsonFile := filepath.Join(tmp, "manual-1783433000.json")
	if err := os.WriteFile(jsonFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	dbDriver := &cleanupRowsErrorDriver{jsonFile: jsonFile}
	driverName := fmt.Sprintf("clamav_cleanup_rows_error_test_%d", cleanupRowsDriverNumber.Add(1))
	sql.Register(driverName, dbDriver)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	s := &server{historyDB: db, cfg: config{LogDir: tmp}}
	deleted, err := s.cleanUserResults(t.Context(), testAliceUserID)
	if !errors.Is(err, errCleanupRowsIteration) {
		t.Fatalf("cleanUserResults error = %v, want injected iteration error", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0", deleted)
	}
	if _, err := os.Stat(jsonFile); err != nil {
		t.Fatalf("history file was removed after partial row iteration: %v", err)
	}
	if calls := dbDriver.execCalls.Load(); calls != 0 {
		t.Fatalf("history delete executed after partial row iteration: %d calls", calls)
	}
}
