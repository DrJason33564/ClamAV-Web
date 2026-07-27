package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type cleanRequest struct {
	CleanAll string `json:"clean_all"`
}

type cleanResponse struct {
	Status  string `json:"status"`
	Deleted int    `json:"deleted"`
	Error   string `json:"error,omitempty"`
}

func (s *server) handleResultsClean(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !cleanAllRequested(r) {
		writeCleanResponse(w, http.StatusBadRequest, "failed", 0, errors.New("clean_all must be Y"))
		return
	}
	deleted, err := s.cleanAllResults()
	if err != nil {
		writeCleanResponse(w, http.StatusInternalServerError, "failed", deleted, err)
		return
	}
	writeCleanResponse(w, http.StatusOK, "success", deleted, nil)
}

func (s *server) handleQuarantineClean(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !cleanAllRequested(r) {
		writeCleanResponse(w, http.StatusBadRequest, "failed", 0, errors.New("clean_all must be Y"))
		return
	}
	deleted, err := cleanDirectoryChildren(s.cfg.QuarantineDir)
	if err != nil {
		writeCleanResponse(w, http.StatusInternalServerError, "failed", deleted, err)
		return
	}
	writeCleanResponse(w, http.StatusOK, "success", deleted, nil)
}

func cleanAllRequested(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("clean_all")), "Y") {
		return true
	}
	var req cleanRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(req.CleanAll), "Y")
}

func (s *server) cleanAllResults() (int, error) {
	deletedLogs, err := cleanMatchingFiles(s.cfg.LogDir, removableResultLog)
	if err != nil {
		return deletedLogs, err
	}
	deletedJobs, err := cleanMatchingFiles(s.cfg.JobsDir, removableResultJob)
	return deletedLogs + deletedJobs, err
}

func cleanMatchingFiles(dir string, match func(string) bool) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	deleted := 0
	for _, entry := range entries {
		if entry.IsDir() || !match(entry.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func cleanDirectoryChildren(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	deleted := 0
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func removableResultLog(name string) bool {
	if !strings.HasSuffix(name, ".log") {
		return false
	}
	return strings.HasPrefix(name, "manual-") ||
		strings.HasPrefix(name, "cron-") ||
		strings.HasPrefix(name, "clamav_detection")
}

func removableResultJob(name string) bool {
	if !strings.HasSuffix(name, ".json") {
		return false
	}
	return strings.HasPrefix(name, "manual-") || strings.HasPrefix(name, "cron-")
}

func writeCleanResponse(w http.ResponseWriter, statusCode int, status string, deleted int, err error) {
	resp := cleanResponse{
		Status:  status,
		Deleted: deleted,
	}
	if err != nil {
		resp.Error = err.Error()
	}
	writeJSON(w, statusCode, resp)
}
