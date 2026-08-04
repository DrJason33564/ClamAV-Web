package main

import (
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

var resultJobIDPattern = version2JobIDPattern

type resultLookup struct {
	ID        string          `json:"lookup_id"`
	Status    string          `json:"status"`
	Results   []resultLogItem `json:"results,omitempty"`
	Total     int             `json:"total"`
	Error     string          `json:"error,omitempty"`
	StartedAt time.Time       `json:"started_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	scope     resultScope
	User      string `json:"-"`
}

type resultLogItem struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Date   string `json:"date"`
	Result any    `json:"result"`
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

type detectionItem struct {
	SourceFile      string `json:"source_file"`
	DetectionReason string `json:"detection_reason"`
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
		User:      who.Username,
	}
	s.resultMu.Lock()
	s.cleanupResultLookupsLocked(time.Now().Add(-15 * time.Minute))
	s.resultLookups[lookup.ID] = lookup
	s.resultMu.Unlock()

	go s.runResultLookup(lookup.ID)
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
	if lookup.User != who.Username {
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

func (s *server) runResultLookup(id string) {
	s.resultMu.RLock()
	lookup, ok := s.resultLookups[id]
	if !ok {
		s.resultMu.RUnlock()
		return
	}
	scope := lookup.scope
	username := lookup.User
	s.resultMu.RUnlock()

	results, total, err := s.readResultLogItems(scope, username)
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
		return
	}
	lookup.Status = "success"
	lookup.Results = results
	lookup.Total = total
}

func (s *server) readResultLogItems(scope resultScope, username string) ([]resultLogItem, int, error) {
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM history_jobs WHERE user=?", username).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := "SELECT job_id,job_type,started_at,result FROM history_jobs WHERE user=? ORDER BY started_at DESC,job_id DESC"
	args := []any{username}
	if !scope.All {
		query += " LIMIT ? OFFSET ?"
		args = append(args, scope.End-scope.Start+1, scope.Start-1)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, total, err
	}
	defer rows.Close()
	items := []resultLogItem{}
	for rows.Next() {
		var item resultLogItem
		var startedAt int64
		if err := rows.Scan(&item.ID, &item.Type, &startedAt, &item.Result); err != nil {
			return nil, total, err
		}
		// Preserve the existing API date representation while job IDs and stored
		// timestamps use Unix time in version 2.
		item.Date = time.Unix(startedAt, 0).Format("20060102150405")
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func parseResultScope(scopeText string) (resultScope, error) {
	scopeText = strings.TrimSpace(scopeText)
	if scopeText == "" || scopeText == "all" {
		return resultScope{All: true}, nil
	}
	startText, endText, ok := strings.Cut(scopeText, "-")
	if !ok {
		return resultScope{}, errors.New("scope must be all or a numeric range like 1-10")
	}
	start, err := strconv.Atoi(strings.TrimSpace(startText))
	if err != nil || start < 1 {
		return resultScope{}, errors.New("scope start must be a positive number")
	}
	end, err := strconv.Atoi(strings.TrimSpace(endText))
	if err != nil || end < start {
		return resultScope{}, errors.New("scope end must be greater than or equal to start")
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

func (s *server) cleanupResultLookupsLocked(before time.Time) {
	for id, lookup := range s.resultLookups {
		if lookup.UpdatedAt.Before(before) {
			delete(s.resultLookups, id)
		}
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
	result, err := s.readDetectionResult(jobID, who.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if result == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) readDetectionResult(jobID string, usernames ...string) (*detectionLookupResponse, error) {
	if len(usernames) > 0 {
		username := usernames[0]
		var jsonFile string
		if err := s.db.QueryRow("SELECT json_file FROM history_jobs WHERE job_id=? AND user=?", jobID, username).Scan(&jsonFile); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		if filepath.Dir(jsonFile) != filepath.Clean(s.cfg.JobsDir) {
			return nil, errors.New("indexed job path is outside jobs directory")
		}
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
				Detections: []detectionItem{},
				Log:        logText,
			}, nil
		}
		return nil, err
	}
	original := string(data)
	detections := parseDetectionLog(original)
	return &detectionLookupResponse{
		JobID:      jobID,
		Detections: detections,
		Original:   original,
		Log:        logText,
	}, nil
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

func parseDetectionLog(content string) []detectionItem {
	detections := make([]detectionItem, 0)
	current := detectionItem{}
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "Source file") {
			if current.SourceFile != "" || current.DetectionReason != "" {
				detections = append(detections, current)
			}
			current = detectionItem{SourceFile: detectionLineValue(line)}
			continue
		}
		if strings.Contains(line, "Detection reason") {
			current.DetectionReason = detectionLineValue(line)
		}
	}
	if current.SourceFile != "" || current.DetectionReason != "" {
		detections = append(detections, current)
	}
	return detections
}

func detectionLineValue(line string) string {
	pos := strings.LastIndex(line, ":")
	if pos < 0 {
		return ""
	}
	return strings.TrimSpace(line[pos+1:])
}
