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
)

var version2JobIDPattern = regexp.MustCompile(`^(manual|cron)-[0-9]{10,19}(?:-[0-9]{3})?$`)

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
	entries, err := os.ReadDir(h.jobsDir)
	if errors.Is(err, os.ErrNotExist) {
		entries = nil
	} else if err != nil {
		return err
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(h.jobsDir, entry.Name())
		seen[path] = true
		info, err := entry.Info()
		if err != nil {
			log.Printf("history index: stat %s: %v", path, err)
			continue
		}
		var oldMtime int64
		err = h.db.QueryRowContext(ctx, "SELECT file_mtime_ns FROM history_jobs WHERE json_file=?", path).Scan(&oldMtime)
		if err == nil && oldMtime == info.ModTime().UnixNano() {
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := h.indexFile(ctx, path, entry.Name(), info.ModTime().UnixNano()); err != nil {
			log.Printf("history index: skip %s: %v", path, err)
			_, _ = h.db.ExecContext(ctx, "DELETE FROM history_jobs WHERE json_file=?", path)
		}
	}
	rows, err := h.db.QueryContext(ctx, "SELECT json_file FROM history_jobs")
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return err
		}
		if !seen[path] {
			stale = append(stale, path)
		}
	}
	rows.Close()
	for _, path := range stale {
		if _, err := h.db.ExecContext(ctx, "DELETE FROM history_jobs WHERE json_file=?", path); err != nil {
			return err
		}
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

func (s *server) runHistoryIndexer(ctx context.Context) {
	for {
		interval := time.Duration(s.appConfig.get().HistoryIndexRefreshInterval) * time.Second
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if err := s.history.refresh(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("history index refresh failed: %v", err)
			}
		}
	}
}
