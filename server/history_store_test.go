package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAdminIndexRebuildCoversAllOwnersAndIgnoresMtime(t *testing.T) {
	s := newScanTestServer(t)
	var jobs []*scanJob
	for _, owner := range []string{testAliceUserID, testBobUserID} {
		job := s.history.newScanJob("manual", owner, "/scan/file", "warn", false)
		if err := s.history.finishScanJob(t.Context(), job, scanSummary{}); err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	path := filepath.Join(s.cfg.JobsDir, jobs[0].JobID+".json")
	info, _ := os.Stat(path)
	jobs[0].Result = "found"
	jobs[0].Detections = []detectionItem{{"/scan/file", "Test"}}
	data, _ := json.Marshal(jobs[0])
	os.WriteFile(path, data, 0640)
	os.Chtimes(path, info.ModTime(), info.ModTime())
	request := func(role string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/results/index", nil)
		r = r.WithContext(context.WithValue(r.Context(), actorContextKey{}, actor{ID: testAliceUserID, Username: "alice", Role: role}))
		w := httptest.NewRecorder()
		s.handleResultsIndex(w, r)
		return w
	}
	if w := request("user"); w.Code != 403 {
		t.Fatalf("non-admin code %d", w.Code)
	}
	if w := request("admin"); w.Code != 200 {
		t.Fatalf("admin code %d %s", w.Code, w.Body.String())
	} else {
		var result indexRebuildResult
		json.Unmarshal(w.Body.Bytes(), &result)
		if result.Updated != 2 {
			t.Fatalf("not all owners rebuilt: %+v", result)
		}
	}
	var result string
	s.historyDB.QueryRow("SELECT result FROM history_jobs WHERE job_id=?", jobs[0].JobID).Scan(&result)
	if result != "found" {
		t.Fatalf("mtime optimization applied: %s", result)
	}
	s.indexRebuilding.Store(true)
	if w := request("admin"); w.Code != 409 {
		t.Fatalf("concurrent rebuild %d", w.Code)
	}
}

func TestHistoryRebuildSerializesWithResultWriters(t *testing.T) {
	s := newScanTestServer(t)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job := s.history.newScanJob("manual", testAliceUserID, "/scan/file", "warn", false)
			if err := s.history.finishScanJob(t.Context(), job, scanSummary{}); err != nil {
				t.Error(err)
			}
		}()
	}
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.history.rebuild(t.Context()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var count int
	if err := s.historyDB.QueryRow("SELECT COUNT(*) FROM history_jobs").Scan(&count); err != nil || count != 10 {
		t.Fatalf("concurrent rebuild lost records: count=%d err=%v", count, err)
	}
}

func TestRebuildPreservesValidRowsForCorruptionAndRollsBackOnCancel(t *testing.T) {
	s := newScanTestServer(t)
	job := s.history.newScanJob("manual", testAliceUserID, "/scan/file", "warn", false)
	if err := s.history.finishScanJob(t.Context(), job, scanSummary{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.cfg.JobsDir, job.JobID+".json")
	os.WriteFile(path, []byte("{"), 0600)
	if result, err := s.history.rebuild(t.Context()); err != nil || result.Failed != 1 || result.Status != "partial" {
		t.Fatalf("result %+v error %v", result, err)
	}
	if err := s.history.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	s.historyDB.QueryRow("SELECT COUNT(*) FROM history_jobs").Scan(&count)
	if count != 1 {
		t.Fatal("corrupt document removed last valid index")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.history.rebuild(ctx); err == nil {
		t.Fatal("canceled rebuild succeeded")
	}
	s.historyDB.QueryRow("SELECT COUNT(*) FROM history_jobs").Scan(&count)
	if count != 1 {
		t.Fatal("canceled rebuild deleted index")
	}
}

func TestHistoryStoreRecoversInterruptedJobsWithoutRequeue(t *testing.T) {
	s := newScanTestServer(t)
	job := s.history.newScanJob("manual", testAliceUserID, "/scan/file", "warn", false)
	if err := s.history.saveScanJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if err := s.history.recoverInterrupted(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	s.historyDB.QueryRow("SELECT status FROM history_jobs WHERE job_id=?", job.JobID).Scan(&status)
	if status != "failed" || len(s.batches) != 0 {
		t.Fatalf("status %s batches %v", status, s.batches)
	}
}

func TestPeriodicIndexReconciliationStillRuns(t *testing.T) {
	s := newScanTestServer(t)
	s.historyIntervalChanged = make(chan struct{}, 1)
	s.appConfig.mu.Lock()
	s.appConfig.cfg.HistoryIndexRefreshInterval = 1
	s.appConfig.mu.Unlock()
	job := s.history.newScanJob("manual", testAliceUserID, "/scan/file", "warn", false)
	if err := s.history.finishScanJob(t.Context(), job, scanSummary{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.historyDB.Exec("DELETE FROM history_jobs"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.runHistoryIndexer(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		s.historyDB.QueryRow("SELECT COUNT(*) FROM history_jobs").Scan(&count)
		if count == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("periodic refresh did not restore index")
}
