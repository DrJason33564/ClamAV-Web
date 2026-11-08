package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"clamav-scanner/internal/applog"
)

var version3JobIDPattern = regexp.MustCompile(`^(manual|cron)-[0-9]{10,19}(?:-[0-9]{3})?$`)

type indexedJobDocument struct {
	Version      int             `json:"version"`
	JobID        string          `json:"job_id"`
	Type         string          `json:"type"`
	Status       string          `json:"status"`
	Result       string          `json:"result"`
	Action       string          `json:"action"`
	StartedAt    int64           `json:"started_at"`
	FinishedAt   *int64          `json:"finished_at"`
	UserID       string          `json:"user_id"`
	DetectionLog *string         `json:"detection_log"`
	Detections   []detectionItem `json:"detections,omitempty"`
}

// HistoryStore serializes task documents and all index writers. Runtime engine
// state lives in the queue manager, never in these historical documents.
type HistoryStore struct {
	db      *sql.DB
	jobsDir string
	logDir  string
	mu      sync.Mutex
	logger  *applog.Logger
}

// Keep the internal name used by existing history readers and migrations.
type historyIndexer = HistoryStore

type historyRefreshOptions struct {
	force            bool
	reconcileDeleted bool
}

func (h *historyIndexer) debug(message string, args ...any) {
	if h.logger != nil {
		h.logger.Debug(message, append([]any{"module", "history"}, args...)...)
	}
}

func (h *historyIndexer) warn(message string, args ...any) {
	if h.logger != nil {
		h.logger.Warn(message, append([]any{"module", "history"}, args...)...)
	}
}

func (h *historyIndexer) error(message string, args ...any) {
	if h.logger != nil {
		h.logger.Error(message, append([]any{"module", "history"}, args...)...)
	}
}

func (h *historyIndexer) refresh(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.refreshLocked(ctx, historyRefreshOptions{reconcileDeleted: true})
}

// upgradeSchema backfills schema 2 without applying the normal stale-file
// deletion pass. The caller skips its ordinary startup refresh when upgraded
// is true so this startup remains non-destructive.
func (h *historyIndexer) upgradeSchema(ctx context.Context) (upgraded bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	version, err := historyDatabaseSchemaVersion(ctx, h.db)
	if err != nil {
		return false, err
	}
	switch version {
	case 3:
		return false, nil
	case 2:
		if _, err := h.db.ExecContext(ctx, historyDetectionsSchema); err != nil {
			return false, fmt.Errorf("create history detections table: %w", err)
		}
		if err := h.refreshLocked(ctx, historyRefreshOptions{force: true}); err != nil {
			return false, err
		}
		if _, err := h.db.ExecContext(ctx, "INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(3, unixepoch())"); err != nil {
			return false, fmt.Errorf("record history schema 3: %w", err)
		}
		return true, nil
	default:
		return false, fmt.Errorf("unsupported history database schema version %d", version)
	}
}

func (h *historyIndexer) refreshLocked(ctx context.Context, options historyRefreshOptions) error {
	started := time.Now()
	indexedMtimes := make(map[string]int64)
	if !options.force || options.reconcileDeleted {
		var err error
		indexedMtimes, err = h.loadIndexedMtimesLocked(ctx)
		if err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(h.jobsDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(h.jobsDir, entry.Name())
		oldMtime, indexed := indexedMtimes[path]
		delete(indexedMtimes, path)
		info, err := entry.Info()
		if err != nil {
			h.warn("history job stat failed", "path", path, "error", err)
			continue
		}
		if !options.force && indexed && oldMtime == info.ModTime().UnixNano() {
			continue
		}
		if err := h.indexCurrentFileLocked(ctx, path, info); err != nil {
			h.warn("history job skipped", "path", path, "error", err)
		}
	}
	if options.reconcileDeleted {
		for path := range indexedMtimes {
			if _, err := h.db.ExecContext(ctx, "DELETE FROM history_jobs WHERE json_file=?", path); err != nil {
				return err
			}
		}
	}
	h.debug("history index refresh completed", "duration_ms", time.Since(started).Milliseconds())
	return nil
}

func (h *historyIndexer) loadIndexedMtimesLocked(ctx context.Context) (map[string]int64, error) {
	rows, err := h.db.QueryContext(ctx, "SELECT json_file,file_mtime_ns FROM history_jobs")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indexedMtimes := make(map[string]int64)
	for rows.Next() {
		var path string
		var mtimeNS int64
		if err := rows.Scan(&path, &mtimeNS); err != nil {
			return nil, err
		}
		indexedMtimes[path] = mtimeNS
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return indexedMtimes, nil
}

func (h *historyIndexer) refreshFile(ctx context.Context, path string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.refreshFileLocked(ctx, path)
}

func (h *historyIndexer) refreshFileLocked(ctx context.Context, path string) error {
	path = filepath.Clean(path)
	if filepath.Dir(path) != filepath.Clean(h.jobsDir) || !strings.HasSuffix(filepath.Base(path), ".json") {
		return nil
	}

	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		_, err = h.db.ExecContext(ctx, "DELETE FROM history_jobs WHERE json_file=?", path)
		return err
	}
	if err != nil {
		return err
	}
	// Explicit refreshes must not rely on filesystem timestamp precision.
	return h.indexCurrentFileLocked(ctx, path, info)
}

func (h *historyIndexer) indexCurrentFileLocked(ctx context.Context, path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("job state is not a regular file")
	}
	// Preserve the previous valid index when a file is incomplete or malformed.
	return h.indexFile(ctx, path, filepath.Base(path), info.ModTime().UnixNano())
}

