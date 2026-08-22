package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type quarantineLookup struct {
	ID        string              `json:"lookup_id"`
	Status    string              `json:"status"`
	Subjects  []quarantineSubject `json:"subjects,omitempty"`
	Error     string              `json:"error,omitempty"`
	StartedAt time.Time           `json:"started_at"`
	UpdatedAt time.Time           `json:"updated_at"`
	UserID    string              `json:"-"`
}

type quarantineSubject struct {
	Name       string `json:"name"`
	SourceFile string `json:"source_file"`
}

type quarantineActionResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

var copyQuarantineFile = copyRegularFile

const clamavQuarantineLockName = "clamav-quarantine-lock"

func (s *server) handleQuarantineLookupStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	lookup := &quarantineLookup{
		ID:        "quarantine-" + randomHex(8),
		Status:    "pending",
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	who, _ := actorFromRequest(r)
	lookup.UserID = who.ID
	if !s.startLookup(who.ID, lookup.ID, func() {
		s.quarantineMu.Lock()
		s.quarantineLookups[lookup.ID] = lookup
		s.quarantineMu.Unlock()
	}, func() {
		s.quarantineMu.Lock()
		delete(s.quarantineLookups, lookup.ID)
		s.quarantineMu.Unlock()
	}, func(ctx context.Context) {
		s.runQuarantineLookup(ctx, lookup.ID)
	}) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many pending lookups for this user"))
		return
	}
	s.debug("quarantine", "quarantine lookup started", "lookup_id", lookup.ID, "user", who.Username)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "pending",
		"lookup_id": lookup.ID,
		"message":   "quarantine list is loading; poll /api/quarantine/lookups/" + lookup.ID,
	})
}

func (s *server) handleQuarantineLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/quarantine/lookups/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}

	s.quarantineMu.RLock()
	lookup, ok := s.quarantineLookups[id]
	if !ok {
		s.quarantineMu.RUnlock()
		http.NotFound(w, r)
		return
	}
	who, _ := actorFromRequest(r)
	if lookup.UserID != who.ID {
		s.quarantineMu.RUnlock()
		http.NotFound(w, r)
		return
	}
	snapshot := *lookup
	if lookup.Subjects != nil {
		snapshot.Subjects = append([]quarantineSubject(nil), lookup.Subjects...)
	}
	s.quarantineMu.RUnlock()

	switch snapshot.Status {
	case "pending":
		writeJSON(w, http.StatusAccepted, snapshot)
	case "failed":
		writeJSON(w, http.StatusInternalServerError, snapshot)
	default:
		writeJSON(w, http.StatusOK, snapshot)
	}
}

func (s *server) runQuarantineLookup(ctx context.Context, id string) {
	s.quarantineMu.RLock()
	lookup, ok := s.quarantineLookups[id]
	if !ok {
		s.quarantineMu.RUnlock()
		return
	}
	userID := lookup.UserID
	s.quarantineMu.RUnlock()
	subjects, err := s.readQuarantineSubjects(ctx, userID)
	if errors.Is(err, context.Canceled) {
		return
	}
	s.quarantineMu.Lock()
	defer s.quarantineMu.Unlock()
	lookup, ok = s.quarantineLookups[id]
	if !ok {
		return
	}
	lookup.UpdatedAt = time.Now()
	if err != nil {
		lookup.Status = "failed"
		lookup.Error = err.Error()
		s.error("quarantine", "quarantine lookup failed", "lookup_id", id, "user_id", userID, "error", err)
		return
	}
	lookup.Status = "success"
	lookup.Subjects = subjects
	s.debug("quarantine", "quarantine lookup completed", "lookup_id", id, "user_id", userID, "subjects", len(subjects))
}

func (s *server) readQuarantineSubjects(ctx context.Context, userID string) ([]quarantineSubject, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validUserID(userID) {
		return []quarantineSubject{}, nil
	}
	directory, err := os.Open(s.cfg.QuarantineDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer directory.Close()
	subjects := make([]quarantineSubject, 0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Read in bounded batches so cancellation does not wait for the entire
		// quarantine directory to be materialized in memory first.
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if entry.IsDir() || entry.Name() == clamavQuarantineLockName || strings.HasSuffix(entry.Name(), ".rec") {
				continue
			}
			sourceFile, owner, err := readQuarantineRecord(filepath.Join(s.cfg.QuarantineDir, entry.Name()+".rec"))
			if err != nil {
				// Legacy and malformed records have no safe owner and are intentionally
				// invisible rather than being assigned to an administrator.
				continue
			}
			if owner != userID {
				continue
			}
			subjects = append(subjects, quarantineSubject{
				Name:       entry.Name(),
				SourceFile: sourceFile,
			})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	sort.Slice(subjects, func(i, j int) bool { return subjects[i].Name < subjects[j].Name })
	return subjects, nil
}

func (s *server) handleQuarantineDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	name, err := quarantineNameFromPath(r.URL.Path, "/api/quarantine/delete/")
	if err != nil {
		writeQuarantineAction(w, http.StatusBadRequest, err)
		return
	}
	who, _ := actorFromRequest(r)
	if err := s.deleteQuarantineSubject(name, who.ID); err != nil {
		s.warn("quarantine", "quarantine subject deletion failed", "name", name, "user", who.Username, "error", err)
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeQuarantineAction(w, status, err)
		return
	}
	s.info("quarantine", "quarantine subject deleted", "name", name, "user", who.Username)
	writeQuarantineAction(w, http.StatusOK, nil)
}

func (s *server) handleQuarantineRecover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	name, err := quarantineNameFromPath(r.URL.Path, "/api/quarantine/recover/")
	if err != nil {
		writeQuarantineAction(w, http.StatusBadRequest, err)
		return
	}
	who, _ := actorFromRequest(r)
	if err := s.recoverQuarantineSubject(name, who); err != nil {
		s.warn("quarantine", "quarantine subject recovery failed", "name", name, "user", who.Username, "error", err)
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		} else if errors.Is(err, errTimeDockAccountRequired) || errors.Is(err, errTimeDockPathDenied) {
			status = http.StatusBadRequest
		}
		writeQuarantineAction(w, status, err)
		return
	}
	s.info("quarantine", "quarantine subject recovered", "name", name, "user", who.Username)
	writeQuarantineAction(w, http.StatusOK, nil)
}

