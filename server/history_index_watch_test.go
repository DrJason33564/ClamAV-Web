package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryIndexerWatchesOnlyChangedJobFiles(t *testing.T) {
	jobsDir := filepath.Join(t.TempDir(), "jobs")
	if err := os.MkdirAll(jobsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	historyDB, err := openHistoryDatabase(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = historyDB.Close() })

	oldDebounce := historyIndexEventDebounce
	oldRetry := historyWatchRetryInterval
	historyIndexEventDebounce = 20 * time.Millisecond
	historyWatchRetryInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		historyIndexEventDebounce = oldDebounce
		historyWatchRetryInterval = oldRetry
	})

	indexer := &historyIndexer{db: historyDB, jobsDir: jobsDir}
	s := &server{
		historyDB: historyDB,
		history:   indexer,
		appConfig: &appConfigStore{cfg: appConfig{HistoryIndexRefreshInterval: 3600}},
	}

	// This valid job exists before the watcher starts. A later event for another
	// path must not cause an opportunistic full-directory scan.
	unannouncedID := "cron-1783500000"
	unannouncedPath := filepath.Join(jobsDir, unannouncedID+".json")
	writeJobDocumentAtomically(t, unannouncedPath, indexedJobDocument{
		Version: 2, JobID: unannouncedID, Type: "cron", Status: "finished", Result: "clean",
		Action: "warn", StartedAt: 1783500000, User: "alice",
	})

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go s.runHistoryIndexer(ctx)

	// Give the watcher a chance to attach, then repeat the atomic publication
	// while polling. Repetition avoids depending on scheduler timing in the test.
	time.Sleep(50 * time.Millisecond)
	jobID := "cron-1783500001"
	jobPath := filepath.Join(jobsDir, jobID+".json")
	waitForHistoryState(t, historyDB, jobID, "running", func() {
		writeJobDocumentAtomically(t, jobPath, indexedJobDocument{
			Version: 2, JobID: jobID, Type: "cron", Status: "running", Result: "unknown",
			Action: "move", StartedAt: 1783500001, User: "alice",
		})
	})

	var unannouncedCount int
	if err := historyDB.QueryRow("SELECT COUNT(*) FROM history_jobs WHERE job_id=?", unannouncedID).Scan(&unannouncedCount); err != nil {
		t.Fatal(err)
	}
	if unannouncedCount != 0 {
		t.Fatalf("event refresh unexpectedly scanned unrelated job file; count=%d", unannouncedCount)
	}

	finishedAt := int64(1783500010)
	waitForHistoryState(t, historyDB, jobID, "finished", func() {
		writeJobDocumentAtomically(t, jobPath, indexedJobDocument{
			Version: 2, JobID: jobID, Type: "cron", Status: "finished", Result: "found",
			Action: "move", StartedAt: 1783500001, FinishedAt: &finishedAt, User: "alice",
		})
	})

	if err := os.Remove(jobPath); err != nil {
		t.Fatal(err)
	}
	waitForHistoryDeletion(t, historyDB, jobID)
}

