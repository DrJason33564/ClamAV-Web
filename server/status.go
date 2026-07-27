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
			s.enrichStatusSource(source)
		} else {
			source = string(data)
		}
	}

	ping, msg := s.pingClamd(r.Context())
	writeJSON(w, http.StatusOK, statusResponse{
		Source:      source,
		Ping:        ping,
		PingMessage: msg,
		CheckedAt:   time.Now().Format(time.RFC3339),
	})
}

func (s *server) enrichStatusSource(source any) {
	root, ok := source.(map[string]any)
	if !ok {
		return
	}
	scan, ok := root["scan"].(map[string]any)
	if !ok {
		return
	}
	jobID, _ := scan["last_job_id"].(string)
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
