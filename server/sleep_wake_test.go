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
		data := make([]byte, len("zSHUTDOWN\x00"))
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
	if got := <-command; got != "zSHUTDOWN\x00" {
		t.Fatalf("unexpected command: %q", got)
	}
}

func TestSendClamdShutdownRejectsResponse(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	go func() {
		defer server.Close()
		buffer := make([]byte, len("zSHUTDOWN\x00"))
		_, _ = io.ReadFull(server, buffer)
		_, _ = io.WriteString(server, "unexpected\x00")
	}()

	err := sendClamdShutdownCommand(context.Background(), client, time.Second)
	if err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("expected response rejection, got %v", err)
	}
}

func TestQueryClamdVersionCommand(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	command := make(chan string, 1)
	go func() {
		defer server.Close()
		data := make([]byte, len("zVERSION\x00"))
		_, readErr := io.ReadFull(server, data)
		if readErr != nil {
			command <- "read error: " + readErr.Error()
			return
		}
		command <- string(data)
		_, _ = io.WriteString(server, "ClamAV 1.5.4/28098/Thu Aug 14 14:24:22 2026\x00")
	}()

	version, err := queryClamdVersionCommand(context.Background(), client, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if version.clamdVersion != "1.5.4" || version.databaseVersion != "28098" || version.databaseDate != "Thu Aug 14 14:24:22 2026" {
		t.Fatalf("unexpected clamd VERSION result: %#v", version)
	}
	if got := <-command; got != "zVERSION\x00" {
		t.Fatalf("unexpected command: %q", got)
	}
}

func TestParseClamdVersionResponseRejectsMalformedValues(t *testing.T) {
	for _, response := range []string{
		"",
		"ClamAV 1.5.4",
		"ClamAV 1.5.4//Thu Aug 14 14:24:22 2026",
		"1.5.4/28098/Thu Aug 14 14:24:22 2026",
	} {
		if version, err := parseClamdVersionResponse(response); err == nil {
			t.Errorf("expected malformed response to fail: response=%q version=%#v", response, version)
		}
	}
}

func TestSleepClamAVRejectsActiveScan(t *testing.T) {
	tmp := t.TempDir()
	scanLock := filepath.Join(tmp, "scan.lock")
	if err := os.Mkdir(scanLock, 0o755); err != nil {
		t.Fatal(err)
	}
	withFakeClamdscan(t, true)

	s := &server{activeBatchID: "active", cfg: config{
		ClamdSocket:   filepath.Join(tmp, "missing.sock"),
		ClamdConf:     filepath.Join(tmp, "clamd.conf"),
		CommandTimout: time.Second,
	}}
	_, _, statusCode, err := s.sleepClamAV(context.Background())
	if statusCode != http.StatusConflict || err == nil {
		t.Fatalf("expected active scan conflict, code=%d err=%v", statusCode, err)
	}
}

func TestWakeClamAVDelegatesStaleLockCleanupToStartupScript(t *testing.T) {
	tmp := t.TempDir()
	sleepLock := filepath.Join(tmp, "sleep.lock")
	if err := os.Mkdir(sleepLock, 0o755); err != nil {
		t.Fatal(err)
	}
	withFakeClamdscan(t, true)
	startupScript := filepath.Join(tmp, "startup.sh")
	if err := os.WriteFile(startupScript, []byte("#!/bin/sh\nrmdir \"$SLEEP_LOCK_DIR\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLEEP_LOCK_DIR", sleepLock)

	s := &server{cfg: config{
		StartupScript: startupScript,
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

func TestClamAVSleepTimerExpires(t *testing.T) {
	tmp := t.TempDir()
	shutdownReceived := make(chan struct{})
	app, err := newAppConfigStore(filepath.Join(tmp, "clamavweb.conf"))
	if err != nil {
		t.Fatal(err)
	}
	// The test-only duration unit turns the configured value into milliseconds;
	// production always uses seconds and retains the 600-second validation.
	app.mu.Lock()
	app.cfg.ClamAVSleepTimer = 1
	app.mu.Unlock()
	s := &server{
		cfg:              config{},
		appConfig:        app,
		clamavSleepTimer: newClamAVSleepTimerState(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.runClamAVSleepTimerWithAction(ctx, time.Millisecond, func(context.Context) (string, string, int, error) {
		s.mu.Lock()
		s.engineSleeping = true
		s.mu.Unlock()
		close(shutdownReceived)
		return "sleeping", "ClamAV entered sleep mode.", http.StatusOK, nil
	})

	select {
	case <-shutdownReceived:
	case <-time.After(time.Second):
		t.Fatal("sleep timer did not send ClamAV shutdown")
	}
	if !s.isSleeping() {
		t.Fatal("expected in-memory sleep state")
	}
}

func TestDisabledClamAVSleepTimerDoesNotStart(t *testing.T) {
	tmp := t.TempDir()
	app, err := newAppConfigStore(filepath.Join(tmp, "clamavweb.conf"))
	if err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.cfg.ClamAVSleepTimer = 0
	app.mu.Unlock()
	s := &server{
		cfg:              config{},
		appConfig:        app,
		clamavSleepTimer: newClamAVSleepTimerState(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	go s.runClamAVSleepTimerWithUnit(ctx, time.Millisecond)
	time.Sleep(25 * time.Millisecond)
	cancel()
	if s.isSleeping() {
		t.Fatal("disabled timer changed sleep state")
	}
}

func TestClamAVSleepTimerResetDiscardsOldDeadline(t *testing.T) {
	tmp := t.TempDir()
	app, err := newAppConfigStore(filepath.Join(tmp, "clamavweb.conf"))
	if err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.cfg.ClamAVSleepTimer = 1
	app.mu.Unlock()
	s := &server{
		cfg:              config{},
		appConfig:        app,
		clamavSleepTimer: newClamAVSleepTimerState(),
	}
	fired := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.runClamAVSleepTimerWithAction(ctx, 200*time.Millisecond, func(context.Context) (string, string, int, error) {
		close(fired)
		return "sleeping", "", http.StatusOK, nil
	})

	time.Sleep(50 * time.Millisecond)
	s.notifyClamAVSleepTimerChanged("test_reset")
	select {
	case <-fired:
		t.Fatal("retired sleep timer deadline fired after reset")
	case <-time.After(120 * time.Millisecond):
	}
	select {
	case <-fired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("replacement sleep timer did not fire")
	}
}

func TestClamAVSleepTimerDefersForQueuedManualScan(t *testing.T) {
	tmp := t.TempDir()
	app, err := newAppConfigStore(filepath.Join(tmp, "clamavweb.conf"))
	if err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.cfg.ClamAVSleepTimer = 1
	app.mu.Unlock()
	s := &server{
		cfg:              config{},
		appConfig:        app,
		clamavSleepTimer: newClamAVSleepTimerState(),
		queuedBatchIDs:   []string{"web-pending"},
	}
	fired := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.runClamAVSleepTimerWithAction(ctx, 20*time.Millisecond, func(context.Context) (string, string, int, error) {
		fired <- struct{}{}
		return "sleeping", "", http.StatusOK, nil
	})

	select {
	case <-fired:
		t.Fatal("sleep timer fired while a manual scan was queued")
	case <-time.After(70 * time.Millisecond):
	}
	s.mu.Lock()
	s.queuedBatchIDs = nil
	s.mu.Unlock()
	s.notifyClamAVSleepTimerChanged("queue_cleared")
	select {
	case <-fired:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("sleep timer did not resume after the manual queue cleared")
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
