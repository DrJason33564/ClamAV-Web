package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
			if result.job["version"] != float64(3) || result.job["user_id"] != testAliceUserID {
				t.Fatalf("expected version 3 job owned by %s, got %#v", testAliceUserID, result.job)
			}
			if _, ok := result.job["started_at"].(float64); !ok {
				t.Fatalf("started_at must be a Unix timestamp number, got %#v", result.job["started_at"])
			}
			for _, field := range []string{
				"version", "job_id", "type", "user_id", "status", "target", "action", "pid",
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
	cmd := exec.Command(script, "--type", "manual", "-u", testAliceUserID, "--target", "/missing", "--action", "warn", "--wake")
	cmd.Env = append(os.Environ(), "LOG_SCRIPT="+repositoryScript(t, "log.sh"))
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("expected manual --wake to exit 2, got %v", err)
	}
}

func TestMoveActionAllocatesNamesAcrossScansAndOwners(t *testing.T) {
	fixture := newMoveScanFixture(t, "")
	first := filepath.Join(fixture.scanDir, "alice", "same.dat")
	second := filepath.Join(fixture.scanDir, "bob", "same.dat")
	reserved := filepath.Join(fixture.scanDir, "alice", "report.rec")
	for path, body := range map[string]string{first: "alice", second: "bob", reserved: "reserved"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	for _, scan := range []struct {
		owner, source, quarantined string
	}{
		{testAliceUserID, first, "same.dat"},
		{testBobUserID, second, "same.dat.001"},
		{testAliceUserID, reserved, "report.rec.001"},
	} {
		result := fixture.run(t, scan.owner, []string{scan.source})
		if result.exitCode != 0 || result.job["status"] != "finished" || result.job["result"] != "found" {
			t.Fatalf("unexpected successful move result: exit=%d job=%#v log=%s", result.exitCode, result.job, result.log)
		}
		quarantined := filepath.Join(fixture.quarantineDir, scan.quarantined)
		if _, err := os.Stat(quarantined); err != nil {
			t.Fatalf("expected quarantined file %s: %v", quarantined, err)
		}
		source, owner, err := readQuarantineRecord(quarantined + ".rec")
		if err != nil || source != scan.source || owner != scan.owner {
			t.Fatalf("unexpected quarantine record for %s: source=%q owner=%q err=%v", scan.quarantined, source, owner, err)
		}
	}

	argsData, err := os.ReadFile(fixture.argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argsData), "--move=") {
		t.Fatalf("move action must not be forwarded to clamdscan: %s", argsData)
	}
}

func TestMoveActionContinuesAfterFileFailure(t *testing.T) {
	fixture := newMoveScanFixture(t, "")
	missing := filepath.Join(fixture.scanDir, "missing.dat")
	good := filepath.Join(fixture.scanDir, "good.dat")
	if err := os.WriteFile(good, []byte("good"), 0o640); err != nil {
		t.Fatal(err)
	}

	result := fixture.run(t, testAliceUserID, []string{missing, good})
	if result.exitCode != 74 {
		t.Fatalf("expected action failure exit 74, got %d; log=%s", result.exitCode, result.log)
	}
	if result.job["status"] != "failed" || result.job["result"] != "error" || result.job["message"] != "Threats were detected, but one or more files failed to move to quarantine." {
		t.Fatalf("unexpected failed job state: %#v", result.job)
	}
	if result.job["detection_log"] == nil {
		t.Fatalf("failed move must retain its detection log: %#v", result.job)
	}
	for _, fragment := range []string{
		"Quarantine failed: source file no longer exists: " + missing,
		"Quarantine succeeded: source=" + good,
		"Quarantine summary: detected=2 succeeded=1 failed=1",
	} {
		if !strings.Contains(result.log, fragment) {
			t.Fatalf("ordinary job log is missing %q:\n%s", fragment, result.log)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.quarantineDir, "good.dat")); err != nil {
		t.Fatalf("later detections must still be quarantined: %v", err)
	}
}

