package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseAccounts(t *testing.T) {
	accounts, err := parseAccounts("admin:secret, ops:pass:with:colon;user:pw")
	if err != nil {
		t.Fatalf("parseAccounts returned error: %v", err)
	}

	if len(accounts) != 3 {
		t.Fatalf("expected 3 accounts, got %d", len(accounts))
	}
	if accounts[0].Username != "admin" || accounts[0].Password != "secret" {
		t.Fatalf("unexpected first account: %#v", accounts[0])
	}
	if accounts[1].Username != "ops" || accounts[1].Password != "pass:with:colon" {
		t.Fatalf("unexpected second account: %#v", accounts[1])
	}
}

func TestParseAccountsRejectsInvalidEntries(t *testing.T) {
	tests := []string{
		"admin",
		":secret",
		"admin:",
		"admin:secret,admin:other",
	}

	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			if _, err := parseAccounts(test); err == nil {
				t.Fatal("expected parseAccounts to reject invalid entry")
			}
		})
	}
}

func TestRequireAuth(t *testing.T) {
	s := &server{
		cfg: config{Accounts: []account{{Username: "admin", Password: "secret"}}},
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	unauthorized := httptest.NewRecorder()
	s.requireAuth(next).ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized request to return 401, got %d", unauthorized.Code)
	}
	if unauthorized.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("expected WWW-Authenticate header")
	}

	authorizedReq := httptest.NewRequest(http.MethodGet, "/", nil)
	authorizedReq.SetBasicAuth("admin", "secret")
	authorized := httptest.NewRecorder()
	s.requireAuth(next).ServeHTTP(authorized, authorizedReq)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("expected authorized request to pass through, got %d", authorized.Code)
	}
}

func TestSafePathRejectsSymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "scan")
	outside := filepath.Join(tmp, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	s := &server{cfg: config{BrowseRoots: []string{root}}}
	if _, err := s.safePath(filepath.Join(root, "escape")); err == nil {
		t.Fatal("expected symlink escaping the scan root to be rejected")
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

	s := &server{cfg: config{ScanScript: script}}
	jobID, output, err := s.runScanScript([]string{"--type", "manual", "--target", "/scan", "--action", "warn"})
	if err != nil {
		t.Fatalf("runScanScript returned error: %v output=%q", err, output)
	}
	if jobID != "manual-20260707123456" {
		t.Fatalf("unexpected job id: %q", jobID)
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
			"web-active": {ID: "web-active", Status: "running"},
			"web-one":    {ID: "web-one", Status: "queued"},
			"web-two":    {ID: "web-two", Status: "queued"},
			"web-three":  {ID: "web-three", Status: "queued"},
		},
	}

	if err := s.reorderQueuedBatch("web-active", 1); err == nil {
		t.Fatal("expected running batch reorder to be rejected")
	}
	if err := s.reorderQueuedBatch("web-three", 1); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.queuedBatchIDs, ","); got != "web-three,web-one,web-two" {
		t.Fatalf("unexpected queue after reorder: %s", got)
	}
	if err := s.cancelQueuedBatch("web-active", "Y"); err == nil {
		t.Fatal("expected running batch cancel to be rejected")
	}
	if err := s.cancelQueuedBatch("web-one", "N"); err == nil {
		t.Fatal("expected missing cancel confirmation to be rejected")
	}
	if err := s.cancelQueuedBatch("web-one", "Y"); err != nil {
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
		"30 3 * * * /scan/docs warn",
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
	if !strings.Contains(text, "# 30 3 * * * /scan/docs warn") {
		t.Fatalf("expected rule line to be commented, got:\n%s", text)
	}
}

