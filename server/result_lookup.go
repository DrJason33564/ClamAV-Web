package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var resultJobIDPattern = version3JobIDPattern

const maxResultLookupItems = 500

type resultLookup struct {
	ID        string          `json:"lookup_id"`
	Status    string          `json:"status"`
	Results   []resultLogItem `json:"results,omitempty"`
	Total     int             `json:"total"`
	Error     string          `json:"error,omitempty"`
	StartedAt time.Time       `json:"started_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	scope     resultScope
	UserID    string `json:"-"`
}

type resultLogItem struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Date   string `json:"date"`
	Result any    `json:"result"`
	Action string `json:"action"`
}

type resultJobFile struct {
	ID   string
	Type string
	Date string
}

type resultScope struct {
	All   bool
	Start int
	End   int
}

type detectionLookupRequest struct {
	JobID string `json:"job_id"`
}

type detectionLookupResponse struct {
	JobID      string          `json:"job_id"`
	Detections []detectionItem `json:"detections"`
	Original   string          `json:"original"`
	Log        string          `json:"log"`
}

func (s *server) handleResultLookupStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	scope, err := parseResultScope(r.URL.Query().Get("scope"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	who, _ := actorFromRequest(r)

	lookup := &resultLookup{
		ID:        "result-" + randomHex(8),
		Status:    "pending",
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
		scope:     scope,
		UserID:    who.ID,
	}
	if !s.startLookup(who.ID, lookup.ID, func() {
		s.resultMu.Lock()
		s.resultLookups[lookup.ID] = lookup
		s.resultMu.Unlock()
	}, func() {
		s.resultMu.Lock()
		delete(s.resultLookups, lookup.ID)
		s.resultMu.Unlock()
	}, func(ctx context.Context) {
		s.runResultLookup(ctx, lookup.ID)
	}) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many pending lookups for this user"))
		return
	}
	s.debug("results", "result lookup started", "lookup_id", lookup.ID, "user", who.Username)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "pending",
		"lookup_id": lookup.ID,
		"message":   "result list is loading; poll /api/results/lookups/" + lookup.ID,
	})
}

func (s *server) handleResultLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/results/lookups/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}

	s.resultMu.RLock()
	lookup, ok := s.resultLookups[id]
	if !ok {
		s.resultMu.RUnlock()
		http.NotFound(w, r)
		return
	}
	who, _ := actorFromRequest(r)
	if lookup.UserID != who.ID {
		s.resultMu.RUnlock()
		http.NotFound(w, r)
		return
	}
	snapshot := *lookup
	if lookup.Results != nil {
		snapshot.Results = append([]resultLogItem(nil), lookup.Results...)
	}
	s.resultMu.RUnlock()

	switch snapshot.Status {
	case "pending":
		writeJSON(w, http.StatusAccepted, snapshot)
	case "failed":
		writeJSON(w, http.StatusInternalServerError, snapshot)
	default:
		writeJSON(w, http.StatusOK, snapshot)
	}
}

func (s *server) runResultLookup(ctx context.Context, id string) {
	s.resultMu.RLock()
	lookup, ok := s.resultLookups[id]
	if !ok {
		s.resultMu.RUnlock()
		return
	}
	scope := lookup.scope
	userID := lookup.UserID
	s.resultMu.RUnlock()

	results, total, err := s.readResultLogItems(ctx, scope, userID)
	if errors.Is(err, context.Canceled) {
		return
	}
	s.resultMu.Lock()
	defer s.resultMu.Unlock()
	lookup, ok = s.resultLookups[id]
	if !ok {
		return
	}
	lookup.UpdatedAt = time.Now()
	if err != nil {
		lookup.Status = "failed"
		lookup.Error = err.Error()
		s.error("results", "result lookup failed", "lookup_id", id, "user_id", userID, "error", err)
		return
	}
	lookup.Status = "success"
	lookup.Results = results
	lookup.Total = total
	s.debug("results", "result lookup completed", "lookup_id", id, "user_id", userID, "returned", len(results), "total", total)
}

func (s *server) readResultLogItems(ctx context.Context, scope resultScope, userID string) ([]resultLogItem, int, error) {
	var total int
	if err := s.historyDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM history_jobs WHERE user_id=?", userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := "SELECT job_id,job_type,started_at,result,action FROM history_jobs WHERE user_id=? ORDER BY started_at DESC,job_id DESC"
	args := []any{userID}
	if !scope.All {
		query += " LIMIT ? OFFSET ?"
		args = append(args, scope.End-scope.Start+1, scope.Start-1)
	}
	rows, err := s.historyDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	items := []resultLogItem{}
	for rows.Next() {
		var item resultLogItem
		var startedAt int64
		if err := rows.Scan(&item.ID, &item.Type, &startedAt, &item.Result, &item.Action); err != nil {
			return nil, total, err
		}
		// Preserve the existing API date representation while job IDs and stored
		// timestamps use Unix time in version 3.
		item.Date = time.Unix(startedAt, 0).Format("20060102150405")
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func parseResultScope(scopeText string) (resultScope, error) {
	scopeText = strings.TrimSpace(scopeText)
	if scopeText == "" || scopeText == "all" {
		return resultScope{}, errors.New("scope must be a numeric range like 1-20")
	}
	startText, endText, ok := strings.Cut(scopeText, "-")
	if !ok {
		return resultScope{}, errors.New("scope must be a numeric range like 1-20")
	}
	start, err := strconv.Atoi(strings.TrimSpace(startText))
	if err != nil || start < 1 {
		return resultScope{}, errors.New("scope start must be a positive number")
	}
	end, err := strconv.Atoi(strings.TrimSpace(endText))
	if err != nil || end < start {
		return resultScope{}, errors.New("scope end must be greater than or equal to start")
	}
	if end-start+1 > maxResultLookupItems {
		return resultScope{}, errors.New("scope cannot include more than 500 results")
	}
	return resultScope{Start: start, End: end}, nil
}

func applyResultScope(files []resultJobFile, scope resultScope) []resultJobFile {
	if scope.All {
		return files
	}
	start := scope.Start - 1
	if start >= len(files) {
		return nil
	}
	end := scope.End
	if end > len(files) {
		end = len(files)
	}
	return files[start:end]
}

func (s *server) readJobResult(jobID string) (any, error) {
	path := filepath.Join(s.cfg.JobsDir, jobID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	result, ok := decoded["result"]
	if !ok {
		return nil, nil
	}
	if result == nil {
		return nil, nil
	}
	text, ok := result.(string)
	if !ok {
		return nil, nil
	}
	switch text {
	case "clean", "found", "error":
		return text, nil
	default:
		return nil, nil
	}
}

func (s *server) handleDetectionResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	var req detectionLookupRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	jobID := strings.TrimSpace(req.JobID)
	if !resultJobIDPattern.MatchString(jobID) {
		writeError(w, http.StatusBadRequest, errors.New("invalid job_id"))
		return
	}

	who, _ := actorFromRequest(r)
	result, err := s.readDetectionResult(jobID, who.ID)
	if err != nil {
		s.error("results", "detection result lookup failed", "job_id", jobID, "user", who.Username, "error", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.debug("results", "detection result lookup completed", "job_id", jobID, "user", who.Username)
	if result == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) readDetectionResult(jobID string, userIDs ...string) (*detectionLookupResponse, error) {
	var jsonFile string
	if len(userIDs) > 0 {
		userID := userIDs[0]
		if err := s.historyDB.QueryRow("SELECT json_file FROM history_jobs WHERE job_id=? AND user_id=?", jobID, userID).Scan(&jsonFile); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
	} else if err := s.historyDB.QueryRow("SELECT json_file FROM history_jobs WHERE job_id=?", jobID).Scan(&jsonFile); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if filepath.Dir(jsonFile) != filepath.Clean(s.cfg.JobsDir) {
		return nil, errors.New("indexed job path is outside jobs directory")
	}
	detections, err := s.readIndexedDetections(jobID)
	if err != nil {
		return nil, err
	}
	logText, err := s.readJobLog(jobID)
	if err != nil {
		return nil, err
	}

	path := filepath.Join(s.cfg.LogDir, "clamav_detection_"+jobID+".log")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &detectionLookupResponse{
				JobID:      jobID,
				Detections: detections,
				Log:        logText,
			}, nil
		}
		return nil, err
	}
	original := string(data)
	return &detectionLookupResponse{
		JobID:      jobID,
		Detections: detections,
		Original:   original,
		Log:        logText,
	}, nil
}

func (s *server) readIndexedDetections(jobID string) ([]detectionItem, error) {
	rows, err := s.historyDB.Query(`
SELECT source_file,detection_reason
FROM history_detections
WHERE job_id=?
ORDER BY sequence`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	detections := make([]detectionItem, 0)
	for rows.Next() {
		var detection detectionItem
		if err := rows.Scan(&detection.SourceFile, &detection.DetectionReason); err != nil {
			return nil, err
		}
		detections = append(detections, detection)
	}
	return detections, rows.Err()
}

func (s *server) readJobLog(jobID string) (string, error) {
	path := filepath.Join(s.cfg.LogDir, jobID+".log")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}