func TestMoveActionFallsBackAndCleansFailedCopies(t *testing.T) {
	for _, test := range []struct {
		name       string
		cpMode     string
		wantMethod string
	}{
		{name: "copy after hard-link failure", cpMode: "copy", wantMethod: "copy"},
		{name: "mv after both copy methods fail", cpMode: "mv", wantMethod: "mv"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMoveScanFixture(t, test.cpMode)
			source := filepath.Join(fixture.scanDir, "fallback.dat")
			if err := os.WriteFile(source, []byte("complete-content"), 0o640); err != nil {
				t.Fatal(err)
			}

			result := fixture.run(t, testAliceUserID, []string{source})
			if result.exitCode != 0 {
				t.Fatalf("fallback scan failed: exit=%d job=%#v log=%s", result.exitCode, result.job, result.log)
			}
			if !strings.Contains(result.log, "method="+test.wantMethod) {
				t.Fatalf("expected %s fallback in log:\n%s", test.wantMethod, result.log)
			}
			data, err := os.ReadFile(filepath.Join(fixture.quarantineDir, "fallback.dat"))
			if err != nil || string(data) != "complete-content" {
				t.Fatalf("fallback left incomplete target: data=%q err=%v", data, err)
			}
		})
	}
}

func TestMoveActionStopsWhenFailedCopyCannotBeCleaned(t *testing.T) {
	fixture := newMoveScanFixture(t, "mv")
	source := filepath.Join(fixture.scanDir, "cleanup-failure.dat")
	if err := os.WriteFile(source, []byte("source-content"), 0o640); err != nil {
		t.Fatal(err)
	}
	realRM, err := exec.LookPath("rm")
	if err != nil {
		t.Fatal(err)
	}
	rmWrapper := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
    case "$arg" in
        */cleanup-failure.dat)
            echo "simulated target cleanup failure" >&2
            exit 1
            ;;
    esac
done
exec %q "$@"
`, realRM)
	if err := os.WriteFile(filepath.Join(fixture.binDir, "rm"), []byte(rmWrapper), 0o755); err != nil {
		t.Fatal(err)
	}

	result := fixture.run(t, testAliceUserID, []string{source})
	if result.exitCode != 74 || result.job["status"] != "failed" {
		t.Fatalf("expected immediate cleanup failure: exit=%d job=%#v log=%s", result.exitCode, result.job, result.log)
	}
	if !strings.Contains(result.log, "Failed to clean incomplete quarantine target") {
		t.Fatalf("cleanup failure is missing from ordinary log:\n%s", result.log)
	}
	if strings.Contains(result.log, "Copy move failed; falling back to mv") {
		t.Fatalf("movement continued after target cleanup failed:\n%s", result.log)
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != "source-content" {
		t.Fatalf("source changed after cleanup failure: data=%q err=%v", data, err)
	}
}

func TestMoveActionRollsBackWhenRecordPublishFails(t *testing.T) {
	fixture := newMoveScanFixture(t, "")
	source := filepath.Join(fixture.scanDir, "record-failure.dat")
	if err := os.WriteFile(source, []byte("source-content"), 0o640); err != nil {
		t.Fatal(err)
	}
	realMV, err := exec.LookPath("mv")
	if err != nil {
		t.Fatal(err)
	}
	mvWrapper := fmt.Sprintf(`#!/bin/sh
case "$1" in
    */.quarantine-rec.*)
        echo "simulated metadata publish failure" >&2
        exit 1
        ;;
esac
exec %q "$@"
`, realMV)
	if err := os.WriteFile(filepath.Join(fixture.binDir, "mv"), []byte(mvWrapper), 0o755); err != nil {
		t.Fatal(err)
	}

	result := fixture.run(t, testAliceUserID, []string{source})
	if result.exitCode != 74 || result.job["status"] != "failed" || result.job["result"] != "error" {
		t.Fatalf("expected metadata publish failure: exit=%d job=%#v log=%s", result.exitCode, result.job, result.log)
	}
	for _, fragment := range []string{"Failed to publish quarantine metadata", "Quarantine move rolled back after metadata failure"} {
		if !strings.Contains(result.log, fragment) {
			t.Fatalf("ordinary log is missing %q:\n%s", fragment, result.log)
		}
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != "source-content" {
		t.Fatalf("source was not restored after metadata failure: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.quarantineDir, "record-failure.dat")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantine data must be rolled back, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.quarantineDir, "record-failure.dat.rec")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("record reservation must be removed, err=%v", err)
	}
}

func TestMoveActionRejectsSuffixWhenRecordNameExceedsNameMax(t *testing.T) {
	fixture := newMoveScanFixture(t, "")
	longName := strings.Repeat("a", 248)
	existing := filepath.Join(fixture.quarantineDir, longName)
	if err := os.WriteFile(existing, []byte("existing"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing+".rec", []byte(`"/scan/existing" alice001`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(fixture.scanDir, longName)
	if err := os.WriteFile(source, []byte("new"), 0o640); err != nil {
		t.Fatal(err)
	}

	result := fixture.run(t, testAliceUserID, []string{source})
	if result.exitCode != 74 || result.job["status"] != "failed" || result.job["result"] != "error" {
		t.Fatalf("expected filename length failure: exit=%d job=%#v log=%s", result.exitCode, result.job, result.log)
	}
	if !strings.Contains(result.log, "target filename exceeds NAME_MAX") {
		t.Fatalf("missing filename length failure in ordinary log:\n%s", result.log)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source must remain in place after preflight failure: %v", err)
	}
}

type moveScanFixture struct {
	root          string
	binDir        string
	scanDir       string
	quarantineDir string
	statusDir     string
	logDir        string
	detections    string
	argsLog       string
	cpMode        string
}

type moveScanResult struct {
	exitCode int
	job      map[string]any
	log      string
}

func newMoveScanFixture(t *testing.T, cpMode string) moveScanFixture {
	t.Helper()
	root := t.TempDir()
	fixture := moveScanFixture{
		root:          root,
		binDir:        filepath.Join(root, "bin"),
		scanDir:       filepath.Join(root, "scan"),
		quarantineDir: filepath.Join(root, "quarantine"),
		statusDir:     filepath.Join(root, "state"),
		logDir:        filepath.Join(root, "log"),
		detections:    filepath.Join(root, "detections.txt"),
		argsLog:       filepath.Join(root, "clamdscan-args.txt"),
		cpMode:        cpMode,
	}
	for _, dir := range []string{fixture.binDir, fixture.scanDir, fixture.quarantineDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	clamdscan := `#!/bin/sh