func TestValidateCronRuleFields(t *testing.T) {
	valid := cronRule{Minute: "*/5", Hour: "0-23/2", Day: "*", Month: "1,6,12", Weekday: "0-7", Target: "/scan", Action: "warn"}
	if err := validateCronRuleFields(valid); err != nil {
		t.Fatalf("expected rule to be valid: %v", err)
	}
	invalid := valid
	invalid.Minute = "99"
	if err := validateCronRuleFields(invalid); err == nil || !strings.Contains(err.Error(), "minute") {
		t.Fatalf("expected minute validation error, got %v", err)
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
		"/scan/trusted",
		"\"/scan/file with space.zip\"",
		"/scan/invalid path",
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

	if err := s.addWhitelistEntry(target); err != nil {
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
	if err := s.deleteWhitelistEntry(target); err != nil {
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
	files := map[string]string{
		"manual-20260707173642.json": `{"job_id":"manual-20260707173642","result":"found"}`,
		"cron-20260707094101.json":   `{"job_id":"cron-20260707094101","result":"clean"}`,
		"manual-20260707180000.json": `{"job_id":"manual-20260707180000","result":null}`,
		"manual-bad.json":            `{"job_id":"manual-bad","result":"found"}`,
		"web-123.json":               `{"job_id":"web-123","result":"clean"}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s := &server{cfg: config{JobsDir: tmp}}
	items, total, err := s.readResultLogItems(resultScope{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("expected total to count all valid result jobs, got %d", total)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 result jobs, got %#v", items)
	}
	if items[0].ID != "manual-20260707180000" || items[0].Type != "manual" || items[0].Date != "20260707180000" || items[0].Result != nil {
		t.Fatalf("unexpected first item: %#v", items[0])
	}
	if items[1].ID != "manual-20260707173642" || items[1].Type != "manual" || items[1].Date != "20260707173642" || items[1].Result != "found" {
		t.Fatalf("unexpected second item: %#v", items[1])
	}
	if items[2].ID != "cron-20260707094101" || items[2].Type != "cron" || items[2].Date != "20260707094101" || items[2].Result != "clean" {
		t.Fatalf("unexpected third item: %#v", items[2])
	}

	scoped, scopedTotal, err := s.readResultLogItems(resultScope{Start: 1, End: 2})
	if err != nil {
		t.Fatal(err)
	}
	if scopedTotal != 3 {
		t.Fatalf("expected scoped total to remain 3, got %d", scopedTotal)
	}
	if len(scoped) != 2 {
		t.Fatalf("expected 2 scoped result jobs, got %#v", scoped)
	}
	if scoped[0].ID != "manual-20260707180000" || scoped[1].ID != "manual-20260707173642" {
		t.Fatalf("unexpected scoped jobs: %#v", scoped)
	}
}

func TestParseResultScope(t *testing.T) {
	all, err := parseResultScope("")
	if err != nil {
		t.Fatal(err)
	}
	if !all.All {
		t.Fatalf("expected default scope to be all, got %#v", all)
	}
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
	if err := os.WriteFile(filepath.Join(tmp, "clamav_detection_"+jobID+".log"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	jobLog := "2026-07-07 17:36:42 [INFO] Scan started\n2026-07-07 17:37:10 [ALERT] Threat found\n"
	if err := os.WriteFile(filepath.Join(tmp, jobID+".log"), []byte(jobLog), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &server{cfg: config{LogDir: tmp}}
	result, err := s.readDetectionResult(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected detection result")
	}
	if len(result.Detections) != 2 {
		t.Fatalf("expected 2 detections, got %#v", result.Detections)
	}
	if result.Detections[0].SourceFile != "/scan/eicar.txt" || result.Detections[0].DetectionReason != "Eicar-Test-Signature" {
		t.Fatalf("unexpected first detection: %#v", result.Detections[0])
	}
	if result.Detections[1].SourceFile != "/scan/eicar2.txt" || result.Detections[1].DetectionReason != "Eicar-Test-Signature-2" {
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
	if err := os.WriteFile(filepath.Join(tmp, missingJobID+".log"), []byte(missingJobLog), 0o644); err != nil {
		t.Fatal(err)
	}
	missing, err := s.readDetectionResult(missingJobID)
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

func TestCleanAllResults(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "log")
	jobsDir := filepath.Join(tmp, "jobs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(jobsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logFiles := []string{
		"manual-20260707173642.log",
		"cron-20260707173643.log",
		"clamav_detection_manual-20260707173642.log",
		"startup.log",
		"manual-20260707173642.txt",
	}
	for _, name := range logFiles {
		if err := os.WriteFile(filepath.Join(logDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	jobFiles := []string{
		"manual-20260707173642.json",
		"cron-20260707173643.json",
		"web-abc.json",
		"manual-20260707173642.log",
	}
	for _, name := range jobFiles {
		if err := os.WriteFile(filepath.Join(jobsDir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s := &server{cfg: config{LogDir: logDir, JobsDir: jobsDir}}
	deleted, err := s.cleanAllResults()
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 5 {
		t.Fatalf("expected 5 files to be deleted, got %d", deleted)
	}
	for _, name := range []string{"startup.log", "manual-20260707173642.txt"} {
		if _, err := os.Stat(filepath.Join(logDir, name)); err != nil {
			t.Fatalf("expected log file to remain: %s: %v", name, err)
		}
	}
	for _, name := range []string{"web-abc.json", "manual-20260707173642.log"} {
		if _, err := os.Stat(filepath.Join(jobsDir, name)); err != nil {
			t.Fatalf("expected job file to remain: %s: %v", name, err)
		}
	}
}

func TestCleanDirectoryChildren(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "nested", "b.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	deleted, err := cleanDirectoryChildren(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("expected 2 quarantine entries to be deleted, got %d", deleted)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected quarantine dir to be empty, got %#v", entries)
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
	if err := os.WriteFile(quarantined+".rec", []byte("\""+sourceFile+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(quarantineDir, "orphan.rec"), []byte("\"/scan/orphan\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(quarantineDir, clamavQuarantineLockName), []byte("lock"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &server{cfg: config{QuarantineDir: quarantineDir}}
	subjects, err := s.readQuarantineSubjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 1 {
		t.Fatalf("expected one quarantine subject, got %#v", subjects)
	}
	if subjects[0].Name != "eicar.txt" || subjects[0].SourceFile != sourceFile {
		t.Fatalf("unexpected quarantine subject: %#v", subjects[0])
	}

	if err := s.recoverQuarantineSubject("eicar.txt"); err != nil {
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
	if err := os.WriteFile(deleteTarget+".rec", []byte("\"/scan/delete-me.txt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.deleteQuarantineSubject("delete-me.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(deleteTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected quarantine file to be deleted, err=%v", err)
	}
	if _, err := os.Stat(deleteTarget + ".rec"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected rec file to be deleted with quarantine file, err=%v", err)
	}
}

func TestQuarantineRecoverFallsBackAcrossDevices(t *testing.T) {
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
	if err := os.WriteFile(quarantined+".rec", []byte("\""+sourceFile+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldRename := renameQuarantineFile
	renameQuarantineFile = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	t.Cleanup(func() {
		renameQuarantineFile = oldRename
	})

	s := &server{cfg: config{QuarantineDir: quarantineDir}}
	if err := s.recoverQuarantineSubject("cross-device.txt"); err != nil {
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

func TestQuarantineNameFromPathRejectsTraversal(t *testing.T) {
	if _, err := quarantineNameFromPath("/api/quarantine/delete/..%2Feicar.txt", "/api/quarantine/delete/"); err == nil {
		t.Fatal("expected traversal filename to be rejected")
	}
	if _, err := quarantineNameFromPath("/api/quarantine/delete/eicar.txt.rec", "/api/quarantine/delete/"); err == nil {
		t.Fatal("expected .rec filename to be rejected")
	}
}
