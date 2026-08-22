package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCronScriptRendersWakeFlag(t *testing.T) {
	tmp := t.TempDir()
	configFile := filepath.Join(tmp, "cron_scan.conf")
	cronFile := filepath.Join(tmp, "clamav-scheduled-scan")
	rules := strings.Join([]string{
		`15 2 * * 0 "/scan/My Folder" remove admin001 Y`,
		`30 3 * * * /scan warn alice001 N`,
	}, "\n") + "\n"
	if err := os.WriteFile(configFile, []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(repositoryScript(t, "cron.sh"), "reload")
	cmd.Env = append(os.Environ(),
		"CRON_CONFIG_FILE="+configFile,
		"CRON_FILE="+cronFile,
		"SCAN_LOG_DIR="+filepath.Join(tmp, "log"),
		"STATUS_DIR="+filepath.Join(tmp, "state"),
		"CONFIG_SCRIPT="+repositoryScript(t, "config.sh"),
		"LOG_SCRIPT="+repositoryScript(t, "log.sh"),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cron reload failed: %v\n%s", err, output)
	}

	data, err := os.ReadFile(cronFile)
	if err != nil {
		t.Fatal(err)
	}
	var wakeLine, noWakeLine string
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "15 2 * * 0 root "):
			wakeLine = line
		case strings.HasPrefix(line, "30 3 * * * root "):
			noWakeLine = line
		}
	}
	if wakeLine == "" || !strings.Contains(wakeLine, " --wake") {
		t.Fatalf("expected Y rule to contain --wake, got %q", wakeLine)
	}
	if noWakeLine == "" || strings.Contains(noWakeLine, " --wake") {
		t.Fatalf("expected N rule not to contain --wake, got %q", noWakeLine)
	}
}

func TestCronConfigRejectsInvalidWakeFlag(t *testing.T) {
	tmp := t.TempDir()
	configFile := filepath.Join(tmp, "cron_scan.conf")
	if err := os.WriteFile(configFile, []byte("0 1 * * * /scan warn alice001 X\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(repositoryScript(t, "cron.sh"), "validate", configFile)
	cmd.Env = append(os.Environ(),
		"CONFIG_SCRIPT="+repositoryScript(t, "config.sh"),
		"LOG_SCRIPT="+repositoryScript(t, "log.sh"),
		"SCAN_LOG_DIR="+filepath.Join(tmp, "log"),
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected invalid wake flag to fail validation, output=%s", output)
	}
	if !strings.Contains(string(output), "Invalid crontab rule at: wake") {
		t.Fatalf("unexpected validation error: %s", output)
	}
}
