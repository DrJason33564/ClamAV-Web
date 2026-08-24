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

	"github.com/fsnotify/fsnotify"

	"clamav-scanner/internal/applog"
)

var version3JobIDPattern = regexp.MustCompile(`^(manual|cron)-[0-9]{10,19}(?:-[0-9]{3})?$`)

var (
	historyIndexEventDebounce            = 150 * time.Millisecond
	historyWatchRetryInterval            = 5 * time.Second
	historyWatchMaxRetryInterval         = 60 * time.Second
	historyWatcherErrorRefreshCooldown   = 60 * time.Second
	historyWatchMaxConsecutiveRetryCount = 10
)

type indexedJobDocument struct {
	Version      int     `json:"version"`
	JobID        string  `json:"job_id"`
	Type         string  `json:"type"`
	Status       string  `json:"status"`
	Result       string  `json:"result"`
	Action       string  `json:"action"`
	StartedAt    int64   `json:"started_at"`
	FinishedAt   *int64  `json:"finished_at"`
	UserID       string  `json:"user_id"`
	DetectionLog *string `json:"detection_log"`
}

type historyIndexer struct {
	db      *sql.DB
	jobsDir string
	logDir  string
	mu      sync.Mutex
	logger  *applog.Logger
}

type historyRefreshOptions struct {
	force            bool
	reconcileDeleted bool
	removeInvalid    bool
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
	return h.refreshLocked(ctx, historyRefreshOptions{reconcileDeleted: true, removeInvalid: true})
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
	if errors.Is(err, os.ErrNotExist) {
		entries = nil
	} else if err != nil {
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
		if err := h.indexCurrentFileLocked(ctx, path, info, options.removeInvalid); err != nil {
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
	// An fsnotify event is authoritative even when a filesystem exposes coarse
	// mtime resolution and two atomic replacements happen within one tick.
	return h.indexCurrentFileLocked(ctx, path, info, true)
}

func (h *historyIndexer) indexCurrentFileLocked(ctx context.Context, path string, info os.FileInfo, removeInvalid bool) error {
	if !info.Mode().IsRegular() {
		if removeInvalid {
			_, _ = h.db.ExecContext(ctx, "DELETE FROM history_jobs WHERE json_file=?", path)
		}
		return fmt.Errorf("job state is not a regular file")
	}
	if err := h.indexFile(ctx, path, filepath.Base(path), info.ModTime().UnixNano()); err != nil {
		if removeInvalid {
			_, _ = h.db.ExecContext(ctx, "DELETE FROM history_jobs WHERE json_file=?", path)
		}
		return err
	}
	return nil
}

func (h *historyIndexer) indexFile(ctx context.Context, path, name string, mtimeNS int64) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var job indexedJobDocument
	if err := json.Unmarshal(data, &job); err != nil {
		return err
	}
	if job.Version != 3 {
		return fmt.Errorf("unsupported job version %d", job.Version)
	}
	if !version3JobIDPattern.MatchString(job.JobID) || name != job.JobID+".json" {
		return errors.New("job id and filename do not match the version 3 format")
	}
	if job.Type != "manual" && job.Type != "cron" || !strings.HasPrefix(job.JobID, job.Type+"-") {
		return errors.New("invalid job type")
	}
	if !validUserID(job.UserID) || job.StartedAt <= 0 {
		return errors.New("invalid job owner or start timestamp")
	}
	if job.Action != "warn" && job.Action != "move" && job.Action != "remove" {
		return errors.New("invalid job action")
	}
	detections, err := h.readJobDetections(job)
	if err != nil {
		return err
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
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
	return tx.Commit()
}

func (h *historyIndexer) readJobDetections(job indexedJobDocument) ([]detectionItem, error) {
	if job.Result != "found" || job.DetectionLog == nil {
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

type historyIndexRequest struct {
	path string
	full bool
}

func historyWatchRetryDelay(failureCount int) time.Duration {
	if failureCount < 1 {
		failureCount = 1
	}
	delay := historyWatchRetryInterval
	for attempt := 1; attempt < failureCount && delay < historyWatchMaxRetryInterval; attempt++ {
		delay *= 2
		if delay > historyWatchMaxRetryInterval {
			delay = historyWatchMaxRetryInterval
		}
	}
	return delay
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

func watchHistoryJobFiles(ctx context.Context, jobsDir string, requests chan<- historyIndexRequest, logger *applog.Logger) {
	consecutiveFailures := 0
	for {
		watcher, err := fsnotify.NewWatcher()
		if err == nil {
			err = watcher.Add(jobsDir)
		}
		if err != nil {
			if watcher != nil {
				_ = watcher.Close()
			}
			consecutiveFailures++
			if logger != nil {
				logger.Warn("history index watcher initialization failed", "module", "history", "error", err, "attempt", consecutiveFailures, "max_attempts", historyWatchMaxConsecutiveRetryCount, "retry_after", historyWatchRetryDelay(consecutiveFailures))
			}
			if consecutiveFailures >= historyWatchMaxConsecutiveRetryCount {
				if logger != nil {
					logger.Error("history index watcher disabled; periodic refresh remains active", "module", "history", "error", err, "attempt", consecutiveFailures)
				}
				return
			}
			delay := historyWatchRetryDelay(consecutiveFailures)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				continue
			}
		}
		consecutiveFailures = 0
		if logger != nil {
			logger.Info("history index watcher started", "module", "history", "jobs_dir", jobsDir)
		}

		watchClosed := false
		for !watchClosed {
			select {
			case <-ctx.Done():
				_ = watcher.Close()
				return
			case event, ok := <-watcher.Events:
				if !ok {
					watchClosed = true
					continue
				}
				if filepath.Clean(event.Name) == filepath.Clean(jobsDir) &&
					event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
					watchClosed = true
					continue
				}
				if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Remove) == 0 ||
					!strings.HasSuffix(filepath.Base(event.Name), ".json") {
					continue
				}
				select {
				case requests <- historyIndexRequest{path: filepath.Clean(event.Name)}:
				case <-ctx.Done():
					_ = watcher.Close()
					return
				}
			case watchErr, ok := <-watcher.Errors:
				if !ok {
					watchClosed = true
					continue
				}
				if logger != nil {
					// Every watcher error is retained verbatim for operational diagnosis.
					logger.Warn("history index watcher reported an error", "module", "history", "error", watchErr)
				}
				select {
				case requests <- historyIndexRequest{full: true}:
				case <-ctx.Done():
					_ = watcher.Close()
					return
				}
			}
		}
		_ = watcher.Close()
	}
}

func (s *server) runHistoryIndexer(ctx context.Context) {
	requests := make(chan historyIndexRequest, 128)
	go watchHistoryJobFiles(ctx, s.history.jobsDir, requests, s.logger)

	pendingPaths := make(map[string]struct{})
	var debounceTimer *time.Timer
	var debounce <-chan time.Time
	var watcherErrorTimer *time.Timer
	var watcherErrorRefresh <-chan time.Time
	var lastFullRefresh time.Time
	watcherErrorRefreshPending := false
	periodicTimer := time.NewTimer(time.Duration(s.appConfig.get().HistoryIndexRefreshInterval) * time.Second)
	defer func() { periodicTimer.Stop() }()
	defer func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		if watcherErrorTimer != nil {
			watcherErrorTimer.Stop()
		}
	}()

	resetDebounce := func() {
		if debounceTimer == nil {
			debounceTimer = time.NewTimer(historyIndexEventDebounce)
		} else {
			if !debounceTimer.Stop() {
				select {
				case <-debounceTimer.C:
				default:
				}
			}
			debounceTimer.Reset(historyIndexEventDebounce)
		}
		debounce = debounceTimer.C
	}
	stopWatcherErrorTimer := func() {
		if watcherErrorTimer == nil {
			return
		}
		if !watcherErrorTimer.Stop() {
			select {
			case <-watcherErrorTimer.C:
			default:
			}
		}
		watcherErrorTimer = nil
		watcherErrorRefresh = nil
	}
	runWatcherErrorRefresh := func() {
		lastFullRefresh = time.Now()
		watcherErrorRefreshPending = false
		stopWatcherErrorTimer()
		if err := s.history.refresh(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.error("history", "history index refresh after watcher error failed", "error", err)
		}
	}
	scheduleWatcherErrorRefresh := func() {
		now := time.Now()
		if lastFullRefresh.IsZero() || now.Sub(lastFullRefresh) >= historyWatcherErrorRefreshCooldown {
			runWatcherErrorRefresh()
			return
		}
		watcherErrorRefreshPending = true
		if watcherErrorTimer == nil {
			watcherErrorTimer = time.NewTimer(historyWatcherErrorRefreshCooldown - now.Sub(lastFullRefresh))
			watcherErrorRefresh = watcherErrorTimer.C
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.historyIntervalChanged:
			interval := time.Duration(s.appConfig.get().HistoryIndexRefreshInterval) * time.Second
			periodicTimer = restartHistoryPeriodicTimer(periodicTimer, interval)
			s.debug("history", "history index periodic timer restarted", "interval", interval)
		case request := <-requests:
			if request.full {
				scheduleWatcherErrorRefresh()
				continue
			}
			pendingPaths[request.path] = struct{}{}
			resetDebounce()
		case <-debounce:
			paths := pendingPaths
			pendingPaths = make(map[string]struct{})
			debounce = nil
			for path := range paths {
				if err := s.history.refreshFile(ctx, path); err != nil && !errors.Is(err, context.Canceled) {
					s.error("history", "history index event refresh failed", "path", path, "error", err)
				}
			}
		case <-watcherErrorRefresh:
			watcherErrorTimer = nil
			watcherErrorRefresh = nil
			if watcherErrorRefreshPending {
				runWatcherErrorRefresh()
			}
		case <-periodicTimer.C:
			if err := s.history.refresh(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.error("history", "periodic history index refresh failed", "error", err)
			} else if err == nil {
				lastFullRefresh = time.Now()
				watcherErrorRefreshPending = false
				stopWatcherErrorTimer()
			}
			periodicTimer = restartHistoryPeriodicTimer(periodicTimer, time.Duration(s.appConfig.get().HistoryIndexRefreshInterval)*time.Second)
		}
	}
}
