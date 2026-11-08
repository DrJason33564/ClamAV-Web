package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type scanBatch struct {
	LockFile    bool       `json:"-"`
	Type        string     `json:"-"`
	Wake        bool       `json:"-"`
	Actor       actor      `json:"-"`
	SubmittedAt time.Time  `json:"-"`
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	Action      string     `json:"action"`
	Targets     []string   `json:"targets"`
	JobIDs      []string   `json:"job_ids"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Current     string     `json:"current,omitempty"`
	Message     string     `json:"message"`
	Completed   int        `json:"completed"`
	Failed      int        `json:"failed"`
	Threats     int        `json:"threats"`
	LastError   string     `json:"last_error,omitempty"`
	UserID      string     `json:"-"`
	Username    string     `json:"-"`
}

type startScanRequest struct {
	Targets []string `json:"targets"`
	Action  string   `json:"action"`
	// Wait remains accepted for API compatibility; all scans use the queue.
	Wait     bool            `json:"wait"`
	LockFile json.RawMessage `json:"LOCK_FILE"`
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

func (s *server) startScan(w http.ResponseWriter, r *http.Request) {
	if s.isSleeping() {
		writeJSON(w, http.StatusConflict, startScanResponse{Status: "failed", Message: "ClamAV is sleeping."})
		return
	}

	var req startScanRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	lockFile := false
	if len(req.LockFile) > 0 {
		if string(req.LockFile) != "true" && string(req.LockFile) != "false" {
			writeError(w, http.StatusBadRequest, errors.New("LOCK_FILE must be true or false"))
			return
		}
		lockFile = string(req.LockFile) == "true"
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
	batch.Type = "manual"
	batch.LockFile = lockFile
	batch.Actor = who
	batch.UserID = who.ID
	batch.Username = who.Username

	// Admission and activation share the power gate. No sleep can slip between
	// checking in-memory state and reserving a queue slot.
	s.accountDeleteMu.Lock()
	defer s.accountDeleteMu.Unlock()
	if s.userDB != nil {
		var active int
		if err := s.userDB.QueryRow("SELECT COUNT(*) FROM users WHERE id=? AND status='active'", who.ID).Scan(&active); err != nil || active != 1 {
			writeError(w, http.StatusForbidden, errors.New("user is not active"))
			return
		}
	}
	s.clamavPowerMu.Lock()
	if s.isSleeping() || s.scanContext().Err() != nil {
		s.clamavPowerMu.Unlock()
		writeJSON(w, http.StatusConflict, startScanResponse{Status: "failed", Message: "ClamAV is sleeping or server is stopping."})
		return
	}
	status, message := s.enqueueBatch(batch)
	s.clamavPowerMu.Unlock()
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
	if batch.SubmittedAt.IsZero() {
		batch.SubmittedAt = time.Now()
	}
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

// The existing queue and power gate form the single in-process engine manager.
// Only activation reserves work; the long scan does not hold the power mutex.
func (s *server) runBatch(id string) {
	defer s.finishBatch(id)
	batch := s.batchSnapshot(id)
	if batch == nil {
		return
	}
	kind := batch.Type
	if kind == "" {
		kind = "manual"
	}
	var preflight error
	if kind == "cron" && s.cfg.ScanWaitMax > 0 && time.Since(batch.SubmittedAt) > s.cfg.ScanWaitMax {
		preflight = errors.New("scheduled scan exceeded queue wait limit")
	}
	s.clamavPowerMu.Lock()
	if preflight == nil && s.isSleeping() {
		if kind == "cron" && batch.Wake {
			_, _, _, preflight = s.wakeClamAV(s.scanContext())
		} else {
			preflight = errors.New("ClamAV is sleeping")
		}
	}
	s.notifyClamAVSleepTimerChanged("scan_started")
	s.clamavPowerMu.Unlock()
	for i, target := range batch.Targets {
		if s.scanContext().Err() != nil {
			break
		}
		job := s.history.newScanJob(kind, batch.UserID, target, batch.Action, batch.LockFile)
		s.updateBatch(id, func(b *scanBatch) {
			b.JobIDs = append(b.JobIDs, job.JobID)
			b.Current = target
			b.Message = fmt.Sprintf("Scanning %d of %d", i+1, len(batch.Targets))
		})
		var summary scanSummary
		err := s.history.saveScanJob(context.Background(), job)
		if err != nil {
			s.error("history", "create scan history failed", "job_id", job.JobID, "error", err)
			summary.ScanErrors++
			summary.Errors = append(summary.Errors, scanIssue{target, "history", err.Error()})
		} else if preflight != nil {
			summary = s.executeScan(s.scanContext(), job, preflight)
			s.error("scan", "scan preflight failed", "job_id", job.JobID, "error", preflight)
		} else {
			who := batch.Actor
			// Re-read permissions after queue delays, including disabled accounts
			// and TimeDock bindings changed after manual admission.
			if s.userDB != nil {
				who, err = s.scanActor(batch.UserID)
			}
			if err == nil {
				_, err = s.safePathForActor(target, who)
			}
			if err != nil {
				summary = s.executeScan(s.scanContext(), job, err)
				s.error("scan", "scan authorization failed", "job_id", job.JobID, "error", err)
			} else {
				summary = s.executeScan(s.scanContext(), job)
			}
		}
		if err := s.history.finishScanJob(context.Background(), job, summary); err != nil {
			s.error("history", "persist scan result failed", "job_id", job.JobID, "error", err)
		}
		s.updateBatch(id, func(b *scanBatch) {
			b.Completed++
			if job.Result == "found" {
				b.Threats++
			}
			if job.Result == "error" {
				b.Failed++
				b.LastError = job.Message
			}
		})
	}
	s.notifyClamAVSleepTimerChanged("scan_finished")
}

func (s *server) scanContext() context.Context {
	if s.scanCtx != nil {
		return s.scanCtx
	}
	return context.Background()
}
func (s *server) scanActor(id string) (who actor, err error) {
	err = s.userDB.QueryRow("SELECT id,username,role,timedock_account FROM users WHERE id=? AND status='active'", id).Scan(&who.ID, &who.Username, &who.Role, &who.TimeDockAccount)
	return
}

func (s *server) startQueueRunnerLocked() {
	if s.queueRunnerActive {
		return
	}
	s.queueRunnerActive = true
	s.scanWG.Add(1)
	go func() { defer s.scanWG.Done(); s.runQueue() }()
}

func (s *server) runQueue() {
	for {
		id, ok := s.nextQueuedBatchID()
		if !ok {
			return
		}

		if s.scanContext().Err() != nil {
			s.mu.Lock()
			s.queueRunnerActive = false
			s.mu.Unlock()
			return
		}
		s.clamavPowerMu.Lock()
		activated := s.activateQueuedBatch(id)
		s.clamavPowerMu.Unlock()
		if !activated {
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

func (s *server) reorderQueuedBatch(id string, targetNumber int, userID string) error {
	if id == "" {
		return errors.New("id is required")
	}
	if targetNumber < 1 {
		return errors.New("queue_number must be greater than or equal to 1")
	}
	if !validUserID(userID) {
		return errors.New("queued scan not found")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	batch := s.batches[id]
	if batch == nil || batch.UserID != userID {
		return errors.New("queued scan not found")
	}
	if id == s.activeBatchID {
		return errors.New("running scan cannot be reordered")
	}
	current := indexOfString(s.queuedBatchIDs, id)
	if current < 0 {
		return errors.New("queued scan not found")
	}
	owned := make([]string, 0)
	for _, queuedID := range s.queuedBatchIDs {
		if batch := s.batches[queuedID]; batch != nil && batch.UserID == userID {
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
		if batch := s.batches[queuedID]; batch != nil && batch.UserID == userID {
			s.queuedBatchIDs[i] = owned[n]
			n++
		}
	}
	return nil
}

func (s *server) cancelQueuedBatch(id string, confirm string, userID string) error {
	if id == "" {
		return errors.New("id is required")
	}
	if confirm != "Y" {
		return errors.New("cancel must be Y")
	}
	if !validUserID(userID) {
		return errors.New("queued scan not found")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	batch := s.batches[id]
	if batch == nil || batch.UserID != userID {
		return errors.New("queued scan not found")
	}
	if id == s.activeBatchID {
		return errors.New("running scan cannot be canceled")
	}
	index := indexOfString(s.queuedBatchIDs, id)
	if index < 0 {
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
