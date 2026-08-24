package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLookupRuntimeLimitsPendingAcrossTypesAndStopsWorkers(t *testing.T) {
	runtime := newLookupRuntime(context.Background())
	finished := make(chan string, 3)
	work := func(id string) func(context.Context) {
		return func(ctx context.Context) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > lookupExecutionTimeout || time.Until(deadline) < lookupExecutionTimeout-time.Second {
				t.Errorf("unexpected lookup deadline: %v ok=%v", deadline, ok)
			}
			<-ctx.Done()
			finished <- id
		}
	}

	if !runtime.start("result-1", testAliceUserID, work("result-1")) ||
		!runtime.start("statistics-1", testAliceUserID, work("statistics-1")) {
		t.Fatal("expected the first two lookups for one user to start")
	}
	if runtime.start("quarantine-1", testAliceUserID, work("quarantine-1")) {
		t.Fatal("expected a third pending lookup for the same user to be rejected")
	}
	if !runtime.start("quarantine-2", testBobUserID, work("quarantine-2")) {
		t.Fatal("expected another user to have an independent pending allowance")
	}

	runtime.stopAndWait()
	if len(finished) != 3 {
		t.Fatalf("expected shutdown to cancel and wait for three workers, got %d", len(finished))
	}
	if runtime.start("result-after-stop", testAliceUserID, work("result-after-stop")) {
		t.Fatal("expected stopped runtime to reject new work")
	}
}

func TestLookupCleanupAndRetentionApplyAcrossTypes(t *testing.T) {
	now := time.Now()
	s := &server{
		resultLookups: map[string]*resultLookup{
			"expired-result":       {ID: "expired-result", Status: "success", UserID: testAliceUserID, StartedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-lookupRetention - time.Second)},
			"timed-out-publishing": {ID: "timed-out-publishing", Status: "pending", UserID: testAliceUserID, StartedAt: now.Add(-lookupExecutionTimeout - time.Second), UpdatedAt: now.Add(-lookupExecutionTimeout - time.Second)},
		},
		historyStatisticsLookups: map[string]*historyStatisticsLookup{
			"stale-pending": {ID: "stale-pending", Status: "pending", UserID: testAliceUserID, StartedAt: now.Add(-lookupExecutionTimeout - lookupCleanupInterval - time.Second), UpdatedAt: now.Add(-lookupExecutionTimeout - lookupCleanupInterval - time.Second)},
		},
		quarantineLookups: map[string]*quarantineLookup{
			"recent-quarantine": {ID: "recent-quarantine", Status: "success", UserID: testAliceUserID, StartedAt: now, UpdatedAt: now},
		},
		lookupRuntime: newLookupRuntime(context.Background()),
	}
	defer s.lookupRuntime.stopAndWait()

	s.cleanupLookups(now)
	if len(s.resultLookups) != 1 || s.resultLookups["timed-out-publishing"] == nil || len(s.historyStatisticsLookups) != 0 || len(s.quarantineLookups) != 1 {
		t.Fatalf("unexpected lookup cleanup result: results=%d statistics=%d quarantine=%d", len(s.resultLookups), len(s.historyStatisticsLookups), len(s.quarantineLookups))
	}

	for index := 0; index < maxRetainedLookupsPerUser; index++ {
		id := "retained-" + strconv.Itoa(index)
		s.resultLookups[id] = &resultLookup{ID: id, Status: "success", UserID: testBobUserID, StartedAt: now, UpdatedAt: now.Add(time.Duration(index) * time.Second)}
	}
	started := make(chan struct{})
	if !s.startLookup(testBobUserID, "newest", func() {
		s.quarantineMu.Lock()
		s.quarantineLookups["newest"] = &quarantineLookup{ID: "newest", Status: "pending", UserID: testBobUserID, StartedAt: now, UpdatedAt: now}
		s.quarantineMu.Unlock()
	}, func() {
		s.quarantineMu.Lock()
		delete(s.quarantineLookups, "newest")
		s.quarantineMu.Unlock()
	}, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
	}) {
		t.Fatal("expected a new lookup to evict the oldest completed entry")
	}
	<-started
	if _, ok := s.resultLookups["retained-0"]; ok {
		t.Fatal("expected the oldest completed lookup to be evicted")
	}
	if got := len(s.lookupRecordsLocked(testBobUserID)); got != maxRetainedLookupsPerUser {
		t.Fatalf("expected retained lookup count to remain capped at %d, got %d", maxRetainedLookupsPerUser, got)
	}
}

func TestRemoveLookupsForUserCancelsAndWaitsForWorkers(t *testing.T) {
	s := &server{
		resultLookups:            make(map[string]*resultLookup),
		historyStatisticsLookups: make(map[string]*historyStatisticsLookup),
		quarantineLookups:        make(map[string]*quarantineLookup),
		lookupRuntime:            newLookupRuntime(context.Background()),
	}
	defer s.lookupRuntime.stopAndWait()
	started := make(chan struct{})
	finished := make(chan struct{})
	if !s.startLookup(testAliceUserID, "result-cancel", func() {
		s.resultMu.Lock()
		s.resultLookups["result-cancel"] = &resultLookup{ID: "result-cancel", Status: "pending", UserID: testAliceUserID, StartedAt: time.Now(), UpdatedAt: time.Now()}
		s.resultMu.Unlock()
	}, func() {
		s.resultMu.Lock()
		delete(s.resultLookups, "result-cancel")
		s.resultMu.Unlock()
	}, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(finished)
	}) {
		t.Fatal("expected lookup to start")
	}
	<-started

	s.removeLookupsForUser(testAliceUserID)
	select {
	case <-finished:
	default:
		t.Fatal("expected user lookup removal to wait for worker cancellation")
	}
	if len(s.resultLookups) != 0 {
		t.Fatalf("expected user lookup state to be removed, got %#v", s.resultLookups)
	}
}

