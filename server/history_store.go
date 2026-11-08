package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

var lastScanJobTime atomic.Int64

type scanIssue struct {
	File  string `json:"file"`
	Stage string `json:"stage"`
	Error string `json:"error"`
}
type scanSummary struct {
	Scanned         int             `json:"scanned"`
	ScanErrors      int             `json:"scan_errors"`
	ActionSucceeded int             `json:"action_succeeded"`
	ActionFailed    int             `json:"action_failed"`
	Detections      []detectionItem `json:"detections"`
	Errors          []scanIssue     `json:"errors"`
}
type scanJob struct {
	indexedJobDocument
	Target   string      `json:"target"`
	LogFile  string      `json:"log_file"`
	Message  string      `json:"message"`
	LockFile bool        `json:"LOCK_FILE"`
	ExitCode *int        `json:"exit_code"`
	PID      *int        `json:"pid"`
	Summary  scanSummary `json:"summary"`
}

func (h *HistoryStore) newScanJob(kind, owner, target, action string, lock bool) *scanJob {
	// Monotonic nanosecond IDs retain the legacy manual-/cron- numeric format.
	var n int64
	for {
		old := lastScanJobTime.Load()
		n = time.Now().UnixNano()
		if n <= old {
			n = old + 1
		}
		if lastScanJobTime.CompareAndSwap(old, n) {
			break
		}
	}
	id := fmt.Sprintf("%s-%d", kind, n)
	return &scanJob{indexedJobDocument: indexedJobDocument{Version: 4, JobID: id, Type: kind, UserID: owner, Action: action, Status: "running", Result: "unknown", StartedAt: time.Now().Unix()}, Target: target, LogFile: filepath.Join(h.logDir, id+".log"), Message: "Scan is running.", LockFile: lock}
}

func (h *HistoryStore) saveScanJob(ctx context.Context, job *scanJob) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.saveScanJobLocked(ctx, job)
}
func (h *HistoryStore) saveScanJobLocked(ctx context.Context, job *scanJob) error {
	if err := os.MkdirAll(h.jobsDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(h.jobsDir, job.JobID+".json")
	// Readers, cleanup, and indexers use the same store mutex. A short write
	// after a crash is diagnosed on refresh; it never deletes a valid index.
	if err := os.WriteFile(path, append(data, '\n'), 0640); err != nil {
		return err
	}
	return h.refreshFileLocked(ctx, path)
}
func (h *HistoryStore) finishScanJob(ctx context.Context, job *scanJob, summary scanSummary) error {
	job.Summary = summary
	job.Detections = summary.Detections
	now := time.Now().Unix()
	job.FinishedAt = &now
	code := 0
	job.Status = "finished"
	job.Result = "clean"
	job.Message = "Scan finished cleanly."
	if len(summary.Detections) > 0 {
		code = 1
		job.Result = "found"
		job.Message = "Threat found while scanning."
		path := filepath.Join(h.logDir, "clamav_detection_"+job.JobID+".log")
		job.DetectionLog = &path
	} else if summary.ScanErrors > 0 || summary.ActionFailed > 0 {
		code = 2
		job.Status = "failed"
		job.Result = "error"
		job.Message = "Scan finished with errors."
	}
	job.ExitCode = &code
	return h.saveScanJob(ctx, job)
}

// Recover unfinished records as interrupted history, never as runnable work.
func (h *HistoryStore) recoverInterrupted(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	entries, err := os.ReadDir(h.jobsDir)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(h.jobsDir, 0755)
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(h.jobsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var job scanJob
		if json.Unmarshal(data, &job) != nil || (job.Status != "running" && job.Status != "waiting") {
			continue
		}
		if _, _, err := h.readIndexedDocument(path, entry.Name()); err != nil {
			h.warn("interrupted job is invalid", "path", path, "error", err)
			continue
		}
		job.Status = "failed"
		job.Result = "error"
		job.Message = "Scan interrupted by server restart."
		now := time.Now().Unix()
		job.FinishedAt = &now
		code := 2
		job.ExitCode = &code
		if err := h.saveScanJobLocked(ctx, &job); err != nil {
			return err
		}
	}
	return nil
}

type indexRebuildResult struct {
	Status  string `json:"status"`
	Checked int    `json:"checked"`
	Updated int    `json:"updated"`
	Failed  int    `json:"failed"`
}

func (s *server) handleResultsIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := requireAdminActor(r); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	if !s.indexRebuilding.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, errors.New("history index rebuild is already running"))
		return
	}
	defer s.indexRebuilding.Store(false)
	result, err := s.history.rebuild(r.Context())
	if err != nil {
		s.error("history", "history index rebuild failed", "error", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.info("history", "all history indexes rebuilt", "checked", result.Checked, "updated", result.Updated, "failed", result.Failed)
	writeJSON(w, http.StatusOK, result)
}

// Rebuild all owners in one transaction. Invalid files retain their last good
// rows; only paths absent from a fully enumerated directory are reconciled.
func (h *HistoryStore) rebuild(ctx context.Context) (result indexRebuildResult, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	result.Status = "success"
	entries, err := os.ReadDir(h.jobsDir)
	if err != nil {
		return result, err
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT json_file FROM history_jobs")
	if err != nil {
		return result, err
	}
	missing := map[string]bool{}
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			rows.Close()
			return result, err
		}
		missing[path] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(h.jobsDir, entry.Name())
		delete(missing, path)
		result.Checked++
		info, statErr := entry.Info()
		var job indexedJobDocument
		var detections []detectionItem
		if statErr == nil && info.Mode().IsRegular() {
			job, detections, statErr = h.readIndexedDocument(path, entry.Name())
		} else if statErr == nil {
			statErr = errors.New("not a regular task file")
		}
		if statErr != nil {
			result.Failed++
			h.warn("index rebuild skipped task", "path", path, "error", statErr)
			continue
		}
		if err = indexDocument(ctx, tx, job, detections, path, info.ModTime().UnixNano()); err != nil {
			return result, err
		}
		result.Updated++
	}
	for path := range missing {
		if _, err = tx.ExecContext(ctx, "DELETE FROM history_jobs WHERE json_file=?", path); err != nil {
			return result, err
		}
	}
	if result.Failed > 0 {
		result.Status = "partial"
	}
	return result, tx.Commit()
}
