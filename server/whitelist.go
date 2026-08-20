package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type whitelistEntry struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

type whitelistRequest struct {
	Path string `json:"path"`
}

type whitelistResponse struct {
	Status  string           `json:"status,omitempty"`
	Entries []whitelistEntry `json:"entries,omitempty"`
	Message string           `json:"message,omitempty"`
	Error   string           `json:"error,omitempty"`
}

func (s *server) handleWhitelist(w http.ResponseWriter, r *http.Request) {
	who, _ := actorFromRequest(r)
	switch r.Method {
	case http.MethodGet:
		s.configFileMu.Lock()
		entries, err := s.readWhitelistEntries(who.Username)
		s.configFileMu.Unlock()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, whitelistResponse{Status: "success", Entries: entries})
	case http.MethodPost:
		s.updateWhitelist(w, r, true)
	case http.MethodDelete:
		s.updateWhitelist(w, r, false)
	default:
		methodNotAllowed(w)
	}
}

func (s *server) updateWhitelist(w http.ResponseWriter, r *http.Request, add bool) {
	// Keep allow-list mutation and the subsequent clamd reload mutually
	// exclusive with an in-process sleep transition.
	s.clamavPowerMu.Lock()
	defer s.clamavPowerMu.Unlock()
	s.configFileMu.Lock()
	defer s.configFileMu.Unlock()
	who, _ := actorFromRequest(r)

	sleeping, err := directoryLockExists(s.cfg.SleepLockDir)
	if err != nil {
		writeWhitelistStatus(w, http.StatusInternalServerError, "failed", "", fmt.Errorf("check ClamAV sleep lock: %w", err))
		return
	}
	if sleeping {
		s.warn("whitelist", "whitelist update rejected while ClamAV is sleeping", "user", who.Username)
		// Use an explicit object so message is JSON null. whitelistResponse uses
		// an omitempty string and would otherwise omit the field.
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":  "failed",
			"message": nil,
			"error":   "ClamAV is sleeping",
		})
		return
	}

	var req whitelistRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeWhitelistStatus(w, http.StatusBadRequest, "failed", "", err)
		return
	}
	target, err := s.prepareWhitelistPath(req.Path, who)
	if err != nil {
		writeWhitelistStatus(w, http.StatusBadRequest, "failed", "", err)
		return
	}
	if busy, err := s.scanLockActive(); err != nil {
		writeWhitelistStatus(w, http.StatusInternalServerError, "failed", "", err)
		return
	} else if busy {
		s.warn("whitelist", "whitelist update rejected while scan is active", "user", who.Username)
		writeWhitelistStatus(w, http.StatusConflict, "busy", "clamav is running", nil)
		return
	}

	if add {
		err = s.addWhitelistEntry(target, who.Username)
	} else {
		err = s.deleteWhitelistEntry(target, who.Username)
	}
	if err != nil {
		s.warn("whitelist", "whitelist entry update failed", "user", who.Username, "path", target, "add", add, "error", err)
		writeWhitelistStatus(w, http.StatusBadRequest, "failed", "", err)
		return
	}

	message, err := s.runExcludeScript()
	if err != nil {
		s.error("whitelist", "exclude database refresh failed", "user", who.Username, "error", err)
		writeWhitelistStatus(w, http.StatusInternalServerError, "failed", message, err)
		return
	}
	reloadMessage, err := s.reloadClamdDatabase()
	if err != nil {
		s.error("whitelist", "ClamAV database reload failed", "user", who.Username, "error", err)
		writeWhitelistStatus(w, http.StatusInternalServerError, "failed", reloadMessage, err)
		return
	}
	s.info("whitelist", "whitelist entry updated", "user", who.Username, "path", target, "add", add)
	if reloadMessage != "" {
		message = strings.TrimSpace(message + "\n" + reloadMessage)
	}
	entries, err := s.readWhitelistEntries(who.Username)
	if err != nil {
		writeWhitelistStatus(w, http.StatusInternalServerError, "failed", message, err)
		return
	}
	writeJSON(w, http.StatusOK, whitelistResponse{
		Status:  "success",
		Entries: entries,
		Message: message,
	})
}

func (s *server) readWhitelistEntries(usernames ...string) ([]whitelistEntry, error) {
	username := ""
	if len(usernames) > 0 {
		username = usernames[0]
	}
	data, err := os.ReadFile(s.cfg.ExcludeConfig)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	entries := make([]whitelistEntry, 0, len(lines))
	for i, line := range lines {
		target, owner, ok := parseWhitelistOwnedLine(line)
		if !ok || (username != "" && owner != username) {
			continue
		}
		entries = append(entries, whitelistEntry{Path: target, Line: i + 1})
	}
	return entries, nil
}

func parseWhitelistOwnedLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	if strings.HasPrefix(line, "\"") {
		rest := strings.TrimPrefix(line, "\"")
		end := strings.Index(rest, "\"")
		if end < 0 {
			return "", "", false
		}
		ownerFields := strings.Fields(strings.TrimSpace(rest[end+1:]))
		if len(ownerFields) != 1 || !usernamePattern.MatchString(ownerFields[0]) {
			return "", "", false
		}
		return rest[:end], ownerFields[0], rest[:end] != ""
	}
	fields := strings.Fields(line)
	if len(fields) != 2 || !usernamePattern.MatchString(fields[1]) {
		return "", "", false
	}
	return fields[0], fields[1], true
}

func (s *server) prepareWhitelistPath(input string, actors ...actor) (string, error) {
	target := strings.TrimSpace(input)
	if target == "" {
		return "", errors.New("path is required")
	}
	who := actor{}
	if len(actors) > 0 {
		who = actors[0]
	}
	target, err := s.safePathForActor(target, who)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(target); err != nil {
		return "", err
	}
	if strings.Contains(target, "\"") {
		return "", errors.New("path must not contain double quote")
	}
	return target, nil
}

func (s *server) scanLockActive() (bool, error) {
	info, err := os.Stat(s.cfg.ScanLockDir)
	if err == nil {
		return info.IsDir(), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (s *server) addWhitelistEntry(target string, usernames ...string) error {
	username := ""
	if len(usernames) > 0 {
		username = usernames[0]
	}
	lines, err := s.readWhitelistLines()
	if err != nil {
		return err
	}
	for _, line := range lines {
		existing, owner, ok := parseWhitelistOwnedLine(line)
		if ok && existing == target && owner == username {
			return nil
		}
	}
	lines = append(lines, whitelistConfigLine(target, username))
	return s.writeWhitelistLines(lines)
}

func (s *server) deleteWhitelistEntry(target string, usernames ...string) error {
	username := ""
	if len(usernames) > 0 {
		username = usernames[0]
	}
	lines, err := s.readWhitelistLines()
	if err != nil {
		return err
	}
	removed := false
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		existing, owner, ok := parseWhitelistOwnedLine(line)
		if ok && existing == target && owner == username {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return fmt.Errorf("whitelist entry not found: %s", target)
	}
	return s.writeWhitelistLines(kept)
}

func (s *server) readWhitelistLines() ([]string, error) {
	data, err := os.ReadFile(s.cfg.ExcludeConfig)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, nil
}

func (s *server) writeWhitelistLines(lines []string) error {
	dir := filepath.Dir(s.cfg.ExcludeConfig)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".exclude.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.cfg.ExcludeConfig); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func whitelistConfigLine(target, username string) string {
	if strings.ContainsAny(target, " \t") {
		return `"` + target + `" ` + username
	}
	return target + " " + username
}

func (s *server) removeWhitelistForUser(username string) error {
	s.clamavPowerMu.Lock()
	defer s.clamavPowerMu.Unlock()
	s.configFileMu.Lock()
	defer s.configFileMu.Unlock()
	lines, err := s.readWhitelistLines()
	if err != nil {
		return err
	}
	kept := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		_, owner, ok := parseWhitelistOwnedLine(line)
		if ok && owner == username {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	if !changed {
		return nil
	}
	if err := s.writeWhitelistLines(kept); err != nil {
		return err
	}
	if _, err := s.runExcludeScript(); err != nil {
		return err
	}
	_, err = s.reloadClamdDatabase()
	return err
}

func (s *server) runExcludeScript() (string, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.ExcludeScript)
	out, err := cmd.CombinedOutput()
	message := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return message, errors.New("exclude command timed out")
	}
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		return message, errors.New(message)
	}
	if message == "" {
		message = "Exclude allow-list refreshed."
	}
	s.debug("whitelist", "exclude database refresh completed", "duration_ms", time.Since(started).Milliseconds())
	return message, nil
}

func (s *server) reloadClamdDatabase() (string, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "clamdscan", "--config-file="+s.cfg.ClamdConf, "--reload")
	out, err := cmd.CombinedOutput()
	message := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return message, errors.New("clamd reload timed out")
	}
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		return message, errors.New(message)
	}
	if message == "" {
		message = "ClamAV database reloaded."
	}
	s.debug("whitelist", "ClamAV database reload completed", "duration_ms", time.Since(started).Milliseconds())
	return message, nil
}

func writeWhitelistStatus(w http.ResponseWriter, statusCode int, status string, message string, err error) {
	resp := whitelistResponse{
		Status:  status,
		Message: message,
	}
	if err != nil {
		resp.Error = err.Error()
	}
	writeJSON(w, statusCode, resp)
}
