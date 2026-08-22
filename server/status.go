package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type statusResponse struct {
	Source          any    `json:"source"`
	Ping            string `json:"ping"`
	PingMessage     string `json:"ping_message"`
	ClamdVersion    string `json:"clamd_version"`
	DatabaseVersion string `json:"database_version"`
	DatabaseDate    string `json:"database_date"`
	CheckedAt       string `json:"checked_at"`
	FirstRun        string `json:"first_run"`
	IsTimeDock      bool   `json:"is_timedock"`
}

const (
	clamdStatusCacheTTL          = 5 * time.Second
	maxClamdVersionResponseBytes = 4096
)

type clamdStatusCacheEntry struct {
	ping            string
	pingMessage     string
	clamdVersion    string
	databaseVersion string
	databaseDate    string
	checkedAt       time.Time
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

	clamdStatus := s.cachedClamdStatus(r.Context())
	firstRun := "not_completed"
	if s.appConfig != nil && s.appConfig.get().WebFirstRunCompleted == 2 {
		firstRun = "completed"
	}
	writeJSON(w, http.StatusOK, statusResponse{
		Source:          source,
		Ping:            clamdStatus.ping,
		PingMessage:     clamdStatus.pingMessage,
		ClamdVersion:    clamdStatus.clamdVersion,
		DatabaseVersion: clamdStatus.databaseVersion,
		DatabaseDate:    clamdStatus.databaseDate,
		CheckedAt:       clamdStatus.checkedAt.Format(time.RFC3339),
		FirstRun:        firstRun,
		IsTimeDock:      s.cfg.IsTimeDock,
	})
}

// cachedClamdStatus serializes cache misses so a burst of status requests
// performs only one ping and VERSION query. The shared probe is detached from
// the first HTTP client; each operation still applies the command timeout.
func (s *server) cachedClamdStatus(ctx context.Context) clamdStatusCacheEntry {
	s.clamdStatusMu.Lock()
	defer s.clamdStatusMu.Unlock()

	if !s.clamdStatusCache.checkedAt.IsZero() && time.Since(s.clamdStatusCache.checkedAt) < clamdStatusCacheTTL {
		return s.clamdStatusCache
	}

	probeCtx := context.WithoutCancel(ctx)
	status, message := s.pingClamd(probeCtx)
	entry := clamdStatusCacheEntry{
		ping:        status,
		pingMessage: message,
	}
	if status == "ready" {
		started := time.Now()
		version, err := queryClamdVersion(probeCtx, s.cfg.ClamdSocket, s.cfg.CommandTimout)
		if err != nil {
			s.warn("status", "clamd VERSION query failed", "duration_ms", time.Since(started).Milliseconds(), "error", err)
		} else {
			entry.clamdVersion = version.clamdVersion
			entry.databaseVersion = version.databaseVersion
			entry.databaseDate = version.databaseDate
			s.debug("status", "clamd VERSION query succeeded", "duration_ms", time.Since(started).Milliseconds())
		}
	}
	entry.checkedAt = time.Now()
	s.clamdStatusCache = entry
	return entry
}

func (s *server) invalidateClamdStatusCache() {
	s.clamdStatusMu.Lock()
	s.clamdStatusCache = clamdStatusCacheEntry{}
	s.clamdStatusMu.Unlock()
}

type clamdVersionInfo struct {
	clamdVersion    string
	databaseVersion string
	databaseDate    string
}

func queryClamdVersion(ctx context.Context, socketPath string, timeout time.Duration) (clamdVersionInfo, error) {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return clamdVersionInfo{}, err
	}
	defer conn.Close()

	return queryClamdVersionCommand(ctx, conn, timeout)
}

func queryClamdVersionCommand(ctx context.Context, conn net.Conn, timeout time.Duration) (clamdVersionInfo, error) {
	if err := setClamdSocketDeadline(ctx, conn, timeout); err != nil {
		return clamdVersionInfo{}, err
	}
	if _, err := io.WriteString(conn, "VERSION\n"); err != nil {
		return clamdVersionInfo{}, err
	}

	reader := bufio.NewReader(io.LimitReader(conn, maxClamdVersionResponseBytes+1))
	response, err := reader.ReadString('\n')
	if len(response) > maxClamdVersionResponseBytes {
		return clamdVersionInfo{}, fmt.Errorf("clamd VERSION response exceeds %d bytes", maxClamdVersionResponseBytes)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return clamdVersionInfo{}, err
	}
	return parseClamdVersionResponse(response)
}

func parseClamdVersionResponse(response string) (clamdVersionInfo, error) {
	response = strings.TrimSpace(response)
	response = strings.TrimSpace(strings.TrimSuffix(response, "\x00"))
	parts := strings.SplitN(response, "/", 3)
	if len(parts) != 3 {
		return clamdVersionInfo{}, fmt.Errorf("unexpected clamd VERSION response: %q", response)
	}

	const clamdPrefix = "ClamAV "
	clamdVersion := strings.TrimSpace(strings.TrimPrefix(parts[0], clamdPrefix))
	databaseVersion := strings.TrimSpace(parts[1])
	databaseDate := strings.TrimSpace(parts[2])
	if !strings.HasPrefix(parts[0], clamdPrefix) || clamdVersion == "" || databaseVersion == "" || databaseDate == "" {
		return clamdVersionInfo{}, fmt.Errorf("unexpected clamd VERSION response: %q", response)
	}
	return clamdVersionInfo{
		clamdVersion:    clamdVersion,
		databaseVersion: databaseVersion,
		databaseDate:    databaseDate,
	}, nil
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
