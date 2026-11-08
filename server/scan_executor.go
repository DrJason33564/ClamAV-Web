package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clamav-scanner/internal/applog"
	"golang.org/x/sys/unix"
)

// Open each component relative to its parent descriptor. O_NOFOLLOW on the
// leaf alone would still allow a concurrently replaced parent symlink.
func openScanPath(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("scan path must be absolute")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	for i, part := range parts {
		if part == "" {
			continue
		}
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, e := unix.Openat(fd, part, flags, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

type lockedScanFile struct {
	file   *os.File
	path   string
	locked bool
}

func (f *lockedScanFile) unlock() error {
	if !f.locked {
		return nil
	}
	if err := unix.Flock(int(f.file.Fd()), unix.LOCK_UN); err != nil {
		return err
	}
	f.locked = false
	return nil
}
func (f *lockedScanFile) close() { _ = f.unlock(); _ = f.file.Close() }

type ScanExecutor struct {
	server         *server
	job            *scanJob
	client         ClamdClient
	log, detection *applog.RecordLog
	summary        scanSummary
}

func (x *ScanExecutor) issue(path, stage string, err error) {
	x.summary.ScanErrors++
	kind := "filesystem"
	var ce *clamdError
	if errors.As(err, &ce) {
		stage = ce.Stage
		kind = ce.Kind
	}
	x.summary.Errors = append(x.summary.Errors, scanIssue{path, stage, err.Error()})
	x.server.error("scan", "file scan failed", "job_id", x.job.JobID, "user_id", x.job.UserID, "file", path, "command", "FILDES", "stage", stage, "error_kind", kind, "error", err)
	x.log.Write("ERROR", fmt.Sprintf("%s: %s: %v", path, stage, err))
}

func (s *server) executeScan(ctx context.Context, job *scanJob, preflight ...error) scanSummary {
	maxSize, maxFiles := int64(5*1024*1024), 5
	if s.appConfig != nil {
		cfg := s.appConfig.get()
		maxSize, maxFiles = cfg.LogFileMaxSize, cfg.LogFileNum
	}
	log, err := applog.NewRecordLog(job.LogFile, maxSize, maxFiles)
	if err != nil {
		s.error("scan", "open scan log failed", "job_id", job.JobID, "error", err)
		return scanSummary{ScanErrors: 1, Errors: []scanIssue{{job.Target, "log", err.Error()}}}
	}
	defer log.Close()
	detection, err := applog.NewRecordLog(filepath.Join(s.cfg.LogDir, "clamav_detection_"+job.JobID+".log"), maxSize, maxFiles)
	if err != nil {
		log.Write("ERROR", err.Error())
		s.error("scan", "open detection log failed", "job_id", job.JobID, "error", err)
		return scanSummary{ScanErrors: 1, Errors: []scanIssue{{job.Target, "log", err.Error()}}}
	}
	defer detection.Close()
	timeout := s.cfg.ScanTimeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	x := &ScanExecutor{server: s, job: job, client: ClamdClient{s.cfg.ClamdSocket, timeout}, log: log, detection: detection}
	log.Write("INFO", "Scan started: "+job.Target)
	log.Write("INFO", "Job id: "+job.JobID)
	log.Write("INFO", "Job type: "+job.Type)
	log.Write("INFO", "Job owner ID: "+job.UserID)
	log.Write("INFO", "Detection action: "+job.Action)
	if len(preflight) > 0 && preflight[0] != nil {
		x.issue(job.Target, "preflight", preflight[0])
	} else {
		x.walk(ctx, job.Target, 0)
	}
	log.Write("INFO", fmt.Sprintf("Scan completed: scanned=%d errors=%d detections=%d action_succeeded=%d action_failed=%d", x.summary.Scanned, x.summary.ScanErrors, len(x.summary.Detections), x.summary.ActionSucceeded, x.summary.ActionFailed))
	return x.summary
}

func (x *ScanExecutor) prepare(path string) *lockedScanFile {
	f, err := openScanPath(path)
	if err != nil {
		x.issue(path, "open", err)
		return nil
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = errors.New("not a regular file")
		}
		x.issue(path, "open", err)
		return nil
	}
	entry := &lockedScanFile{file: f, path: path}
	if x.job.LockFile {
		if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			x.server.warn("scan", "file lock unavailable; continuing scan", "job_id", x.job.JobID, "file", path, "error", err)
			x.log.Write("WARN", fmt.Sprintf("File lock unavailable; continuing scan: %s: %v", path, err))
		} else {
			entry.locked = true
		}
	}
	return entry
}

func (x *ScanExecutor) scan(ctx context.Context, entry *lockedScanFile) {
	defer entry.close()
	started := time.Now()
	signature, err := x.client.ScanFile(ctx, entry.file, func() {
		// SCM_RIGHTS duplicates the open description in clamd. Explicit unlock
		// releases the advisory lock at submission, even while clamd holds its fd.
		if err := entry.unlock(); err != nil {
			x.server.warn("scan", "release file lock failed", "job_id", x.job.JobID, "file", entry.path, "error", err)
			x.log.Write("WARN", fmt.Sprintf("Release file lock failed: %s: %v", entry.path, err))
		}
	})
	if err != nil {
		x.issue(entry.path, "scan", err)
	} else {
		x.summary.Scanned++
	}
	x.server.debug("scan", "file scan completed", "job_id", x.job.JobID, "file", entry.path, "duration_ms", time.Since(started).Milliseconds(), "detected", signature != "")
	if signature == "" {
		return
	}
	x.summary.Detections = append(x.summary.Detections, detectionItem{entry.path, signature})
	x.detection.Write("DETECTION", "Source file      : "+entry.path)
	x.detection.Write("DETECTION", "Detection reason : "+signature)
	x.log.Write("ALERT", entry.path+": "+signature+" FOUND")
	if x.job.Action == "warn" {
		return
	}
	err = x.server.applyScanAction(entry.file, entry.path, x.job.Action, x.job.UserID)
	if err != nil {
		x.summary.ActionFailed++
		x.summary.Errors = append(x.summary.Errors, scanIssue{entry.path, "action", err.Error()})
		x.server.error("scan", "detected file action failed", "job_id", x.job.JobID, "file", entry.path, "action", x.job.Action, "error", err)
		x.log.Write("ERROR", fmt.Sprintf("%s action failed: %s: %v", x.job.Action, entry.path, err))
	} else {
		x.summary.ActionSucceeded++
		x.log.Write("ALERT", x.job.Action+" action applied: "+entry.path)
	}
}

func (x *ScanExecutor) walk(ctx context.Context, path string, depth int) {
	if err := ctx.Err(); err != nil {
		x.issue(path, "canceled", err)
		return
	}
	f, err := openScanPath(path)
	if err != nil {
		x.issue(path, "open", err)
		return
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		x.issue(path, "stat", err)
		return
	}
	if !info.IsDir() {
		f.Close()
		if entry := x.prepare(path); entry != nil {
			x.scan(ctx, entry)
		}
		return
	}
	defer f.Close()
	if depth > 20 {
		x.issue(path, "traverse", errors.New("maximum directory recursion exceeded"))
		return
	}
	var dirs []string
	for {
		if err := ctx.Err(); err != nil {
			x.issue(path, "canceled", err)
			return
		}
		entries, readErr := f.ReadDir(128)
		files := make([]*lockedScanFile, 0, len(entries))
		for _, entry := range entries {
			child := filepath.Join(path, entry.Name())
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() {
				dirs = append(dirs, child)
				continue
			}
			if entry.Type().IsRegular() {
				if file := x.prepare(child); file != nil {
					files = append(files, file)
				}
			}
		}
		// Bound descriptors for large directories. Each batch locks sibling
		// files before submission, and holds no locks while descending.
		for _, file := range files {
			if ctx.Err() != nil {
				file.close()
				x.issue(file.path, "canceled", ctx.Err())
			} else {
				x.scan(ctx, file)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			x.issue(path, "traverse", readErr)
			break
		}
	}
	for _, dir := range dirs {
		x.walk(ctx, dir, depth+1)
		if ctx.Err() != nil {
			return
		}
	}
}
