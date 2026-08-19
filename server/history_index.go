package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

var version2JobIDPattern = regexp.MustCompile(`^(manual|cron)-[0-9]{10,19}(?:-[0-9]{3})?$`)

var (
	historyIndexEventDebounce            = 150 * time.Millisecond
	historyWatchRetryInterval            = 5 * time.Second
	historyWatchMaxRetryInterval         = 60 * time.Second
	historyWatcherErrorRefreshCooldown   = 60 * time.Second
	historyWatchMaxConsecutiveRetryCount = 10
)

type indexedJobDocument struct {
	Version    int    `json:"version"`
	JobID      string `json:"job_id"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Result     string `json:"result"`
	Action     string `json:"action"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt *int64 `json:"finished_at"`
	User       string `json:"user"`
}

type historyIndexer struct {
	db      *sql.DB
	jobsDir string
	mu      sync.Mutex
}

func (h *historyIndexer) refresh(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	indexedMtimes, err := h.loadIndexedMtimesLocked(ctx)
	if err != nil {
		return err
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
			log.Printf("history index: stat %s: %v", path, err)
			continue
		}
		if indexed && oldMtime == info.ModTime().UnixNano() {
			continue
		}
		if err := h.indexCurrentFileLocked(ctx, path, info); err != nil {
			log.Printf("history index: skip %s: %v", path, err)
		}
	}
	for path := range indexedMtimes {
		if _, err := h.db.ExecContext(ctx, "DELETE FROM history_jobs WHERE json_file=?", path); err != nil {
			return err
		}
	}
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
	return h.indexCurrentFileLocked(ctx, path, info)
}

func (h *historyIndexer) indexCurrentFileLocked(ctx context.Context, path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		_, _ = h.db.ExecContext(ctx, "DELETE FROM history_jobs WHERE json_file=?", path)
		return fmt.Errorf("job state is not a regular file")
	}
	if err := h.indexFile(ctx, path, filepath.Base(path), info.ModTime().UnixNano()); err != nil {
		_, _ = h.db.ExecContext(ctx, "DELETE FROM history_jobs WHERE json_file=?", path)
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
	if job.Version != 2 {
		return fmt.Errorf("unsupported job version %d", job.Version)
	}
	if !version2JobIDPattern.MatchString(job.JobID) || name != job.JobID+".json" {
		return errors.New("job id and filename do not match the version 2 format")
	}
	if job.Type != "manual" && job.Type != "cron" || !strings.HasPrefix(job.JobID, job.Type+"-") {
		return errors.New("invalid job type")
	}
	if !usernamePattern.MatchString(job.User) || job.StartedAt <= 0 {
		return errors.New("invalid job owner or start timestamp")
	}
	if job.Action != "warn" && job.Action != "move" && job.Action != "remove" {
		return errors.New("invalid job action")
	}
	_, err = h.db.ExecContext(ctx, `
INSERT INTO history_jobs(job_id,job_type,status,result,action,started_at,finished_at,user,json_file,file_mtime_ns,indexed_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(job_id) DO UPDATE SET
 job_type=excluded.job_type,status=excluded.status,result=excluded.result,action=excluded.action,
 started_at=excluded.started_at,finished_at=excluded.finished_at,user=excluded.user,json_file=excluded.json_file,
 file_mtime_ns=excluded.file_mtime_ns,indexed_at=excluded.indexed_at`,
		job.JobID, job.Type, job.Status, job.Result, job.Action, job.StartedAt, job.FinishedAt, job.User, path, mtimeNS, time.Now().Unix())
	return err
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

func watchHistoryJobFiles(ctx context.Context, jobsDir string, requests chan<- historyIndexRequest) {
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
			if consecutiveFailures >= historyWatchMaxConsecutiveRetryCount {
				log.Printf("[ERROR] history index watcher disabled after %d consecutive failures; periodic history refresh remains active: %v", consecutiveFailures, err)
				return
			}
			delay := historyWatchRetryDelay(consecutiveFailures)
			log.Printf("history index watcher unavailable (attempt %d/%d); retrying in %s: %v", consecutiveFailures, historyWatchMaxConsecutiveRetryCount, delay, err)
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
				log.Printf("history index watcher error: %v", watchErr)
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
	go watchHistoryJobFiles(ctx, s.history.jobsDir, requests)

	pendingPaths := make(map[string]struct{})
	var debounceTimer *time.Timer
	var debounce <-chan time.Time
	var watcherErrorTimer *time.Timer
	var watcherErrorRefresh <-chan time.Time
	var lastFullRefresh time.Time
	watcherErrorRefreshPending := false
	periodicTimer := time.NewTimer(time.Duration(s.appConfig.get().HistoryIndexRefreshInterval) * time.Second)
	defer periodicTimer.Stop()
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
			log.Printf("history index refresh after watcher error failed: %v", err)
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
					log.Printf("history index event refresh failed for %s: %v", path, err)
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
				log.Printf("history index refresh failed: %v", err)
			} else if err == nil {
				lastFullRefresh = time.Now()
				watcherErrorRefreshPending = false
				stopWatcherErrorTimer()
			}
			periodicTimer.Reset(time.Duration(s.appConfig.get().HistoryIndexRefreshInterval) * time.Second)
		}
	}
}
