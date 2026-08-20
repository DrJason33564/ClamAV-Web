package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var scanQueueLockPollInterval = 5 * time.Second

type scanBatch struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	Action     string     `json:"action"`
	Targets    []string   `json:"targets"`
	JobIDs     []string   `json:"job_ids"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Current    string     `json:"current,omitempty"`
	Message    string     `json:"message"`
	Completed  int        `json:"completed"`
	Failed     int        `json:"failed"`
	Threats    int        `json:"threats"`
	LastError  string     `json:"last_error,omitempty"`
	UserID     string     `json:"-"`
	Username   string     `json:"-"`
}

type startScanRequest struct {
	Targets []string `json:"targets"`
	Action  string   `json:"action"`
	// Wait remains accepted for API compatibility; queue scheduling no longer
	// forwards it as --wait to scan_once.sh.
	Wait bool `json:"wait"`
}

type startScanResponse struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type scanQueueItem struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	JobIDs      []string   `json:"job_ids"`
	Targets     []string   `json:"targets"`
	Action      string     `json:"action"`
	StartedAt   *time.Time `json:"started_at"`
	QueueNumber int        `json:"queue_number"`
}

type reorderScanRequest struct {
	ID          string `json:"id"`
	QueueNumber int    `json:"queue_number"`
}

type cancelScanRequest struct {
	ID     string `json:"id"`
	Cancel string `json:"cancel"`
}

type scanActionResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type jobFile struct {
	ID    string
	State map[string]any
}

func (s *server) startScan(w http.ResponseWriter, r *http.Request) {
	sleeping, err := directoryLockExists(s.cfg.SleepLockDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("check ClamAV sleep lock: %w", err))
		return
	}
	if sleeping {
		s.warn("scan", "scan request rejected while ClamAV is sleeping")
		// A rejected request never enters the in-memory queue and therefore
		// cannot reach scan_once.sh after ClamAV has been put to sleep.
		writeJSON(w, http.StatusConflict, startScanResponse{
			ID:      "",
			Status:  "failed",
			Message: "ClamAV is sleeping.",
		})
		return
	}

	var req startScanRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	action := strings.TrimSpace(req.Action)
	if action == "" {
		action = "warn"
	}
	if action != "warn" && action != "move" && action != "remove" {
		writeError(w, http.StatusBadRequest, errors.New("action must be warn, move, or remove"))
		return
	}

	targets := make([]string, 0, len(req.Targets))
	seen := map[string]bool{}
	who, _ := actorFromRequest(r)
	for _, target := range req.Targets {
		path, err := s.safePathForActor(target, who)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("%s: %w", target, err))
			return
		}
		if _, err := os.Stat(path); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("%s: %w", path, err))
			return
		}
		if !seen[path] {
			seen[path] = true
			targets = append(targets, path)
		}
	}
	if len(targets) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("at least one target is required"))
		return
	}

	batch := &scanBatch{
		ID:      "web-" + randomHex(8),
		Status:  "queued",
		Action:  action,
		Targets: targets,
		Message: "Queued",
	}
	batch.UserID = who.ID
	batch.Username = who.Username

	status, message := s.enqueueBatch(batch)
	s.info("scan", "scan batch queued", "batch_id", batch.ID, "user_id", batch.UserID, "user", batch.Username, "targets", len(batch.Targets), "action", batch.Action)
	writeJSON(w, http.StatusAccepted, startScanResponse{
		ID:      batch.ID,
		Status:  status,
		Message: message,
	})
}

func (s *server) enqueueBatch(batch *scanBatch) (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches[batch.ID] = batch
	s.queuedBatchIDs = append(s.queuedBatchIDs, batch.ID)
	s.startQueueRunnerLocked()
	snapshot := s.cloneBatchLocked(batch.ID)
	if snapshot == nil {
		return "queued", "Queued"
	}
	return snapshot.Status, snapshot.Message
}

func (s *server) listScans(w http.ResponseWriter, r *http.Request) {
	who, _ := actorFromRequest(r)
	items, err := s.listQueueItems(r.URL.Query().Get("scope"), who.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *server) listQueueItems(scope string, userIDs ...string) ([]scanQueueItem, error) {
	userID := ""
	if len(userIDs) > 0 {
		userID = userIDs[0]
	}
	start, end, all, err := parseScanScope(scope)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]scanQueueItem, 0, len(s.queuedBatchIDs)+1)
	if all || start == 1 {
		if active := s.cloneBatchLocked(s.activeBatchID); active != nil && (userID == "" || active.UserID == userID) {
			items = append(items, queueItem(active, 0))
		}
	}
	queueNumber := 0
	for _, id := range s.queuedBatchIDs {
		batch := s.cloneBatchLocked(id)
		if batch == nil || (userID != "" && batch.UserID != userID) {
			continue
		}
		queueNumber++
		if !all && (queueNumber < start || queueNumber > end) {
			continue
		}
		items = append(items, queueItem(batch, queueNumber))
	}
	return items, nil
}

func parseScanScope(scope string) (int, int, bool, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" || scope == "all" {
		return 0, 0, true, nil
	}
	startText, endText, ok := strings.Cut(scope, "-")
	if !ok {
		return 0, 0, false, errors.New("scope must be all or a numeric range like 1-10")
	}
	start, err := strconv.Atoi(strings.TrimSpace(startText))
	if err != nil || start < 1 {
		return 0, 0, false, errors.New("scope start must be a positive number")
	}
	end, err := strconv.Atoi(strings.TrimSpace(endText))
	if err != nil || end < start {
		return 0, 0, false, errors.New("scope end must be greater than or equal to start")
	}
	return start, end, false, nil
}

func queueItem(batch *scanBatch, queueNumber int) scanQueueItem {
	return scanQueueItem{
		ID:          batch.ID,
		Status:      batch.Status,
		JobIDs:      append([]string(nil), batch.JobIDs...),
		Targets:     append([]string(nil), batch.Targets...),
		Action:      batch.Action,
		StartedAt:   batch.StartedAt,
		QueueNumber: queueNumber,
	}
}

func (s *server) handleScanReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req reorderScanRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	who, _ := actorFromRequest(r)
	if err := s.reorderQueuedBatch(strings.TrimSpace(req.ID), req.QueueNumber, who.ID); err != nil {
		s.warn("scan", "scan queue reorder rejected", "batch_id", strings.TrimSpace(req.ID), "user", who.Username, "error", err)
		writeJSON(w, http.StatusBadRequest, scanActionResponse{Status: "failed", Message: err.Error()})
		return
	}
	s.info("scan", "scan queue reordered", "batch_id", strings.TrimSpace(req.ID), "user", who.Username, "queue_number", req.QueueNumber)
	writeJSON(w, http.StatusOK, scanActionResponse{Status: "success", Message: "Queue reordered"})
}

func (s *server) handleScanCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req cancelScanRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	who, _ := actorFromRequest(r)
	if err := s.cancelQueuedBatch(strings.TrimSpace(req.ID), strings.TrimSpace(req.Cancel), who.ID); err != nil {
		s.warn("scan", "queued scan cancellation rejected", "batch_id", strings.TrimSpace(req.ID), "user", who.Username, "error", err)
		writeJSON(w, http.StatusBadRequest, scanActionResponse{Status: "failed", Message: err.Error()})
		return
	}
	s.info("scan", "queued scan canceled", "batch_id", strings.TrimSpace(req.ID), "user", who.Username)
	writeJSON(w, http.StatusOK, scanActionResponse{Status: "success", Message: "Queued scan canceled"})
}

func (s *server) runBatch(id string) {
	batch := s.batchSnapshot(id)
	if batch == nil {
		return
	}
	targets := append([]string(nil), batch.Targets...)
	action := batch.Action
	userID := batch.UserID
	username := batch.Username
	s.info("scan", "scan batch started", "batch_id", id, "user_id", userID, "user", username, "targets", len(targets), "action", action)

	for i, target := range targets {
		targetStarted := time.Now()
		s.debug("scan", "scan target started", "batch_id", id, "user", username, "target", target, "position", i+1, "total", len(targets))
		s.updateBatch(id, func(batch *scanBatch) {
			batch.Current = target
			batch.Message = fmt.Sprintf("Scanning %d of %d", i+1, len(targets))
		})

		args := []string{"--type", "manual", "--user", userID, "--target", target, "--action", action}
		jobID, waitScan, err := s.startScanScript(args)
		if jobID != "" {
			s.updateBatch(id, func(batch *scanBatch) {
				batch.JobIDs = append(batch.JobIDs, jobID)
			})
		}
		output := ""
		if err == nil {
			output, err = waitScan()
		}

		state, _ := s.jobState(jobID)
		result := jobResult(state)
		if err != nil || result == "error" {
			s.error("scan", "scan target failed", "batch_id", id, "job_id", jobID, "user", username, "target", target, "duration_ms", time.Since(targetStarted).Milliseconds(), "error", errString(err))
		} else {
			s.debug("scan", "scan target completed", "batch_id", id, "job_id", jobID, "user", username, "target", target, "result", result, "duration_ms", time.Since(targetStarted).Milliseconds())
		}
		s.updateBatch(id, func(batch *scanBatch) {
			batch.Completed++
			if result == "found" {
				batch.Threats++
			}
			if err != nil || result == "error" {
				batch.Failed++
				if output != "" {
					batch.LastError = output
				} else {
					batch.LastError = errString(err)
				}
			}
		})
	}

	now := time.Now()
	s.updateBatch(id, func(batch *scanBatch) {
		batch.FinishedAt = &now
		batch.Current = ""
		switch {
		case batch.Failed > 0:
			batch.Status = "failed"
			batch.Message = "Finished with errors"
		case batch.Threats > 0:
			batch.Status = "finished"
			batch.Message = "Threats found"
		default:
			batch.Status = "finished"
			batch.Message = "All scans finished cleanly"
		}
	})
	finished := s.batchSnapshot(id)
	if finished != nil {
		s.info("scan", "scan batch completed", "batch_id", id, "user", username, "status", finished.Status, "completed", finished.Completed, "failed", finished.Failed, "threats", finished.Threats)
	}
	s.finishBatch(id)
	// Manual scans can be indexed immediately; the periodic pass remains the
	// source of truth for cron jobs and for repairing interrupted updates.
	if s.history != nil {
		go func() { _ = s.history.refresh(context.Background()) }()
	}
}

func (s *server) startScanScript(args []string) (string, func() (string, error), error) {
	cmd := exec.Command(s.cfg.ScanScript, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Serialize the process start with ClamAV power transitions. Once Start
	// succeeds, bumping the timer generation prevents a simultaneous stale
	// expiry from shutting clamd down underneath the new manual scan.
	s.clamavPowerMu.Lock()
	err = cmd.Start()
	if err == nil {
		s.notifyClamAVSleepTimerChanged("manual_scan_started")
	}
	s.clamavPowerMu.Unlock()
	if err != nil {
		return "", nil, err
	}

	scanner := bufio.NewScanner(stdout)
	jobID := ""
	var stdoutRest bytes.Buffer
	if !scanner.Scan() {
		scanErr := scanner.Err()
		waitErr := cmd.Wait()
		if scanErr != nil {
			return "", nil, scanErr
		}
		if waitErr != nil {
			return "", nil, waitErr
		}
		return "", nil, errors.New("scan script did not return a job id")
	}

	line := strings.TrimSpace(scanner.Text())
	if strings.HasPrefix(line, "JOB_ID=") {
		jobID = strings.TrimPrefix(line, "JOB_ID=")
	} else {
		stdoutRest.WriteString(line)
		stdoutRest.WriteByte('\n')
	}

	done := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			stdoutRest.WriteString(scanner.Text())
			stdoutRest.WriteByte('\n')
		}
		done <- scanner.Err()
	}()

	waitScan := func() (string, error) {
		scanErr := <-done
		err := cmd.Wait()
		output := strings.TrimSpace(stdoutRest.String() + stderr.String())
		if scanErr != nil {
			return output, scanErr
		}
		if jobID == "" && err == nil {
			err = errors.New("scan script did not return a job id")
		}
		return output, err
	}

	return jobID, waitScan, nil
}

func (s *server) runScanScript(args []string) (string, string, error) {
	jobID, waitScan, err := s.startScanScript(args)
	if err != nil {
		return jobID, "", err
	}
	output, err := waitScan()
	return jobID, output, err
}

func (s *server) jobState(jobID string) (any, string) {
	path := filepath.Join(s.cfg.JobsDir, jobID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{"status": "pending", "message": err.Error()}, ""
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return string(data), ""
	}

	logPath, _ := decoded["log_file"].(string)
	logText := ""
	if logPath != "" {
		logText = tailFile(logPath, s.cfg.MaxLogBytes)
	}
	return decoded, logText
}

func (s *server) batchSnapshotsFromJobs() map[string]*scanBatch {
	jobs := s.readJobFiles()
	grouped := make(map[string][]jobFile)
	for _, job := range jobs {
		batchID, ok := jobBatchID(job.ID)
		if !ok {
			continue
		}
		grouped[batchID] = append(grouped[batchID], job)
	}

	batches := make(map[string]*scanBatch, len(grouped))
	for batchID, batchJobs := range grouped {
		sort.Slice(batchJobs, func(i, j int) bool {
			return batchJobs[i].ID < batchJobs[j].ID
		})
		batches[batchID] = s.batchFromJobs(batchID, batchJobs)
	}
	return batches
}

func (s *server) readJobFiles() []jobFile {
	entries, err := os.ReadDir(s.cfg.JobsDir)
	if err != nil {
		return nil
	}

	jobs := make([]jobFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(s.cfg.JobsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			continue
		}

		id, _ := decoded["job_id"].(string)
		if id == "" {
			id = strings.TrimSuffix(entry.Name(), ".json")
		}
		jobs = append(jobs, jobFile{ID: id, State: decoded})
	}
	return jobs
}

func (s *server) batchFromJobs(batchID string, jobs []jobFile) *scanBatch {
	batch := &scanBatch{
		ID:      batchID,
		Status:  "finished",
		Message: "All scans finished cleanly",
	}

	var latestFinish *time.Time
	running := false
	for _, job := range jobs {
		state := job.State
		batch.JobIDs = append(batch.JobIDs, job.ID)
		if target, ok := state["target"].(string); ok {
			batch.Targets = append(batch.Targets, target)
		}
		if action, ok := state["action"].(string); ok && batch.Action == "" {
			batch.Action = action
		}

		if started, ok := parseJobTime(state["started_at"]); ok {
			if batch.StartedAt == nil || started.Before(*batch.StartedAt) {
				copyStarted := started
				batch.StartedAt = &copyStarted
			}
		}

		status, _ := state["status"].(string)
		result := jobResult(state)
		switch {
		case status == "running":
			running = true
			batch.Current, _ = state["target"].(string)
		case status == "finished" || status == "failed":
			batch.Completed++
			if finished, ok := parseJobTime(state["finished_at"]); ok {
				if latestFinish == nil || finished.After(*latestFinish) {
					copyFinish := finished
					latestFinish = &copyFinish
				}
			}
		}

		if result == "found" {
			batch.Threats++
		}
		if status == "failed" || result == "error" {
			batch.Failed++
			if msg, ok := state["message"].(string); ok {
				batch.LastError = msg
			}
		}
	}

	if latestFinish != nil {
		batch.FinishedAt = latestFinish
	}

	switch {
	case running:
		batch.Status = "running"
		batch.Message = fmt.Sprintf("Scanning %d of %d", batch.Completed+1, len(batch.JobIDs))
	case batch.Failed > 0:
		batch.Status = "failed"
		batch.Message = "Finished with errors"
	case batch.Threats > 0:
		batch.Status = "finished"
		batch.Message = "Threats found"
	default:
		batch.Status = "finished"
		batch.Message = "All scans finished cleanly"
	}
	return batch
}

func jobBatchID(jobID string) (string, bool) {
	if !strings.HasPrefix(jobID, "web-") {
		if strings.HasPrefix(jobID, "manual-") || strings.HasPrefix(jobID, "cron-") {
			return stripNumericSuffix(jobID), true
		}
		return "", false
	}
	pos := strings.LastIndex(jobID, "-")
	if pos <= len("web-") || pos == len(jobID)-1 {
		return "", false
	}
	suffix := jobID[pos+1:]
	for _, ch := range suffix {
		if ch < '0' || ch > '9' {
			return "", false
		}
	}
	return jobID[:pos], true
}

func stripNumericSuffix(jobID string) string {
	pos := strings.LastIndex(jobID, "-")
	if pos == -1 || pos == len(jobID)-1 {
		return jobID
	}
	suffix := jobID[pos+1:]
	for _, ch := range suffix {
		if ch < '0' || ch > '9' {
			return jobID
		}
	}
	if len(suffix) == 3 {
		return jobID[:pos]
	}
	return jobID
}

func parseJobTime(value any) (time.Time, bool) {
	text, ok := value.(string)
	if !ok || text == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse("20060102150405", text); err == nil {
		return parsed, true
	}
	if parsed, err := time.Parse(time.RFC3339, text); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

func jobResult(state any) string {
	m, ok := state.(map[string]any)
	if !ok {
		return "unknown"
	}
	if result, ok := m["result"].(string); ok {
		return result
	}
	if status, ok := m["status"].(string); ok && status == "failed" {
		return "error"
	}
	return "unknown"
}

func (s *server) startQueueRunnerLocked() {
	if s.queueRunnerActive {
		return
	}
	s.queueRunnerActive = true
	go s.runQueue()
}

func (s *server) runQueue() {
	for {
		id, ok := s.nextQueuedBatchID()
		if !ok {
			return
		}

		blocked, err := s.scanLockBlocksManualStart()
		if err != nil || blocked {
			if err != nil {
				s.warn("scan", "scan queue lock check failed", "batch_id", id, "error", err)
			} else {
				s.debug("scan", "scan batch waiting for active scan lock", "batch_id", id)
			}
			s.updateQueuedBatchMessage(id, "Queued; waiting for active scan lock")
			time.Sleep(scanQueueLockPollInterval)
			continue
		}

		if !s.activateQueuedBatch(id) {
			continue
		}
		s.runBatch(id)
	}
}

func (s *server) nextQueuedBatchID() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	valid := s.queuedBatchIDs[:0]
	users := make([]string, 0)
	seenUsers := map[string]bool{}
	for _, id := range s.queuedBatchIDs {
		batch, ok := s.batches[id]
		if !ok {
			continue
		}
		valid = append(valid, id)
		if !seenUsers[batch.UserID] {
			seenUsers[batch.UserID] = true
			users = append(users, batch.UserID)
		}
	}
	s.queuedBatchIDs = valid
	if len(users) > 0 {
		selected := users[randomIndex(len(users))]
		for _, id := range s.queuedBatchIDs {
			if s.batches[id].UserID == selected {
				return id, true
			}
		}
	}
	if s.activeBatchID == "" {
		s.queueRunnerActive = false
	}
	return "", false
}

func (s *server) activateQueuedBatch(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeBatchID != "" {
		return false
	}
	batch, ok := s.batches[id]
	if !ok {
		return false
	}
	index := indexOfString(s.queuedBatchIDs, id)
	if index < 0 {
		return false
	}
	s.queuedBatchIDs = append(s.queuedBatchIDs[:index], s.queuedBatchIDs[index+1:]...)
	now := time.Now()
	batch.StartedAt = &now
	batch.Status = "running"
	batch.Message = "Running"
	s.activeBatchID = id
	return true
}

func (s *server) updateQueuedBatchMessage(id string, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeBatchID != "" || indexOfString(s.queuedBatchIDs, id) < 0 {
		return
	}
	if batch, ok := s.batches[id]; ok && batch.Status == "queued" {
		batch.Message = message
	}
}

func (s *server) finishBatch(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeBatchID == id {
		s.activeBatchID = ""
	}
	// Current-queue state is intentionally memory-only. Completed batches are
	// removed here; their durable scan history remains under /state/jobs.
	delete(s.batches, id)
}

func (s *server) scanLockBlocksManualStart() (bool, error) {
	info, err := os.Stat(s.cfg.ScanLockDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return true, err
	}
	if !info.IsDir() {
		return true, nil
	}

	pidData, err := os.ReadFile(filepath.Join(s.cfg.ScanLockDir, "pid"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return true, err
	}
	pidText := strings.TrimSpace(string(pidData))
	if pidText == "" {
		return false, nil
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return false, nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, nil
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, syscall.EPERM) {
			return true, nil
		}
		return false, nil
	}
	return true, nil
}

func (s *server) reorderQueuedBatch(id string, targetNumber int, userIDs ...string) error {
	userID := ""
	if len(userIDs) > 0 {
		userID = userIDs[0]
	}
	if id == "" {
		return errors.New("id is required")
	}
	if targetNumber < 1 {
		return errors.New("queue_number must be greater than or equal to 1")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.activeBatchID {
		return errors.New("running scan cannot be reordered")
	}
	current := indexOfString(s.queuedBatchIDs, id)
	if current < 0 || s.batches[id] == nil || (userID != "" && s.batches[id].UserID != userID) {
		return errors.New("queued scan not found")
	}
	owned := make([]string, 0)
	for _, queuedID := range s.queuedBatchIDs {
		if batch := s.batches[queuedID]; batch != nil && (userID == "" || batch.UserID == userID) {
			owned = append(owned, queuedID)
		}
	}
	if targetNumber > len(owned) {
		targetNumber = len(owned)
	}
	ownedCurrent := indexOfString(owned, id)
	owned = append(owned[:ownedCurrent], owned[ownedCurrent+1:]...)
	target := targetNumber - 1
	owned = append(owned, "")
	copy(owned[target+1:], owned[target:])
	owned[target] = id
	n := 0
	for i, queuedID := range s.queuedBatchIDs {
		if batch := s.batches[queuedID]; batch != nil && (userID == "" || batch.UserID == userID) {
			s.queuedBatchIDs[i] = owned[n]
			n++
		}
	}
	return nil
}

func (s *server) cancelQueuedBatch(id string, confirm string, userIDs ...string) error {
	userID := ""
	if len(userIDs) > 0 {
		userID = userIDs[0]
	}
	if id == "" {
		return errors.New("id is required")
	}
	if confirm != "Y" {
		return errors.New("cancel must be Y")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.activeBatchID {
		return errors.New("running scan cannot be canceled")
	}
	index := indexOfString(s.queuedBatchIDs, id)
	if index < 0 || s.batches[id] == nil || (userID != "" && s.batches[id].UserID != userID) {
		return errors.New("queued scan not found")
	}
	s.queuedBatchIDs = append(s.queuedBatchIDs[:index], s.queuedBatchIDs[index+1:]...)
	delete(s.batches, id)
	return nil
}

func randomIndex(length int) int {
	if length <= 1 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return int(time.Now().UnixNano() % int64(length))
	}
	var value uint64
	for _, part := range b {
		value = value<<8 | uint64(part)
	}
	return int(value % uint64(length))
}

func (s *server) queueItemsLocked() []scanQueueItem {
	items := make([]scanQueueItem, 0, len(s.queuedBatchIDs)+1)
	if active := s.cloneBatchLocked(s.activeBatchID); active != nil {
		items = append(items, queueItem(active, 0))
	}
	for index, id := range s.queuedBatchIDs {
		if batch := s.cloneBatchLocked(id); batch != nil {
			items = append(items, queueItem(batch, index+1))
		}
	}
	return items
}

func indexOfString(values []string, needle string) int {
	for i, value := range values {
		if value == needle {
			return i
		}
	}
	return -1
}

func (s *server) batchSnapshot(id string) *scanBatch {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cloneBatchLocked(id)
}

func (s *server) updateBatch(id string, fn func(*scanBatch)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch, ok := s.batches[id]; ok {
		fn(batch)
	}
}

func cloneBatch(batch *scanBatch) *scanBatch {
	copyBatch := *batch
	copyBatch.Targets = append([]string(nil), batch.Targets...)
	copyBatch.JobIDs = append([]string(nil), batch.JobIDs...)
	return &copyBatch
}

func (s *server) cloneBatchLocked(id string) *scanBatch {
	if id == "" {
		return nil
	}
	batch, ok := s.batches[id]
	if !ok {
		return nil
	}
	return cloneBatch(batch)
}

func mergeBatchMetadata(dst, src *scanBatch) {
	if len(src.Targets) > len(dst.Targets) {
		dst.Targets = append([]string(nil), src.Targets...)
	}
	if dst.Action == "" {
		dst.Action = src.Action
	}
	if dst.Status == "running" && len(dst.Targets) > 0 {
		dst.Message = fmt.Sprintf("Scanning %d of %d", dst.Completed+1, len(dst.Targets))
	}
}

func tailFile(path string, maxBytes int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return ""
	}
	offset := info.Size() - maxBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), int(maxBytes)+1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if offset > 0 && len(lines) > 0 {
		lines[0] = "... log truncated ..."
	}
	return strings.Join(lines, "\n")
}

func randomHex(bytesLen int) string {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
