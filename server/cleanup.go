package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	errUserBusy  = errors.New("user has an active scan")
	errLastAdmin = errors.New("the last active administrator cannot be deleted")
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
	who, _ := actorFromRequest(r)
	deleted, err := s.cleanUserResults(r.Context(), who.ID)
	if err != nil {
		s.error("cleanup", "result cleanup failed", "user", who.Username, "deleted", deleted, "error", err)
		writeCleanResponse(w, http.StatusInternalServerError, "failed", deleted, err)
		return
	}
	s.info("cleanup", "result cleanup completed", "user", who.Username, "deleted", deleted)
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
	who, _ := actorFromRequest(r)
	deleted, err := s.cleanUserQuarantine(who.ID)
	if err != nil {
		s.error("cleanup", "quarantine cleanup failed", "user", who.Username, "deleted", deleted, "error", err)
		writeCleanResponse(w, http.StatusInternalServerError, "failed", deleted, err)
		return
	}
	s.info("cleanup", "quarantine cleanup completed", "user", who.Username, "deleted", deleted)
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

func (s *server) cleanUserResults(ctx context.Context, userID string) (int, error) {
	if s.history != nil {
		_ = s.history.refresh(ctx)
	}
	rows, err := s.historyDB.QueryContext(ctx, "SELECT job_id,json_file FROM history_jobs WHERE user_id=?", userID)
	if err != nil {
		return 0, err
	}
	type ownedJob struct{ id, jsonFile string }
	var jobs []ownedJob
	for rows.Next() {
		var job ownedJob
		if err := rows.Scan(&job.id, &job.jsonFile); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, job)
	}
	rows.Close()
	deleted := 0
	for _, job := range jobs {
		for _, path := range []string{job.jsonFile, filepath.Join(s.cfg.LogDir, job.id+".log"), filepath.Join(s.cfg.LogDir, "clamav_detection_"+job.id+".log")} {
			if err := os.Remove(path); err == nil {
				deleted++
			} else if !errors.Is(err, os.ErrNotExist) {
				return deleted, err
			}
		}
	}
	if _, err := s.historyDB.ExecContext(ctx, "DELETE FROM history_jobs WHERE user_id=?", userID); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (s *server) cleanUserQuarantine(userID string) (int, error) {
	entries, err := os.ReadDir(s.cfg.QuarantineDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".rec") || entry.Name() == clamavQuarantineLockName {
			continue
		}
		target := filepath.Join(s.cfg.QuarantineDir, entry.Name())
		_, owner, err := readQuarantineRecord(target + ".rec")
		if err != nil || owner != userID {
			continue
		}
		if err := s.deleteQuarantineSubject(entry.Name(), userID); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func (s *server) cleanDeleteUser(ctx context.Context, username string) error {
	s.debug("users", "user deletion started", "user", username)
	s.accountDeleteMu.Lock()
	defer s.accountDeleteMu.Unlock()
	var id string
	var role, status string
	if err := s.userDB.QueryRowContext(ctx, "SELECT id,role,status FROM users WHERE username=?", username).Scan(&id, &role, &status); err != nil {
		return err
	}
	if role == "admin" && status == "active" {
		if last, err := s.isLastActiveAdmin(ctx, username); err != nil {
			return err
		} else if last {
			return errLastAdmin
		}
	}
	if s.history != nil {
		_ = s.history.refresh(ctx)
		var activeJobs int
		if err := s.historyDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM history_jobs WHERE user_id=? AND status IN ('waiting','running')", id).Scan(&activeJobs); err != nil {
			return err
		}
		if activeJobs > 0 {
			return errUserBusy
		}
	}
	s.mu.Lock()
	if active := s.batches[s.activeBatchID]; active != nil && active.UserID == id {
		s.mu.Unlock()
		return errUserBusy
	}
	kept := s.queuedBatchIDs[:0]
	for _, batchID := range s.queuedBatchIDs {
		if batch := s.batches[batchID]; batch != nil && batch.UserID == id {
			delete(s.batches, batchID)
			continue
		}
		kept = append(kept, batchID)
	}
	s.queuedBatchIDs = kept
	s.mu.Unlock()
	now := time.Now().Unix()
	if _, err := s.userDB.ExecContext(ctx, "UPDATE users SET status='deleting',updated_at=? WHERE id=?", now, id); err != nil {
		return err
	}
	_, _ = s.userDB.ExecContext(ctx, "UPDATE sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL", now, id)
	// Cancel workers before deleting their in-memory state so account cleanup
	// cannot race a lookup that is still reading the user's assets.
	s.removeLookupsForUser(id)
	if err := s.removeCronRulesForUser(id); err != nil {
		return err
	}
	if err := s.removeWhitelistForUser(id); err != nil {
		return err
	}
	if _, err := s.cleanUserQuarantine(id); err != nil {
		return err
	}
	if _, err := s.cleanUserResults(ctx, id); err != nil {
		return err
	}
	_, err := s.userDB.ExecContext(ctx, "DELETE FROM users WHERE id=?", id)
	if err == nil {
		s.debug("users", "user deletion resources cleaned", "user", username)
	} else {
		s.error("users", "user deletion failed", "user", username, "error", err)
	}
	return err
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
