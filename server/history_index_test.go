package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExplicitRefreshDoesNotTrustUnchangedMtime(t *testing.T) {
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
		Version: 3, JobID: jobID, Type: "cron", Status: "running", Result: "unknown",
		Action: "warn", StartedAt: 1783500002, UserID: testAliceUserID,
	})
	if err := os.Chtimes(jobPath, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := indexer.refreshFile(t.Context(), jobPath); err != nil {
		t.Fatal(err)
	}

	finishedAt := int64(1783500012)
	writeJobDocumentAtomically(t, jobPath, indexedJobDocument{
		Version: 3, JobID: jobID, Type: "cron", Status: "finished", Result: "clean",
		Action: "warn", StartedAt: 1783500002, FinishedAt: &finishedAt, UserID: testAliceUserID,
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
		Version: 3, JobID: firstID, Type: "cron", Status: "running", Result: "unknown",
		Action: "warn", StartedAt: 1783500003, UserID: testAliceUserID,
	})
	writeJobDocumentAtomically(t, secondPath, indexedJobDocument{
		Version: 3, JobID: secondID, Type: "cron", Status: "finished", Result: "clean",
		Action: "warn", StartedAt: 1783500004, UserID: testAliceUserID,
	})
	if err := indexer.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}

	finishedAt := int64(1783500013)
	writeJobDocumentAtomically(t, firstPath, indexedJobDocument{
		Version: 3, JobID: firstID, Type: "cron", Status: "finished", Result: "found",
		Action: "warn", StartedAt: 1783500003, FinishedAt: &finishedAt, UserID: testAliceUserID,
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

func TestHistoryIndexerStoresAndReplacesDetectionDetails(t *testing.T) {
	root := t.TempDir()
	jobsDir := filepath.Join(root, "jobs")
	logDir := filepath.Join(root, "logs")
	for _, dir := range []string{jobsDir, logDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	historyDB, err := openHistoryDatabase(filepath.Join(root, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = historyDB.Close() })
	indexer := &historyIndexer{db: historyDB, jobsDir: jobsDir, logDir: logDir}

	jobID := "manual-1783500100"
	jobPath := filepath.Join(jobsDir, jobID+".json")
	detectionPath := filepath.Join(logDir, "clamav_detection_"+jobID+".log")
	detectionLog := strings.Join([]string{
		"2026-08-24 10:00:00 [DETECTION] Source file      : /scan/eicar.txt",
		"2026-08-24 10:00:00 [DETECTION] Detection reason : Eicar-Test-Signature",
		"2026-08-24 10:00:01 [DETECTION] Source file      : /scan/second.txt",
		"2026-08-24 10:00:01 [DETECTION] Detection reason : Test-Signature-2",
	}, "\n")
	if err := os.WriteFile(detectionPath, []byte(detectionLog), 0o600); err != nil {
		t.Fatal(err)
	}
	finishedAt := int64(1783500110)
	writeJobDocumentAtomically(t, jobPath, indexedJobDocument{
		Version: 3, JobID: jobID, Type: "manual", Status: "finished", Result: "found",
		Action: "warn", StartedAt: 1783500100, FinishedAt: &finishedAt, UserID: testAliceUserID,
		DetectionLog: &detectionPath,
	})
	if err := indexer.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}

	rows, err := historyDB.Query(`SELECT sequence,source_file,detection_reason FROM history_detections WHERE job_id=? ORDER BY sequence`, jobID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type indexedDetection struct {
		sequence int
		file     string
		reason   string
	}
	var got []indexedDetection
	for rows.Next() {
		var detection indexedDetection
		if err := rows.Scan(&detection.sequence, &detection.file, &detection.reason); err != nil {
			t.Fatal(err)
		}
		got = append(got, detection)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (indexedDetection{0, "/scan/eicar.txt", "Eicar-Test-Signature"}) || got[1] != (indexedDetection{1, "/scan/second.txt", "Test-Signature-2"}) {
		t.Fatalf("unexpected indexed detections: %#v", got)
	}

	writeJobDocumentAtomically(t, jobPath, indexedJobDocument{
		Version: 3, JobID: jobID, Type: "manual", Status: "finished", Result: "clean",
		Action: "warn", StartedAt: 1783500100, FinishedAt: &finishedAt, UserID: testAliceUserID,
	})
	if err := indexer.refreshFile(t.Context(), jobPath); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := historyDB.QueryRow("SELECT COUNT(*) FROM history_detections WHERE job_id=?", jobID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected reindex to clear stale detections, got %d", count)
	}
}

func TestHistorySchema2UpgradeBackfillsWithoutDeletingJobs(t *testing.T) {
	root := t.TempDir()
	jobsDir := filepath.Join(root, "jobs")
	logDir := filepath.Join(root, "logs")
	for _, dir := range []string{jobsDir, logDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	historyDB, err := openHistoryDatabase(filepath.Join(root, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = historyDB.Close() })
	if _, err := historyDB.Exec(`DELETE FROM schema_migrations; INSERT INTO schema_migrations(version,applied_at) VALUES(2,unixepoch()); DROP TABLE history_detections`); err != nil {
		t.Fatal(err)
	}

	jobID := "cron-1783500101"
	jobPath := filepath.Join(jobsDir, jobID+".json")
	detectionPath := filepath.Join(logDir, "clamav_detection_"+jobID+".log")
	if err := os.WriteFile(detectionPath, []byte("[DETECTION] Source file : /scan/backfill.txt\n[DETECTION] Detection reason : Backfill-Signature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJobDocumentAtomically(t, jobPath, indexedJobDocument{
		Version: 3, JobID: jobID, Type: "cron", Status: "finished", Result: "found",
		Action: "move", StartedAt: 1783500101, UserID: testAliceUserID, DetectionLog: &detectionPath,
	})
	info, err := os.Stat(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	insertJob := func(id, jsonFile string, mtime int64) {
		t.Helper()
		if _, err := historyDB.Exec(`INSERT INTO history_jobs(job_id,job_type,status,result,action,started_at,finished_at,user_id,json_file,file_mtime_ns,indexed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			id, "cron", "finished", "found", "move", 1783500101, 1783500111, testAliceUserID, jsonFile, mtime, now); err != nil {
			t.Fatal(err)
		}
	}
	insertJob(jobID, jobPath, info.ModTime().UnixNano())
	staleID := "cron-1783500102"
	insertJob(staleID, filepath.Join(jobsDir, staleID+".json"), 1)

	indexer := &historyIndexer{db: historyDB, jobsDir: jobsDir, logDir: logDir}
	upgraded, err := indexer.upgradeSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !upgraded {
		t.Fatal("expected schema 2 database to be upgraded")
	}
	var version, staleCount, detectionCount int
	if err := historyDB.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil || version != 3 {
		t.Fatalf("unexpected schema version: %d err=%v", version, err)
	}
	if err := historyDB.QueryRow("SELECT COUNT(*) FROM history_jobs WHERE job_id=?", staleID).Scan(&staleCount); err != nil || staleCount != 1 {
		t.Fatalf("schema upgrade deleted an unmatched history job: count=%d err=%v", staleCount, err)
	}
	if err := historyDB.QueryRow("SELECT COUNT(*) FROM history_detections WHERE job_id=? AND source_file='/scan/backfill.txt' AND detection_reason='Backfill-Signature'", jobID).Scan(&detectionCount); err != nil || detectionCount != 1 {
		t.Fatalf("schema upgrade did not backfill detection: count=%d err=%v", detectionCount, err)
	}
}

func TestRestartHistoryPeriodicTimerDiscardsOldDeadline(t *testing.T) {
	oldTimer := time.NewTimer(5 * time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	newTimer := restartHistoryPeriodicTimer(oldTimer, 100*time.Millisecond)
	defer newTimer.Stop()
	if newTimer == oldTimer {
		t.Fatal("history timer was reset in place instead of being replaced")
	}

	select {
	case <-oldTimer.C:
		t.Fatal("the retired timer deadline fired after reset")
	case <-time.After(20 * time.Millisecond):
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
