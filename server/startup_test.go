package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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

	cmd := exec.Command("sh", repositoryScript(t, "startup.sh"), "--wake")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STATUS_DIR="+statusDir,
		"SLEEP_LOCK_DIR="+sleepLock,
		"SCAN_LOG_DIR="+logDir,
		"STARTUP_LOG_FILE="+filepath.Join(logDir, "startup.log"),
		"LOG_SCRIPT="+repositoryScript(t, "log.sh"),
		"CLAMAV_INIT=/bin/false",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("startup.sh --wake failed: %v output=%s", err, output)
	}
	if _, err := os.Stat(sleepLock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected startup.sh --wake to remove sleep lock, err=%v", err)
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