func (h *historyIndexer) indexFile(ctx context.Context, path, name string, mtimeNS int64) error {
	job, detections, err := h.readIndexedDocument(path, name)
	if err != nil {
		return err
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := indexDocument(ctx, tx, job, detections, path, mtimeNS); err != nil {
		return err
	}
	return tx.Commit()
}

func (h *historyIndexer) readIndexedDocument(path, name string) (job indexedJobDocument, detections []detectionItem, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return job, nil, err
	}
	if err := json.Unmarshal(data, &job); err != nil {
		return job, nil, err
	}
	if job.Version != 3 && job.Version != 4 {
		return job, nil, fmt.Errorf("unsupported job version %d", job.Version)
	}
	if !version3JobIDPattern.MatchString(job.JobID) || name != job.JobID+".json" {
		return job, nil, errors.New("job id and filename do not match the version 3 format")
	}
	if job.Type != "manual" && job.Type != "cron" || !strings.HasPrefix(job.JobID, job.Type+"-") {
		return job, nil, errors.New("invalid job type")
	}
	if !validUserID(job.UserID) || job.StartedAt <= 0 {
		return job, nil, errors.New("invalid job owner or start timestamp")
	}
	if job.Action != "warn" && job.Action != "move" && job.Action != "remove" {
		return job, nil, errors.New("invalid job action")
	}
	detections, err = h.readJobDetections(job)
	if err != nil {
		return job, nil, err
	}

	return job, detections, nil
}

func indexDocument(ctx context.Context, tx *sql.Tx, job indexedJobDocument, detections []detectionItem, path string, mtimeNS int64) (err error) {
	if _, err = tx.ExecContext(ctx, `
INSERT INTO history_jobs(job_id,job_type,status,result,action,started_at,finished_at,user_id,json_file,file_mtime_ns,indexed_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(job_id) DO UPDATE SET
 job_type=excluded.job_type,status=excluded.status,result=excluded.result,action=excluded.action,
 started_at=excluded.started_at,finished_at=excluded.finished_at,user_id=excluded.user_id,json_file=excluded.json_file,
 file_mtime_ns=excluded.file_mtime_ns,indexed_at=excluded.indexed_at`,
		job.JobID, job.Type, job.Status, job.Result, job.Action, job.StartedAt, job.FinishedAt, job.UserID, path, mtimeNS, time.Now().Unix()); err != nil {
		return err
	}
	// Replacing the child rows in the same transaction prevents stale findings
	// when a job is atomically rewritten with a different result or log.
	if _, err := tx.ExecContext(ctx, "DELETE FROM history_detections WHERE job_id=?", job.JobID); err != nil {
		return err
	}
	for sequence, detection := range detections {
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_detections(job_id,sequence,source_file,detection_reason) VALUES(?,?,?,?)`,
			job.JobID, sequence, detection.SourceFile, detection.DetectionReason); err != nil {
			return err
		}
	}
	return nil
}

func (h *historyIndexer) readJobDetections(job indexedJobDocument) ([]detectionItem, error) {
	if job.Version >= 4 {
		return job.Detections, nil
	}
	if job.DetectionLog == nil {
		return nil, nil
	}
	detectionPath := filepath.Clean(strings.TrimSpace(*job.DetectionLog))
	expectedPath := filepath.Clean(filepath.Join(h.logDir, "clamav_detection_"+job.JobID+".log"))
	if detectionPath == "." || detectionPath != expectedPath {
		return nil, errors.New("invalid detection log path")
	}
	data, err := os.ReadFile(detectionPath)
	if err != nil {
		return nil, fmt.Errorf("read detection log: %w", err)
	}
	return parse_detection_log(string(data)), nil
}

// restartHistoryPeriodicTimer fully retires the previous timer before creating
// its replacement. Draining prevents a stale tick from triggering an immediate
// refresh on Go versions whose timer channels may already contain a value.
func restartHistoryPeriodicTimer(timer *time.Timer, interval time.Duration) *time.Timer {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	return time.NewTimer(interval)
}

func (s *server) notifyHistoryIntervalChanged() {
	if s.historyIntervalChanged == nil {
		return
	}
	// Only the latest persisted value matters, so coalesce rapid API updates.
	select {
	case s.historyIntervalChanged <- struct{}{}:
	default:
	}
}

// Periodic reconciliation remains a fallback for failed immediate indexing and
// externally restored task files. Scans call HistoryStore directly, not fsnotify.
func (s *server) runHistoryIndexer(ctx context.Context) {
	timer := time.NewTimer(time.Duration(s.appConfig.get().HistoryIndexRefreshInterval) * time.Second)
	defer func() { stopAndDrainTimer(timer) }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.historyIntervalChanged:
			timer = restartHistoryPeriodicTimer(timer, time.Duration(s.appConfig.get().HistoryIndexRefreshInterval)*time.Second)
		case <-timer.C:
			if err := s.history.refresh(ctx); err != nil && ctx.Err() == nil {
				s.error("history", "periodic history refresh failed", "error", err)
			}
			timer = restartHistoryPeriodicTimer(timer, time.Duration(s.appConfig.get().HistoryIndexRefreshInterval)*time.Second)
		}
	}
}