func quarantineNameFromPath(path string, prefix string) (string, error) {
	raw := strings.TrimPrefix(path, prefix)
	if raw == "" || strings.Contains(raw, "/") {
		return "", errors.New("filename is required")
	}
	name, err := url.PathUnescape(raw)
	if err != nil {
		return "", err
	}
	if name == "" || name != filepath.Base(name) || strings.Contains(name, "\x00") || strings.HasSuffix(name, ".rec") {
		return "", errors.New("invalid filename")
	}
	return name, nil
}

func (s *server) deleteQuarantineSubject(name string, userID string) error {
	if !validUserID(userID) {
		return os.ErrNotExist
	}
	target := filepath.Join(s.cfg.QuarantineDir, name)
	_, owner, err := readQuarantineRecord(target + ".rec")
	if err != nil || owner != userID {
		return os.ErrNotExist
	}
	if err := os.Remove(target); err != nil {
		return err
	}
	recPath := target + ".rec"
	if err := os.Remove(recPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *server) recoverQuarantineSubject(name string, who actor) error {
	userID := who.ID
	if !validUserID(userID) {
		return os.ErrNotExist
	}
	quarantined := filepath.Join(s.cfg.QuarantineDir, name)
	recPath := quarantined + ".rec"
	sourceFile, owner, err := readQuarantineRecord(recPath)
	if err != nil {
		return err
	}
	if owner != userID {
		return os.ErrNotExist
	}
	if sourceFile == "" {
		return errors.New("quarantine record is empty")
	}
	if err := s.authorizeRestorePath(sourceFile, who); err != nil {
		return err
	}
	if _, err := os.Stat(quarantined); err != nil {
		return err
	}
	if _, err := os.Stat(sourceFile); err == nil {
		return fmt.Errorf("target already exists: %s", sourceFile)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(sourceFile), 0o755); err != nil {
		return err
	}
	if err := restoreQuarantinedFile(quarantined, sourceFile); err != nil {
		return err
	}
	if err := os.Remove(recPath); err != nil {
		return err
	}
	return nil
}

func restoreQuarantinedFile(quarantined string, sourceFile string) error {
	// O_EXCL is the authoritative existence check. A preceding Stat can make a
	// friendlier error but cannot prevent another process from creating the
	// restore target immediately afterward.
	if err := copyQuarantineFile(quarantined, sourceFile); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("target already exists: %s", sourceFile)
		}
		return err
	}
	if err := os.Remove(quarantined); err != nil {
		_ = os.Remove(sourceFile)
		return err
	}
	return nil
}

func copyRegularFile(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("quarantine subject is not a regular file: %s", src)
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

func readQuarantineRecord(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", nil
		}
		return "", "", err
	}
	return parseQuotedRecord(string(data))
}

func parseQuotedRecord(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", errors.New("quarantine record is empty")
	}
	if !strings.HasPrefix(value, "\"") {
		return "", "", errors.New("invalid quarantine record")
	}
	rest := strings.TrimPrefix(value, "\"")
	end := strings.Index(rest, "\"")
	if end < 0 {
		return "", "", errors.New("invalid quarantine record")
	}
	ownerFields := strings.Fields(rest[end+1:])
	if len(ownerFields) != 1 || !validUserID(ownerFields[0]) {
		return "", "", errors.New("invalid quarantine record owner")
	}
	return rest[:end], ownerFields[0], nil
}

func writeQuarantineAction(w http.ResponseWriter, statusCode int, err error) {
	status := "success"
	respStatus := statusCode
	resp := quarantineActionResponse{Status: status}
	if err != nil {
		resp.Status = "failed"
		resp.Error = err.Error()
		if respStatus < http.StatusBadRequest {
			respStatus = http.StatusInternalServerError
		}
	}
	writeJSON(w, respStatus, resp)
}
