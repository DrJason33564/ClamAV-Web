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
			s.enrichStatusSource(source, who.Username)
		} else {
			source = string(data)
		}
	}

	ping, msg := s.pingClamd(r.Context())
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
	})
}

func (s *server) enrichStatusSource(source any, usernames ...string) {
	username := ""
	if len(usernames) > 0 {
		username = usernames[0]
	}
	root, ok := source.(map[string]any)
	if !ok {
		return
	}
	scan, ok := root["scan"].(map[string]any)
	if !ok {
		return
	}
	if activeID, _ := scan["active_job_id"].(string); activeID != "" && username != "" {
		state, _ := s.jobState(activeID)
		job, _ := state.(map[string]any)
		if owner, _ := job["user"].(string); owner != username {
			scan["active_job_id"] = nil
		}
	}
	jobID, _ := scan["last_job_id"].(string)
	if s.db != nil && username != "" {
		var status, result string
		err := s.db.QueryRow("SELECT job_id,status,result FROM history_jobs WHERE user=? ORDER BY started_at DESC,job_id DESC LIMIT 1", username).Scan(&jobID, &status, &result)
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
	ctx, cancel := context.WithTimeout(ctx, s.cfg.CommandTimout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "clamdscan", "--config-file="+s.cfg.ClamdConf, "--ping=1")
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return "timeout", "clamd ping timed out"
	}
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return "error", msg
	}
	if msg == "" {
		msg = "clamd is ready"
	}
	return "ready", msg
}
