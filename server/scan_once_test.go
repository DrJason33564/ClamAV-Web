package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCronScanSleepHandling(t *testing.T) {
	tests := []struct {
		name          string
		wake          bool
		wakeSucceeds  bool
		wantExitCode  int
		wantStatus    string
		wantLogLine   string
		wantSleepLock bool
	}{
		{
			name:          "sleeping without wake",
			wantExitCode:  1,
			wantStatus:    "failed",
			wantLogLine:   "[ERROR] ClamAV is sleeping",
			wantSleepLock: true,
		},
		{
			name:          "wake fails",
			wake:          true,
			wantExitCode:  1,
			wantStatus:    "failed",
			wantLogLine:   "[ERROR] Failed to wake ClamAV",
			wantSleepLock: true,
		},
		{
			name:          "wake succeeds and scan flow continues",
			wake:          true,
			wakeSucceeds:  true,
			wantExitCode:  0,
			wantStatus:    "finished",
			wantLogLine:   "[WARN] Scan target does not exist",
			wantSleepLock: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runCronScanFixture(t, test.wake, test.wakeSucceeds)
			if result.exitCode != test.wantExitCode {
				t.Fatalf("expected exit %d, got %d; output=%s", test.wantExitCode, result.exitCode, result.output)
			}
			if result.job["status"] != test.wantStatus {
				t.Fatalf("expected job status %q, got %#v", test.wantStatus, result.job["status"])
			}
			if result.job["version"] != float64(2) || result.job["user"] != "alice" {
				t.Fatalf("expected version 2 job owned by alice, got %#v", result.job)
			}
			if _, ok := result.job["started_at"].(float64); !ok {
				t.Fatalf("started_at must be a Unix timestamp number, got %#v", result.job["started_at"])
			}
			for _, field := range []string{
				"version", "job_id", "type", "user", "status", "target", "action", "pid",
				"started_at", "finished_at", "exit_code", "result", "log_file",
				"detection_log", "message",
			} {
				if _, ok := result.job[field]; !ok {
					t.Fatalf("complete job JSON is missing %q: %#v", field, result.job)
				}
			}
			for _, line := range []string{
				"[INFO] Scan started:",
				"[INFO] Job id:",
				"[INFO] Job type: cron",
				"[INFO] Detection action: warn only",
				test.wantLogLine,
			} {
				if !strings.Contains(result.log, line) {
					t.Fatalf("job log is missing %q:\n%s", line, result.log)
				}
			}
			_, lockErr := os.Stat(result.sleepLock)
			if test.wantSleepLock && lockErr != nil {
				t.Fatalf("expected sleep lock to remain: %v", lockErr)
			}
			if !test.wantSleepLock && !errors.Is(lockErr, os.ErrNotExist) {
				t.Fatalf("expected sleep lock removal, err=%v", lockErr)
			}
		})
	}
}

func TestManualScanRejectsWakeFlag(t *testing.T) {
	script := repositoryScript(t, "scan_once.sh")
	cmd := exec.Command(script, "--type", "manual", "-u", "alice", "--target", "/missing", "--action", "warn", "--wake")
	cmd.Env = append(os.Environ(), "LOG_SCRIPT="+repositoryScript(t, "log.sh"))
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("expected manual --wake to exit 2, got %v", err)
	}
}

func TestStartupWakeRemovesSleepLockWhenClamdIsReady(t *testing.T) {
	tmp := t.TempDir()
	statusDir := filepath.Join(tmp, "state")
	logDir := filepath.Join(tmp, "log")
	sleepLock := filepath.Join(statusDir, "sleep.lock")
	if err := os.MkdirAll(sleepLock, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	clamdscan := filepath.Join(binDir, "clamdscan")
	if err := os.WriteFile(clamdscan, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(repositoryScript(t, "startup.sh"), "--wake")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STATUS_DIR="+statusDir,
		"SLEEP_LOCK_DIR="+sleepLock,
		"SCAN_LOG_DIR="+logDir,
		"STARTUP_LOG_FILE="+filepath.Join(logDir, "startup.log"),
		"LOG_SCRIPT="+repositoryScript(t, "log.sh"),
		"CONFIG_SCRIPT="+repositoryScript(t, "config.sh"),
		"CLAMAV_INIT=/bin/false",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("startup.sh --wake failed: %v output=%s", err, output)
	}
	if _, err := os.Stat(sleepLock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected startup.sh --wake to remove sleep lock, err=%v", err)
	}
}

type cronScanFixtureResult struct {
	exitCode  int
	output    string
	log       string
	job       map[string]any
	sleepLock string
}

func runCronScanFixture(t *testing.T, wake bool, wakeSucceeds bool) cronScanFixtureResult {
	t.Helper()
	tmp := t.TempDir()
	statusDir := filepath.Join(tmp, "state")
	jobsDir := filepath.Join(statusDir, "jobs")
	sleepLock := filepath.Join(statusDir, "sleep.lock")
	if err := os.MkdirAll(sleepLock, 0o755); err != nil {
		t.Fatal(err)
	}

	startupScript := filepath.Join(tmp, "startup.sh")
	startupBody := "#!/bin/sh\nexit 1\n"
	if wakeSucceeds {
		startupBody = "#!/bin/sh\nrmdir \"$SLEEP_LOCK_DIR\"\n"
	}
	if err := os.WriteFile(startupScript, []byte(startupBody), 0o755); err != nil {
		t.Fatal(err)
	}

	script := repositoryScript(t, "scan_once.sh")
	logScript := repositoryScript(t, "log.sh")
	missingTarget := filepath.Join(tmp, "missing-target")
	args := []string{"--type", "cron", "-u", "alice", "--target", missingTarget, "--action", "warn"}
	if wake {
		args = append(args, "--wake")
	}
	cmd := exec.Command(script, args...)
	cmd.Env = append(os.Environ(),
		"SCAN_LOG_DIR="+filepath.Join(tmp, "log"),
		"STATUS_DIR="+statusDir,
		"JOBS_DIR="+jobsDir,
		"SCAN_LOCK_DIR="+filepath.Join(statusDir, "scan.lock"),
		"SLEEP_LOCK_DIR="+sleepLock,
		"QUARANTINE_DIR="+filepath.Join(tmp, "quarantine"),
		"STARTUP_SCRIPT="+startupScript,
		"LOG_SCRIPT="+logScript,
	)
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatal(err)
		}
		exitCode = exitErr.ExitCode()
	}

	jobID := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(output)), "JOB_ID="))
	if jobID == "" || strings.Contains(jobID, "\n") {
		t.Fatalf("expected one JOB_ID line, got %q", output)
	}
	jobData, err := os.ReadFile(filepath.Join(jobsDir, jobID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var job map[string]any
	if err := json.Unmarshal(jobData, &job); err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(filepath.Join(tmp, "log", jobID+".log"))
	if err != nil {
		t.Fatal(err)
	}
	return cronScanFixtureResult{
		exitCode:  exitCode,
		output:    string(output),
		log:       string(logData),
		job:       job,
		sleepLock: sleepLock,
	}
}

func repositoryScript(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", name))
	if err != nil {
		t.Fatal(err)
	}
	return path
}