printf '%s\n' "$*" >> "$CLAMDSCAN_ARGS_LOG"
while IFS= read -r detected || [ -n "$detected" ]; do
    printf '%s: Eicar-Test-Signature FOUND\n' "$detected"
done < "$FAKE_DETECTIONS_FILE"
exit 1
`
	if err := os.WriteFile(filepath.Join(fixture.binDir, "clamdscan"), []byte(clamdscan), 0o755); err != nil {
		t.Fatal(err)
	}

	if cpMode != "" {
		realCP, err := exec.LookPath("cp")
		if err != nil {
			t.Fatal(err)
		}
		cpWrapper := fmt.Sprintf(`#!/bin/sh
target="$3"
if [ -e "$target" ] || [ -L "$target" ]; then
    echo "target was not cleaned before fallback: $target" >&2
    exit 90
fi
printf 'incomplete' > "$target"
if [ "$1" = "-l" ]; then
    echo "simulated hard-link failure" >&2
    exit 1
fi
if [ "$FAKE_CP_MODE" = "mv" ]; then
    echo "simulated copy failure" >&2
    exit 1
fi
rm -f "$target"
exec %q "$@"
`, realCP)
		if err := os.WriteFile(filepath.Join(fixture.binDir, "cp"), []byte(cpWrapper), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func (f moveScanFixture) run(t *testing.T, owner string, detections []string) moveScanResult {
	t.Helper()
	if err := os.WriteFile(f.detections, []byte(strings.Join(detections, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(repositoryScript(t, "scan_once.sh"),
		"--type", "manual", "--user", owner, "--target", f.scanDir, "--action", "move")
	cmd.Env = append(os.Environ(),
		"PATH="+f.binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SCAN_LOG_DIR="+f.logDir,
		"STATUS_DIR="+f.statusDir,
		"JOBS_DIR="+filepath.Join(f.statusDir, "jobs"),
		"SCAN_LOCK_DIR="+filepath.Join(f.statusDir, "scan.lock"),
		"SLEEP_LOCK_DIR="+filepath.Join(f.statusDir, "sleep.lock"),
		"QUARANTINE_DIR="+f.quarantineDir,
		"LOG_SCRIPT="+repositoryScript(t, "log.sh"),
		"FAKE_DETECTIONS_FILE="+f.detections,
		"CLAMDSCAN_ARGS_LOG="+f.argsLog,
		"FAKE_CP_MODE="+f.cpMode,
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
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "JOB_ID=") {
		t.Fatalf("missing job id in output %q", output)
	}
	jobID := strings.TrimPrefix(lines[0], "JOB_ID=")
	jobData, err := os.ReadFile(filepath.Join(f.statusDir, "jobs", jobID+".json"))
	if err != nil {
		t.Fatalf("read job state: %v output=%s", err, output)
	}
	var job map[string]any
	if err := json.Unmarshal(jobData, &job); err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(filepath.Join(f.logDir, jobID+".log"))
	if err != nil {
		t.Fatal(err)
	}
	return moveScanResult{exitCode: exitCode, job: job, log: string(logData)}
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
	args := []string{"--type", "cron", "-u", testAliceUserID, "--target", missingTarget, "--action", "warn"}
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