func TestNewHTTPServerAppliesConnectionLimits(t *testing.T) {
	handler := http.NewServeMux()
	httpServer := newHTTPServer("127.0.0.1:8080", handler)

	if httpServer.Addr != "127.0.0.1:8080" || httpServer.Handler != handler {
		t.Fatalf("unexpected HTTP server routing: addr=%q handler=%v", httpServer.Addr, httpServer.Handler)
	}
	if httpServer.ReadHeaderTimeout != httpReadHeaderTimeout ||
		httpServer.ReadTimeout != httpReadTimeout ||
		httpServer.IdleTimeout != httpIdleTimeout ||
		httpServer.WriteTimeout != httpWriteTimeout ||
		httpServer.MaxHeaderBytes != httpMaxHeaderBytes {
		t.Fatalf("unexpected HTTP connection limits: %#v", httpServer)
	}
}

func TestStartScanRejectsSleepingClamAV(t *testing.T) {
	tmp := t.TempDir()
	sleepLock := filepath.Join(tmp, "sleep.lock")
	if err := os.Mkdir(sleepLock, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &server{
		cfg:     config{SleepLockDir: sleepLock},
		batches: make(map[string]*scanBatch),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/scans", strings.NewReader(`{"targets":["/scan"]}`))
	response := httptest.NewRecorder()

	s.startScan(response, req)

	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", response.Code)
	}
	var body startScanResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.ID != "" || body.Status != "failed" || body.Message != "ClamAV is sleeping." {
		t.Fatalf("unexpected sleeping response: %#v", body)
	}
	if len(s.batches) != 0 || len(s.queuedBatchIDs) != 0 {
		t.Fatalf("sleeping request entered scan queue: batches=%d queued=%d", len(s.batches), len(s.queuedBatchIDs))
	}
}

func TestWhitelistUpdatesRejectSleepingClamAV(t *testing.T) {
	tmp := t.TempDir()
	sleepLock := filepath.Join(tmp, "sleep.lock")
	if err := os.Mkdir(sleepLock, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: config{SleepLockDir: sleepLock}}

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/whitelist", strings.NewReader(`{"path":"/scan/example"}`))
			response := httptest.NewRecorder()
			s.handleWhitelist(response, req)

			if response.Code != http.StatusConflict {
				t.Fatalf("expected 409, got %d", response.Code)
			}
			var body map[string]any
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["status"] != "failed" || body["message"] != nil || body["error"] != "ClamAV is sleeping" {
				t.Fatalf("unexpected sleeping response: %#v", body)
			}
		})
	}
}

func TestStatusCachesAndCoalescesClamdProbe(t *testing.T) {
	binDir := t.TempDir()
	counterPath := filepath.Join(t.TempDir(), "ping-count")
	socketPath := filepath.Join(t.TempDir(), "clamd.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	versionCommands := make(chan string, 2)
	versionErrors := make(chan error, 1)
	go func() {
		for range 2 {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				versionErrors <- acceptErr
				return
			}
			command := make([]byte, len("VERSION\n"))
			_, readErr := io.ReadFull(conn, command)
			if readErr == nil {
				_, readErr = io.WriteString(conn, "ClamAV 1.5.4/28098/Thu Aug 14 14:24:22 2026\n")
			}
			_ = conn.Close()
			if readErr != nil {
				versionErrors <- readErr
				return
			}
			if string(command) != "VERSION\n" {
				versionErrors <- fmt.Errorf("unexpected clamd command: %q", command)
				return
			}
			versionCommands <- string(command)
		}
	}()

	clamdscan := filepath.Join(binDir, "clamdscan")
	if err := os.WriteFile(clamdscan, []byte("#!/bin/sh\nprintf x >> \"$CLAMD_PING_TEST_COUNTER\"\nsleep 0.05\nprintf PONG\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAMD_PING_TEST_COUNTER", counterPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := &server{cfg: config{
		StatusFile:    filepath.Join(t.TempDir(), "missing-status.json"),
		ClamdConf:     filepath.Join(t.TempDir(), "clamd.conf"),
		ClamdSocket:   socketPath,
		CommandTimout: time.Second,
	}}
	var checkedAt string
	for range 2 {
		response := httptest.NewRecorder()
		s.handleStatus(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("unexpected status response: %d %s", response.Code, response.Body.String())
		}
		var body statusResponse
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Ping != "ready" || body.ClamdVersion != "1.5.4" || body.DatabaseVersion != "28098" || body.DatabaseDate != "Thu Aug 14 14:24:22 2026" {
			t.Fatalf("unexpected status response: %#v", body)
		}
		if checkedAt == "" {
			s.clamdStatusMu.Lock()
			checkedAt = s.clamdStatusCache.checkedAt.Format(time.RFC3339)
			s.clamdStatusMu.Unlock()
			if body.CheckedAt != checkedAt {
				t.Fatalf("checked_at does not match the cached probe: response=%q cache=%q", body.CheckedAt, checkedAt)
			}
		} else if body.CheckedAt != checkedAt {
			t.Fatalf("cached checked_at changed: first=%q second=%q", checkedAt, body.CheckedAt)
		}
	}
	assertPingCount(t, counterPath, 1)
	assertVersionQueryCount(t, versionCommands, 1)

	// Expire the entry and issue a burst. The cache mutex must combine every
	// request into one new ping and VERSION probe rather than merely cache afterward.
	s.clamdStatusMu.Lock()
	s.clamdStatusCache.checkedAt = time.Now().Add(-clamdStatusCacheTTL)
	s.clamdStatusMu.Unlock()
	var callers sync.WaitGroup
	for range 8 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			status := s.cachedClamdStatus(t.Context())
			if status.ping != "ready" || status.pingMessage != "PONG" || status.clamdVersion != "1.5.4" || status.databaseVersion != "28098" {
				t.Errorf("unexpected cached ClamAV status: %#v", status)
			}
		}()
	}
	callers.Wait()
	assertPingCount(t, counterPath, 2)
	assertVersionQueryCount(t, versionCommands, 2)
	select {
	case err := <-versionErrors:
		t.Fatal(err)
	default:
	}
}

