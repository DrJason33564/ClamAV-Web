package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type statusResponse struct {
	Source      any    `json:"source"`
	Ping        string `json:"ping"`
	PingMessage string `json:"ping_message"`
	CheckedAt   string `json:"checked_at"`
	FirstRun    string `json:"first_run"`
	IsTimeDock  bool   `json:"is_timedock"`
}

const clamdPingCacheTTL = 5 * time.Second

type clamdPingCacheEntry struct {
	status    string
	message   string
	checkedAt time.Time
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	var source any = map[string]any{"message": "status file is not available yet"}
	if data, err := os.ReadFile(s.cfg.StatusFile); err == nil {
		var decoded any
		if json.Unmarshal(data, &decoded) == nil {
			source = decoded
			who, _ := actorFromRequest(r)
			s.enrichStatusSource(source, who.ID)
		} else {
			source = string(data)
		}
	}

	ping, msg := s.cachedPingClamd(r.Context())
	firstRun := "not_completed"
	if s.appConfig != nil && s.appConfig.get().WebFirstRunCompleted == 2 {
		firstRun = "completed"
	}
	writeJSON(w, http.StatusOK, statusResponse{
		Source:      source,
		Ping:        ping,
		PingMessage: msg,
		CheckedAt:   time.Now().Format(time.RFC3339),
		FirstRun:    firstRun,
		IsTimeDock:  s.cfg.IsTimeDock,
	})
}

// cachedPingClamd serializes cache misses so a burst of status requests starts
// only one clamdscan process. The shared probe is detached from the first HTTP
// client, while pingClamd still applies the configured command timeout.
func (s *server) cachedPingClamd(ctx context.Context) (string, string) {
	s.clamdPingMu.Lock()
	defer s.clamdPingMu.Unlock()

	if !s.clamdPingCache.checkedAt.IsZero() && time.Since(s.clamdPingCache.checkedAt) < clamdPingCacheTTL {
		return s.clamdPingCache.status, s.clamdPingCache.message
	}
	status, message := s.pingClamd(context.WithoutCancel(ctx))
	s.clamdPingCache = clamdPingCacheEntry{
		status:    status,
		message:   message,
		checkedAt: time.Now(),
	}
	return status, message
}

func (s *server) invalidateClamdPingCache() {
	s.clamdPingMu.Lock()
	s.clamdPingCache = clamdPingCacheEntry{}
	s.clamdPingMu.Unlock()
}

func (s *server) enrichStatusSource(source any, userIDs ...string) {
	userID := ""
	if len(userIDs) > 0 {
		userID = userIDs[0]
	}
	root, ok := source.(map[string]any)
	if !ok {
		return
	}
	scan, ok := root["scan"].(map[string]any)
	if !ok {
		return
	}
	if activeID, _ := scan["active_job_id"].(string); activeID != "" && userID != "" {
		state, _ := s.jobState(activeID)
		job, _ := state.(map[string]any)
		if owner, _ := job["user_id"].(string); owner != userID {
			scan["active_job_id"] = nil
		}
	}
	jobID, _ := scan["last_job_id"].(string)
	if s.historyDB != nil && userID != "" {
		var status, result string
		err := s.historyDB.QueryRow("SELECT job_id,status,result FROM history_jobs WHERE user_id=? ORDER BY started_at DESC,job_id DESC LIMIT 1", userID).Scan(&jobID, &status, &result)
		if err != nil {
			scan["last_job_id"] = nil
			scan["last_job_status"] = nil
			scan["last_job_result"] = nil
			return
		}
		scan["last_job_id"] = jobID
		scan["last_job_status"] = status
		scan["last_job_result"] = result
		return
	}
	if jobID == "" {
		return
	}
	scan["last_job_status"] = nil
	scan["last_job_result"] = nil
	state, _ := s.jobState(jobID)
	job, ok := state.(map[string]any)
	if !ok {
		return
	}
	if status, ok := job["status"].(string); ok && (status == "running" || status == "finished" || status == "failed") {
		scan["last_job_status"] = status
	}
	if result, ok := job["result"].(string); ok {
		scan["last_job_result"] = result
	}
}

func (s *server) pingClamd(ctx context.Context) (string, string) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.CommandTimout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "clamdscan", "--config-file="+s.cfg.ClamdConf, "--ping=1")
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		s.warn("status", "clamd ping timed out", "duration_ms", time.Since(started).Milliseconds())
		return "timeout", "clamd ping timed out"
	}
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		s.warn("status", "clamd ping failed", "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return "error", msg
	}
	if msg == "" {
		msg = "clamd is ready"
	}
	s.debug("status", "clamd ping succeeded", "duration_ms", time.Since(started).Milliseconds())
	return "ready", msg
}