func TestHistoryEventRefreshDoesNotTrustUnchangedMtime(t *testing.T) {
	jobsDir := t.TempDir()
	historyDB, err := openHistoryDatabase(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = historyDB.Close() })
	indexer := &historyIndexer{db: historyDB, jobsDir: jobsDir}

	jobID := "cron-1783500002"
	jobPath := filepath.Join(jobsDir, jobID+".json")
	fixedTime := time.Unix(1783500002, 0)
	writeJobDocumentAtomically(t, jobPath, indexedJobDocument{
		Version: 2, JobID: jobID, Type: "cron", Status: "running", Result: "unknown",
		Action: "warn", StartedAt: 1783500002, User: "alice",
	})
	if err := os.Chtimes(jobPath, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := indexer.refreshFile(t.Context(), jobPath); err != nil {
		t.Fatal(err)
	}

	finishedAt := int64(1783500012)
	writeJobDocumentAtomically(t, jobPath, indexedJobDocument{
		Version: 2, JobID: jobID, Type: "cron", Status: "finished", Result: "clean",
		Action: "warn", StartedAt: 1783500002, FinishedAt: &finishedAt, User: "alice",
	})
	if err := os.Chtimes(jobPath, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := indexer.refreshFile(t.Context(), jobPath); err != nil {
		t.Fatal(err)
	}

	var status string
	if err := historyDB.QueryRow("SELECT status FROM history_jobs WHERE job_id=?", jobID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "finished" {
		t.Fatalf("event refresh skipped an atomic replacement with unchanged mtime; status=%q", status)
	}
}

func TestHistoryWatchRetryDelayUsesCappedExponentialBackoff(t *testing.T) {
	want := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
		40 * time.Second,
		60 * time.Second,
		60 * time.Second,
	}
	for i, wantDelay := range want {
		if got := historyWatchRetryDelay(i + 1); got != wantDelay {
			t.Fatalf("attempt %d: expected retry delay %s, got %s", i+1, wantDelay, got)
		}
	}
}

func TestHistoryRefreshReconcilesBatchMtimes(t *testing.T) {
	jobsDir := t.TempDir()
	historyDB, err := openHistoryDatabase(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = historyDB.Close() })
	indexer := &historyIndexer{db: historyDB, jobsDir: jobsDir}

	firstID := "cron-1783500003"
	secondID := "cron-1783500004"
	firstPath := filepath.Join(jobsDir, firstID+".json")
	secondPath := filepath.Join(jobsDir, secondID+".json")
	writeJobDocumentAtomically(t, firstPath, indexedJobDocument{
		Version: 2, JobID: firstID, Type: "cron", Status: "running", Result: "unknown",
		Action: "warn", StartedAt: 1783500003, User: "alice",
	})
	writeJobDocumentAtomically(t, secondPath, indexedJobDocument{
		Version: 2, JobID: secondID, Type: "cron", Status: "finished", Result: "clean",
		Action: "warn", StartedAt: 1783500004, User: "alice",
	})
	if err := indexer.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}

	finishedAt := int64(1783500013)
	writeJobDocumentAtomically(t, firstPath, indexedJobDocument{
		Version: 2, JobID: firstID, Type: "cron", Status: "finished", Result: "found",
		Action: "warn", StartedAt: 1783500003, FinishedAt: &finishedAt, User: "alice",
	})
	changedTime := time.Unix(1783500014, 0)
	if err := os.Chtimes(firstPath, changedTime, changedTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(secondPath); err != nil {
		t.Fatal(err)
	}
	if err := indexer.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}

	var status string
	if err := historyDB.QueryRow("SELECT status FROM history_jobs WHERE job_id=?", firstID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "finished" {
		t.Fatalf("changed job was not updated by full reconciliation: status=%q", status)
	}
	var deletedCount int
	if err := historyDB.QueryRow("SELECT COUNT(*) FROM history_jobs WHERE job_id=?", secondID).Scan(&deletedCount); err != nil {
		t.Fatal(err)
	}
	if deletedCount != 0 {
		t.Fatalf("deleted job remained indexed after full reconciliation: count=%d", deletedCount)
	}
}

func TestHistoryWatcherStopsAfterRetryLimit(t *testing.T) {
	oldRetry := historyWatchRetryInterval
	oldMaxRetry := historyWatchMaxRetryInterval
	oldMaxCount := historyWatchMaxConsecutiveRetryCount
	historyWatchRetryInterval = time.Millisecond
	historyWatchMaxRetryInterval = 2 * time.Millisecond
	historyWatchMaxConsecutiveRetryCount = 3
	t.Cleanup(func() {
		historyWatchRetryInterval = oldRetry
		historyWatchMaxRetryInterval = oldMaxRetry
		historyWatchMaxConsecutiveRetryCount = oldMaxCount
	})

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan struct{})
	missingJobsDir := filepath.Join(t.TempDir(), "missing")
	go func() {
		watchHistoryJobFiles(ctx, missingJobsDir, make(chan historyIndexRequest, 1))
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("history watcher did not stop after reaching its retry limit")
	}
}

func writeJobDocumentAtomically(t *testing.T, path string, job indexedJobDocument) {
	t.Helper()
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".job-state-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		t.Fatal(err)
	}
}

func waitForHistoryState(t *testing.T, db *sql.DB, jobID string, wantStatus string, publish func()) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		publish()
		var status string
		err := db.QueryRow("SELECT status FROM history_jobs WHERE job_id=?", jobID).Scan(&status)
		if err == nil && status == wantStatus {
			return
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("query history state: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("history job %s did not reach status %q; last status=%q err=%v", jobID, wantStatus, status, err)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func waitForHistoryDeletion(t *testing.T, db *sql.DB, jobID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM history_jobs WHERE job_id=?", jobID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("history job %s was not removed after its file was deleted", jobID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
