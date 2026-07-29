package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSendClamdShutdownAcceptsEmptyReply(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	command := make(chan string, 1)
	go func() {
		defer server.Close()
		data := make([]byte, len("SHUTDOWN\n"))
		_, readErr := io.ReadFull(server, data)
		if readErr != nil {
			command <- "read error: " + readErr.Error()
			return
		}
		command <- string(data)
	}()

	if err := sendClamdShutdownCommand(context.Background(), client, time.Second); err != nil {
		t.Fatalf("expected empty reply to succeed: %v", err)
	}
	if got := <-command; got != "SHUTDOWN\n" {
		t.Fatalf("unexpected command: %q", got)
	}
}

func TestSendClamdShutdownRejectsResponse(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	go func() {
		defer server.Close()
		buffer := make([]byte, len("SHUTDOWN\n"))
		_, _ = io.ReadFull(server, buffer)
		_, _ = io.WriteString(server, "unexpected\n")
	}()

	err := sendClamdShutdownCommand(context.Background(), client, time.Second)
	if err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("expected response rejection, got %v", err)
	}
}

func TestDirectoryLockLifecycle(t *testing.T) {
	tmp := t.TempDir()
	sleepLock := filepath.Join(tmp, "sleep.lock")
	if err := createDirectoryLock(sleepLock); err != nil {
		t.Fatal(err)
	}
	if err := createDirectoryLock(sleepLock); err != nil {
		t.Fatalf("expected idempotent lock creation: %v", err)
	}
	if info, err := os.Stat(sleepLock); err != nil || !info.IsDir() {
		t.Fatalf("expected sleep lock directory, info=%v err=%v", info, err)
	}
	if err := removeDirectoryLock(sleepLock); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sleepLock); !os.IsNotExist(err) {
		t.Fatalf("expected sleep lock removal, err=%v", err)
	}
}

func TestSleepClamAVRejectsActiveScan(t *testing.T) {
	tmp := t.TempDir()
	scanLock := filepath.Join(tmp, "scan.lock")
	if err := os.Mkdir(scanLock, 0o755); err != nil {
		t.Fatal(err)
	}
	withFakeClamdscan(t, true)

	s := &server{cfg: config{
		SleepLockDir:  filepath.Join(tmp, "sleep.lock"),
		ScanLockDir:   scanLock,
		ClamdSocket:   filepath.Join(tmp, "missing.sock"),
		ClamdConf:     filepath.Join(tmp, "clamd.conf"),
		CommandTimout: time.Second,
	}}
	_, _, statusCode, err := s.sleepClamAV(context.Background())
	if statusCode != http.StatusConflict || err == nil {
		t.Fatalf("expected active scan conflict, code=%d err=%v", statusCode, err)
	}
}

func TestWakeClamAVRemovesStaleLockWhenAlreadyReady(t *testing.T) {
	tmp := t.TempDir()
	sleepLock := filepath.Join(tmp, "sleep.lock")
	if err := os.Mkdir(sleepLock, 0o755); err != nil {
		t.Fatal(err)
	}
	withFakeClamdscan(t, true)

	s := &server{cfg: config{
		SleepLockDir:  sleepLock,
		ClamdConf:     filepath.Join(tmp, "clamd.conf"),
		CommandTimout: time.Second,
		WakeTimeout:   time.Second,
	}}
	status, _, statusCode, err := s.wakeClamAV(context.Background())
	if err != nil || statusCode != http.StatusOK || status != "awake" {
		t.Fatalf("unexpected wake result: status=%q code=%d err=%v", status, statusCode, err)
	}
	if _, err := os.Stat(sleepLock); !os.IsNotExist(err) {
		t.Fatalf("expected stale sleep lock removal, err=%v", err)
	}
}

func withFakeClamdscan(t *testing.T, ready bool) {
	t.Helper()
	binDir := t.TempDir()
	exitCommand := "exit 1"
	if ready {
		exitCommand = "exit 0"
	}
	path := filepath.Join(binDir, "clamdscan")
	content := "#!/bin/sh\n" + exitCommand + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