func assertPingCount(t *testing.T, path string, want int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != want {
		t.Fatalf("expected %d clamd ping processes, got %d", want, len(data))
	}
}

func assertVersionQueryCount(t *testing.T, commands chan string, want int) {
	t.Helper()
	if len(commands) != want {
		t.Fatalf("expected %d clamd VERSION queries, got %d", want, len(commands))
	}
}

func TestArgon2PasswordHash(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword("correct horse battery staple", hash) {
		t.Fatal("expected password to verify")
	}
	if verifyPassword("wrong password", hash) {
		t.Fatal("wrong password unexpectedly verified")
	}
	if hash2, _ := hashPassword("correct horse battery staple"); hash == hash2 {
		t.Fatal("expected independently salted password hashes")
	}
}

func TestSafePathRejectsEverySymlinkComponent(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "scan")
	outside := filepath.Join(tmp, "outside")
	inside := filepath.Join(root, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(inside, "regular.dat")
	if err := os.WriteFile(regular, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"escape":      outside,
		"inside-dir":  inside,
		"inside-file": regular,
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}

	s := &server{cfg: config{BrowseRoots: []string{root}}}
	if _, err := s.safePath(regular); err != nil {
		t.Fatalf("regular path was rejected: %v", err)
	}
	for _, path := range []string{
		filepath.Join(root, "escape"),
		filepath.Join(root, "inside-dir"),
		filepath.Join(root, "inside-dir", "regular.dat"),
		filepath.Join(root, "inside-file"),
	} {
		if _, err := s.safePath(path); !errors.Is(err, errSymlinkPath) {
			t.Fatalf("expected symlink path %s to be rejected, got %v", path, err)
		}
	}
	if _, err := s.safePathAllowMissing(filepath.Join(inside, "new", "file.dat")); err != nil {
		t.Fatalf("missing restore path was rejected: %v", err)
	}
	if _, err := s.safePathAllowMissing(filepath.Join(root, "inside-dir", "new.dat")); !errors.Is(err, errSymlinkPath) {
		t.Fatalf("expected missing path below symlink to be rejected, got %v", err)
	}
}

func TestBrowseHidesSymbolicLinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "scan")
	if err := os.MkdirAll(filepath.Join(root, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(root, "regular.dat")
	if err := os.WriteFile(regular, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"directory-link": filepath.Join(root, "directory"),
		"file-link":      regular,
		"broken-link":    filepath.Join(root, "missing"),
	} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}

	s := &server{cfg: config{BrowseRoots: []string{root}}}
	response := httptest.NewRecorder()
	s.handleBrowse(response, httptest.NewRequest(http.MethodGet, "/api/browse?path="+root, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("browse failed: %d %s", response.Code, response.Body.String())
	}
	var body browseResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 2 || body.Entries[0].Name != "directory" || body.Entries[1].Name != "regular.dat" {
		t.Fatalf("symbolic links were exposed by browse API: %#v", body.Entries)
	}
	if body.Target.Path != root || !body.Target.IsDir {
		t.Fatalf("unexpected directory target: %#v", body.Target)
	}

	fileResponse := httptest.NewRecorder()
	s.handleBrowse(fileResponse, httptest.NewRequest(http.MethodGet, "/api/browse?path="+regular, nil))
	if fileResponse.Code != http.StatusOK {
		t.Fatalf("browse file failed: %d %s", fileResponse.Code, fileResponse.Body.String())
	}
	if err := json.NewDecoder(fileResponse.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Path != root || body.Target.Path != regular || body.Target.IsDir {
		t.Fatalf("file browse did not return its parent and target: %#v", body)
	}
}

func TestBatchSnapshotsFromJobs(t *testing.T) {
	tmp := t.TempDir()
	jobsDir := filepath.Join(tmp, "jobs")
	if err := os.Mkdir(jobsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJob := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(jobsDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeJob("web-abc-001.json", `{"job_id":"web-abc-001","status":"finished","target":"/scan/a","action":"warn","started_at":"20260706010101","finished_at":"20260706010102","result":"clean"}`)
	writeJob("web-abc-002.json", `{"job_id":"web-abc-002","status":"finished","target":"/scan/b","action":"warn","started_at":"20260706010201","finished_at":"20260706010202","result":"found"}`)
	writeJob("manual-20260706010303.json", `{"job_id":"manual-20260706010303","status":"finished","target":"/scan/c","action":"warn","started_at":"20260706010303","finished_at":"20260706010304","result":"clean"}`)

	s := &server{cfg: config{JobsDir: jobsDir}}
	batch := s.batchSnapshotsFromJobs()["web-abc"]
	if batch == nil {
		t.Fatal("expected batch to be rebuilt from job files")
	}
	if batch.Completed != 2 || batch.Threats != 1 || batch.Status != "finished" {
		t.Fatalf("unexpected rebuilt batch: %#v", batch)
	}
	if len(batch.Targets) != 2 || batch.Targets[0] != "/scan/a" || batch.Targets[1] != "/scan/b" {
		t.Fatalf("unexpected targets: %#v", batch.Targets)
	}
	manual := s.batchSnapshotsFromJobs()["manual-20260706010303"]
	if manual == nil || manual.Completed != 1 || len(manual.JobIDs) != 1 {
		t.Fatalf("expected manual job batch to be rebuilt, got %#v", manual)
	}
}

func TestRunScanScriptReadsJobID(t *testing.T) {
	tmp := t.TempDir()
	script := filepath.Join(tmp, "scan_once.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho JOB_ID=manual-20260707123456\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := &server{cfg: config{ScanScript: script}, clamavSleepTimer: newClamAVSleepTimerState()}
	jobID, output, err := s.runScanScript([]string{"--type", "manual", "--target", "/scan", "--action", "warn"})
	if err != nil {
		t.Fatalf("runScanScript returned error: %v output=%q", err, output)
	}
	if jobID != "manual-20260707123456" {
		t.Fatalf("unexpected job id: %q", jobID)
	}
	if generation := s.clamavSleepTimer.currentGeneration(); generation != 1 {
		t.Fatalf("manual scan start did not reset sleep timer: generation=%d", generation)
	}
}

func TestScanQueueActivateStartsFirstBatch(t *testing.T) {
	s := &server{
		queuedBatchIDs: []string{"web-first"},
		batches:        make(map[string]*scanBatch),
	}
	batch := &scanBatch{
		ID:      "web-first",
		Status:  "queued",
		Targets: []string{"/scan/a"},
		Action:  "warn",
		Message: "Queued",
	}
	s.batches[batch.ID] = batch

	if !s.activateQueuedBatch("web-first") {
		t.Fatal("expected queued batch to be activated")
	}
	if s.activeBatchID != "web-first" || len(s.queuedBatchIDs) != 0 {
		t.Fatalf("expected first batch to become active, active=%q queued=%#v", s.activeBatchID, s.queuedBatchIDs)
	}
	if s.batches["web-first"].StartedAt == nil {
		t.Fatal("expected started_at to be set when batch starts running")
	}
}

func TestScanLockBlocksManualStartOnlyForLivePID(t *testing.T) {
	tmp := t.TempDir()
	lockDir := filepath.Join(tmp, "scan.lock")
	if err := os.Mkdir(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &server{cfg: config{ScanLockDir: lockDir}}
	blocked, err := s.scanLockBlocksManualStart()
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Fatal("expected live pid lock to block manual queue start")
	}

	if err := os.WriteFile(filepath.Join(lockDir, "pid"), []byte("99999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocked, err = s.scanLockBlocksManualStart()
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Fatal("expected stale pid lock to allow scan_once.sh to start and clean it")
	}
	if _, err := os.Stat(filepath.Join(lockDir, "pid")); err != nil {
		t.Fatalf("expected scans.go to leave stale lock files untouched: %v", err)
	}
}

func TestScanQueueListScope(t *testing.T) {
	started := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	s := &server{
		activeBatchID:  "web-active",
		queuedBatchIDs: []string{"web-one", "web-two", "web-three"},
		batches: map[string]*scanBatch{
			"web-active": {ID: "web-active", Status: "running", Targets: []string{"/scan/a"}, Action: "warn", StartedAt: &started},
			"web-one":    {ID: "web-one", Status: "queued", Targets: []string{"/scan/b"}, Action: "move"},
			"web-two":    {ID: "web-two", Status: "queued", Targets: []string{"/scan/c"}, Action: "warn"},
			"web-three":  {ID: "web-three", Status: "queued", Targets: []string{"/scan/d"}, Action: "remove"},
		},
	}

	first, err := s.listQueueItems("1-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != "web-active" || first[0].QueueNumber != 0 || first[1].ID != "web-one" || first[1].QueueNumber != 1 {
		t.Fatalf("unexpected 1-1 scope result: %#v", first)
	}
	if first[1].StartedAt != nil {
		t.Fatalf("expected queued started_at to be nil, got %#v", first[1].StartedAt)
	}

	later, err := s.listQueueItems("2-3")
	if err != nil {
		t.Fatal(err)
	}
	if len(later) != 2 || later[0].ID != "web-two" || later[0].QueueNumber != 2 || later[1].ID != "web-three" || later[1].QueueNumber != 3 {
		t.Fatalf("unexpected 2-3 scope result: %#v", later)
	}
}

func TestScanQueueReorderAndCancelQueuedOnly(t *testing.T) {
	s := &server{
		activeBatchID:  "web-active",
		queuedBatchIDs: []string{"web-one", "web-two", "web-three"},
		batches: map[string]*scanBatch{
			"web-active": {ID: "web-active", Status: "running", UserID: testAliceUserID},
			"web-one":    {ID: "web-one", Status: "queued", UserID: testAliceUserID},
			"web-two":    {ID: "web-two", Status: "queued", UserID: testAliceUserID},
			"web-three":  {ID: "web-three", Status: "queued", UserID: testAliceUserID},
		},
	}

	if err := s.reorderQueuedBatch("web-active", 1, testAliceUserID); err == nil {
		t.Fatal("expected running batch reorder to be rejected")
	}
	if err := s.reorderQueuedBatch("web-three", 1, testAliceUserID); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.queuedBatchIDs, ","); got != "web-three,web-one,web-two" {
		t.Fatalf("unexpected queue after reorder: %s", got)
	}
	if err := s.cancelQueuedBatch("web-active", "Y", testAliceUserID); err == nil {
		t.Fatal("expected running batch cancel to be rejected")
	}
	if err := s.cancelQueuedBatch("web-one", "N", testAliceUserID); err == nil {
		t.Fatal("expected missing cancel confirmation to be rejected")
	}
	if err := s.cancelQueuedBatch("web-one", "Y", testAliceUserID); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.queuedBatchIDs, ","); got != "web-three,web-two" {
		t.Fatalf("unexpected queue after cancel: %s", got)
	}
	if _, ok := s.batches["web-one"]; ok {
		t.Fatal("expected canceled batch to be removed from memory")
	}
}

func TestCronRuleID(t *testing.T) {
	id := randomAlphaNum(16)
	if !validCronRuleID(id) {
		t.Fatalf("expected generated id to be valid, got %q", id)
	}
	if validCronRuleID("ABC") {
		t.Fatal("expected invalid id to be rejected")
	}
}

func TestCronConfigPreservesCommentsAndDisabledRule(t *testing.T) {
	tmp := t.TempDir()
	cronConfig := filepath.Join(tmp, "cron_scan.conf")
	cronScript := filepath.Join(tmp, "cron.sh")
	body := strings.Join([]string{
		"# user readable header",
		"# scanner-cron-rule id=abc123def456gh78 enabled=true",
		"30 3 * * * /scan/docs warn alice001 Y",
		"# user note",
		"",
	}, "\n")
	if err := os.WriteFile(cronConfig, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cronScript, []byte("#!/bin/sh\nif [ \"$1\" = validate ]; then echo 'Cron config is valid: 1 rule(s).'; exit 0; fi\necho 'Cron rules reloaded: 1 rule(s).'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	scanRoot := filepath.Join(tmp, "scan")
	if err := os.MkdirAll(filepath.Join(scanRoot, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: config{
		CronConfigFile: cronConfig,
		CronScript:     cronScript,
		BrowseRoots:    []string{scanRoot},
	}}

	if _, _, err := s.setCronRuleEnabled("abc123def456gh78", false); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(cronConfig)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	if !strings.Contains(text, "# user readable header") || !strings.Contains(text, "# user note") {
		t.Fatalf("expected user comments to be preserved, got:\n%s", text)
	}
	if !strings.Contains(text, "# scanner-cron-rule id=abc123def456gh78 enabled=false") {
		t.Fatalf("expected metadata to be disabled, got:\n%s", text)
	}
	if !strings.Contains(text, "# 30 3 * * * /scan/docs warn alice001 Y") {
		t.Fatalf("expected rule line to be commented, got:\n%s", text)
	}
}

func TestValidateCronRuleFields(t *testing.T) {
	valid := cronRule{Minute: "*/5", Hour: "0-23/2", Day: "*", Month: "1,6,12", Weekday: "0-7", Target: "/scan", Action: "warn", Wake: true}
	if err := validateCronRuleFields(valid); err != nil {
		t.Fatalf("expected rule to be valid: %v", err)
	}
	invalid := valid
	invalid.Minute = "99"
	if err := validateCronRuleFields(invalid); err == nil || !strings.Contains(err.Error(), "minute") {
		t.Fatalf("expected minute validation error, got %v", err)
	}
}

func TestCronRuleWakeColumn(t *testing.T) {
	wakeRule, err := parseCronRuleLine(`15 2 * * 0 "/scan/My Folder" remove admin001 Y`)
	if err != nil || !wakeRule.Wake {
		t.Fatalf("expected Y to enable wake, rule=%#v err=%v", wakeRule, err)
	}
	wakeRule.Enabled = true
	if got := cronConfigLine(wakeRule); got != `15 2 * * 0 "/scan/My Folder" remove admin001 Y` {
		t.Fatalf("unexpected serialized wake rule: %q", got)
	}

	noWakeRule, err := parseCronRuleLine("0 3 * * * /scan warn alice001 N")
	if err != nil || noWakeRule.Wake {
		t.Fatalf("expected N to disable wake, rule=%#v err=%v", noWakeRule, err)
	}
	if _, err := parseCronRuleLine("0 3 * * * /scan warn alice001 maybe"); err == nil {
		t.Fatal("expected an invalid wake column to be rejected")
	}
	if _, err := parseCronRuleLine("0 3 * * * /scan warn alice001"); err == nil {
		t.Fatal("expected a missing wake column to be rejected")
	}
}

func TestCronScriptMessage(t *testing.T) {
	output := "2026 log noise\nInvalid crontab rule at: minute, line 2, value 99\n"
	if got := cronScriptMessage(output); got != "Invalid crontab rule at: minute, line 2, value 99" {
		t.Fatalf("unexpected message: %q", got)
	}
}

func TestWhitelistEntriesParseQuotedPaths(t *testing.T) {
	tmp := t.TempDir()
	excludeConfig := filepath.Join(tmp, "exclude.conf")
	body := strings.Join([]string{
		"# trusted paths",
		"/scan/trusted alice001",
		"\"/scan/file with space.zip\" alice001",
		"/scan/invalid path extra",
	}, "\n")
	if err := os.WriteFile(excludeConfig, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &server{cfg: config{ExcludeConfig: excludeConfig}}
	entries, err := s.readWhitelistEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 parseable entries, got %#v", entries)
	}
	if entries[0].Path != "/scan/trusted" || entries[0].Line != 2 {
		t.Fatalf("unexpected first entry: %#v", entries[0])
	}
	if entries[1].Path != "/scan/file with space.zip" || entries[1].Line != 3 {
		t.Fatalf("unexpected second entry: %#v", entries[1])
	}
}

func TestWhitelistAddDeleteAndBusyLock(t *testing.T) {
	tmp := t.TempDir()
	scanRoot := filepath.Join(tmp, "scan")
	target := filepath.Join(scanRoot, "trusted file.txt")
	if err := os.MkdirAll(scanRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	excludeConfig := filepath.Join(tmp, "exclude.conf")
	excludeScript := filepath.Join(tmp, "exclude.sh")
	if err := os.WriteFile(excludeScript, []byte("#!/bin/sh\necho refreshed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	lockDir := filepath.Join(tmp, "state", "scan.lock")
	s := &server{cfg: config{
		BrowseRoots:   []string{scanRoot},
		ExcludeConfig: excludeConfig,
		ExcludeScript: excludeScript,
		ScanLockDir:   lockDir,
	}}

	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if busy, err := s.scanLockActive(); err != nil || !busy {
		t.Fatalf("expected active scan lock, busy=%v err=%v", busy, err)
	}
	if err := os.Remove(lockDir); err != nil {
		t.Fatal(err)
	}

	if err := s.addWhitelistEntry(target, testAliceUserID); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(excludeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "\""+target+"\"") {
		t.Fatalf("expected quoted target in config, got %q", string(content))
	}
	message, err := s.runExcludeScript()
	if err != nil {
		t.Fatalf("runExcludeScript returned error: %v", err)
	}
	if message != "refreshed" {
		t.Fatalf("unexpected script message: %q", message)
	}
	if err := s.deleteWhitelistEntry(target, testAliceUserID); err != nil {
		t.Fatal(err)
	}
	entries, err := s.readWhitelistEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected whitelist to be empty after delete, got %#v", entries)
	}
}

func TestReadResultLogItems(t *testing.T) {
	tmp := t.TempDir()
	// Keep one legacy v2 fixture to verify the v3 index ignores username-owned jobs.
	files := map[string]string{
		"manual-1783431402.json": `{"version":3,"job_id":"manual-1783431402","type":"manual","status":"finished","result":"found","action":"warn","started_at":1783431402,"finished_at":1783431410,"user_id":"alice001"}`,
		"cron-1783407661.json":   `{"version":3,"job_id":"cron-1783407661","type":"cron","status":"finished","result":"clean","action":"remove","started_at":1783407661,"finished_at":1783407670,"user_id":"alice001"}`,
		"manual-1783432800.json": `{"version":3,"job_id":"manual-1783432800","type":"manual","status":"running","result":"unknown","action":"move","started_at":1783432800,"finished_at":null,"user_id":"alice001"}`,
		"manual-bad.json":        `{"version":3,"job_id":"manual-bad","user_id":"alice001"}`,
		"manual-1783432900.json": `{"version":2,"job_id":"manual-1783432900","user":"alice"}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	historyDB, err := openHistoryDatabase(filepath.Join(tmp, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer historyDB.Close()
	s := &server{cfg: config{JobsDir: tmp}, historyDB: historyDB}
	s.history = &historyIndexer{db: historyDB, jobsDir: tmp}
	if err := s.history.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.readResultLogItems(t.Context(), resultScope{All: true}, testAliceUserID)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("expected total to count all valid result jobs, got %d", total)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 result jobs, got %#v", items)
	}
	if items[0].ID != "manual-1783432800" || items[0].Type != "manual" || items[0].Result != "unknown" || items[0].Action != "move" {
		t.Fatalf("unexpected first item: %#v", items[0])
	}
	if items[1].ID != "manual-1783431402" || items[1].Type != "manual" || items[1].Result != "found" || items[1].Action != "warn" {
		t.Fatalf("unexpected second item: %#v", items[1])
	}
	if items[2].ID != "cron-1783407661" || items[2].Type != "cron" || items[2].Result != "clean" || items[2].Action != "remove" {
		t.Fatalf("unexpected third item: %#v", items[2])
	}

	scoped, scopedTotal, err := s.readResultLogItems(t.Context(), resultScope{Start: 1, End: 2}, testAliceUserID)
	if err != nil {
		t.Fatal(err)
	}
	if scopedTotal != 3 {
		t.Fatalf("expected scoped total to remain 3, got %d", scopedTotal)
	}
	if len(scoped) != 2 {
		t.Fatalf("expected 2 scoped result jobs, got %#v", scoped)
	}
	if scoped[0].ID != "manual-1783432800" || scoped[1].ID != "manual-1783431402" {
		t.Fatalf("unexpected scoped jobs: %#v", scoped)
	}
}

func TestParseResultScope(t *testing.T) {
	scoped, err := parseResultScope("2-5")
	if err != nil {
		t.Fatal(err)
	}
	if scoped.All || scoped.Start != 2 || scoped.End != 5 {
		t.Fatalf("unexpected parsed scope: %#v", scoped)
	}
	if _, err := parseResultScope("5-2"); err == nil {
		t.Fatal("expected invalid range to be rejected")
	}
	for _, invalid := range []string{"", "all", "1-501"} {
		if _, err := parseResultScope(invalid); err == nil {
			t.Fatalf("expected scope %q to be rejected", invalid)
		}
	}
	if _, err := parseResultScope("501-1000"); err != nil {
		t.Fatalf("expected a later 500-item page to be accepted: %v", err)
	}
}

func TestResultLookupRejectsInvalidScope(t *testing.T) {
	s := &server{resultLookups: make(map[string]*resultLookup)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/results/lookups?scope=bad", nil)

	s.handleResultLookupStart(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid scope to return 400, got %d", rec.Code)
	}
	if len(s.resultLookups) != 0 {
		t.Fatalf("expected no lookup to be created, got %#v", s.resultLookups)
	}
}

func TestReadDetectionResult(t *testing.T) {
	tmp := t.TempDir()
	jobsDir := filepath.Join(tmp, "jobs")
	logDir := filepath.Join(tmp, "logs")
	for _, dir := range []string{jobsDir, logDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	historyDB, err := openHistoryDatabase(filepath.Join(tmp, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = historyDB.Close() })
	insertJob := func(jobID, jobType string) {
		t.Helper()
		if _, err := historyDB.Exec(`INSERT INTO history_jobs(job_id,job_type,status,result,action,started_at,finished_at,user_id,json_file,file_mtime_ns,indexed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			jobID, jobType, "finished", "found", "warn", 1783433000, 1783433010, testAliceUserID, filepath.Join(jobsDir, jobID+".json"), 1, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	jobID := "manual-20260707173642"
	body := strings.Join([]string{
		"2026-07-07 17:37:10 ------------ ClamAV Detection ------------",
		"2026-07-07 17:37:10 [DETECTION] Source file      : /scan/eicar.txt",
		"2026-07-07 17:37:10 [DETECTION] Source directory : /scan",
		"2026-07-07 17:37:10 [DETECTION] Detection reason : Eicar-Test-Signature",
		"2026-07-07 17:37:11 ------------ ClamAV Detection ------------",
		"2026-07-07 17:37:11 [DETECTION] Source file      : /scan/eicar2.txt",
		"2026-07-07 17:37:11 [DETECTION] Source directory : /scan",
		"2026-07-07 17:37:11 [DETECTION] Detection reason : Eicar-Test-Signature-2",
		"2026-07-07 17:37:10 ------------------------------------------",
	}, "\n")
	if err := os.WriteFile(filepath.Join(logDir, "clamav_detection_"+jobID+".log"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	jobLog := "2026-07-07 17:36:42 [INFO] Scan started\n2026-07-07 17:37:10 [ALERT] Threat found\n"
	if err := os.WriteFile(filepath.Join(logDir, jobID+".log"), []byte(jobLog), 0o644); err != nil {
		t.Fatal(err)
	}
	insertJob(jobID, "manual")
	indexedDetections := []detectionItem{
		{SourceFile: "/indexed/eicar.txt", DetectionReason: "Indexed-Signature"},
		{SourceFile: "/indexed/eicar2.txt", DetectionReason: "Indexed-Signature-2"},
	}
	for sequence, detection := range indexedDetections {
		if _, err := historyDB.Exec(`INSERT INTO history_detections(job_id,sequence,source_file,detection_reason) VALUES(?,?,?,?)`,
			jobID, sequence, detection.SourceFile, detection.DetectionReason); err != nil {
			t.Fatal(err)
		}
	}

	s := &server{cfg: config{JobsDir: jobsDir, LogDir: logDir}, historyDB: historyDB}
	result, err := s.readDetectionResult(jobID, testAliceUserID)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected detection result")
	}
	if len(result.Detections) != 2 {
		t.Fatalf("expected 2 detections, got %#v", result.Detections)
	}
	if result.Detections[0].SourceFile != "/indexed/eicar.txt" || result.Detections[0].DetectionReason != "Indexed-Signature" {
		t.Fatalf("unexpected first detection: %#v", result.Detections[0])
	}
	if result.Detections[1].SourceFile != "/indexed/eicar2.txt" || result.Detections[1].DetectionReason != "Indexed-Signature-2" {
		t.Fatalf("unexpected second detection: %#v", result.Detections[1])
	}
	if result.Original != body {
		t.Fatal("expected original log content to be preserved")
	}
	if result.Log != jobLog {
		t.Fatalf("expected job log content to be returned, got %q", result.Log)
	}

	missingJobID := "cron-20260707173643"
	missingJobLog := "cron scan log\n"
	if err := os.WriteFile(filepath.Join(logDir, missingJobID+".log"), []byte(missingJobLog), 0o644); err != nil {
		t.Fatal(err)
	}
	insertJob(missingJobID, "cron")
	missing, err := s.readDetectionResult(missingJobID, testAliceUserID)
	if err != nil {
		t.Fatal(err)
	}
	if missing == nil {
		t.Fatal("expected missing detection log to still return job log object")
	}
	if missing.Log != missingJobLog {
		t.Fatalf("expected job log to be returned without detection log, got %#v", missing)
	}
	if missing.Original != "" || len(missing.Detections) != 0 {
		t.Fatalf("expected detection fields to be empty when detection log is missing, got %#v", missing)
	}
}

func TestQuarantineSubjectsDeleteAndRecover(t *testing.T) {
	tmp := t.TempDir()
	quarantineDir := filepath.Join(tmp, "quarantine")
	sourceDir := filepath.Join(tmp, "scan")
	if err := os.MkdirAll(quarantineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	quarantined := filepath.Join(quarantineDir, "eicar.txt")
	sourceFile := filepath.Join(sourceDir, "eicar.txt")
	if err := os.WriteFile(quarantined, []byte("infected"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantined+".rec", []byte("\""+sourceFile+"\" alice001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(quarantineDir, "orphan.rec"), []byte("\"/scan/orphan\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(quarantineDir, clamavQuarantineLockName), []byte("lock"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &server{cfg: config{QuarantineDir: quarantineDir, BrowseRoots: []string{sourceDir}}}
	subjects, err := s.readQuarantineSubjects(t.Context(), testAliceUserID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 1 {
		t.Fatalf("expected one quarantine subject, got %#v", subjects)
	}
	if subjects[0].Name != "eicar.txt" || subjects[0].SourceFile != sourceFile {
		t.Fatalf("unexpected quarantine subject: %#v", subjects[0])
	}

	if err := s.recoverQuarantineSubject("eicar.txt", actor{ID: testAliceUserID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sourceFile); err != nil {
		t.Fatalf("expected file to be recovered: %v", err)
	}
	if _, err := os.Stat(quarantined); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected quarantined file to be moved away, err=%v", err)
	}
	if _, err := os.Stat(quarantined + ".rec"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected rec file to be deleted after recover, err=%v", err)
	}

	deleteTarget := filepath.Join(quarantineDir, "delete-me.txt")
	if err := os.WriteFile(deleteTarget, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deleteTarget+".rec", []byte("\"/scan/delete-me.txt\" alice001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.deleteQuarantineSubject("delete-me.txt", testAliceUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(deleteTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected quarantine file to be deleted, err=%v", err)
	}
	if _, err := os.Stat(deleteTarget + ".rec"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected rec file to be deleted with quarantine file, err=%v", err)
	}
}

func TestQuarantineRecoverCopiesAndRemovesSource(t *testing.T) {
	tmp := t.TempDir()
	quarantineDir := filepath.Join(tmp, "quarantine")
	sourceDir := filepath.Join(tmp, "scan")
	if err := os.MkdirAll(quarantineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	quarantined := filepath.Join(quarantineDir, "cross-device.txt")
	sourceFile := filepath.Join(sourceDir, "cross-device.txt")
	content := []byte("infected sample")
	if err := os.WriteFile(quarantined, content, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantined+".rec", []byte("\""+sourceFile+"\" alice001\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &server{cfg: config{QuarantineDir: quarantineDir, BrowseRoots: []string{sourceDir}}}
	if err := s.recoverQuarantineSubject("cross-device.txt", actor{ID: testAliceUserID}); err != nil {
		t.Fatal(err)
	}
	recovered, err := os.ReadFile(sourceFile)
	if err != nil {
		t.Fatalf("expected file to be recovered: %v", err)
	}
	if string(recovered) != string(content) {
		t.Fatalf("unexpected recovered content: %q", recovered)
	}
	if _, err := os.Stat(quarantined); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected quarantined file to be deleted after fallback copy, err=%v", err)
	}
	if _, err := os.Stat(quarantined + ".rec"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected rec file to be deleted after recover, err=%v", err)
	}
}

func TestQuarantineRecoverDoesNotOverwriteTargetCreatedAfterCheck(t *testing.T) {
	tmp := t.TempDir()
	quarantineDir := filepath.Join(tmp, "quarantine")
	sourceDir := filepath.Join(tmp, "scan")
	if err := os.MkdirAll(quarantineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	quarantined := filepath.Join(quarantineDir, "race.txt")
	sourceFile := filepath.Join(sourceDir, "race.txt")
	recordFile := quarantined + ".rec"
	if err := os.WriteFile(quarantined, []byte("quarantined"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordFile, []byte("\""+sourceFile+"\" "+testAliceUserID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	originalCopy := copyQuarantineFile
	copyQuarantineFile = func(src, dst string) error {
		if err := os.WriteFile(dst, []byte("concurrent"), 0o600); err != nil {
			return err
		}
		return originalCopy(src, dst)
	}
	t.Cleanup(func() { copyQuarantineFile = originalCopy })

	s := &server{cfg: config{QuarantineDir: quarantineDir, BrowseRoots: []string{sourceDir}}}
	err := s.recoverQuarantineSubject("race.txt", actor{ID: testAliceUserID})
	if err == nil || !strings.Contains(err.Error(), "target already exists") {
		t.Fatalf("expected concurrent target to reject recovery, got %v", err)
	}
	target, err := os.ReadFile(sourceFile)
	if err != nil || string(target) != "concurrent" {
		t.Fatalf("concurrent target was changed: data=%q err=%v", target, err)
	}
	for _, path := range []string{quarantined, recordFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retry state was not preserved for %s: %v", path, err)
		}
	}

	copyQuarantineFile = originalCopy
	if err := os.Remove(sourceFile); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverQuarantineSubject("race.txt", actor{ID: testAliceUserID}); err != nil {
		t.Fatalf("recovery retry failed: %v", err)
	}
	restored, err := os.ReadFile(sourceFile)
	if err != nil || string(restored) != "quarantined" {
		t.Fatalf("unexpected retry result: data=%q err=%v", restored, err)
	}
}

func TestQuarantineNameFromPathRejectsTraversal(t *testing.T) {
	if _, err := quarantineNameFromPath("/api/quarantine/delete/..%2Feicar.txt", "/api/quarantine/delete/"); err == nil {
		t.Fatal("expected traversal filename to be rejected")
	}
	if _, err := quarantineNameFromPath("/api/quarantine/delete/eicar.txt.rec", "/api/quarantine/delete/"); err == nil {
		t.Fatal("expected .rec filename to be rejected")
	}
}
