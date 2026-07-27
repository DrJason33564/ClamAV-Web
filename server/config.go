package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type config struct {
	Addr           string
	StatusFile     string
	JobsDir        string
	ScanLockDir    string
	LogDir         string
	QuarantineDir  string
	ScanScript     string
	CronConfigFile string
	CronScript     string
	ExcludeConfig  string
	ExcludeScript  string
	ClamdConf      string
	BrowseRoots    []string
	MaxLogBytes    int64
	CommandTimout  time.Duration
	Accounts       []account
}

type account struct {
	Username string
	Password string
}

func loadConfig() config {
	accounts, err := parseAccounts(os.Getenv("SCANNER_ACCOUNTS"))
	if err != nil {
		log.Fatalf("invalid SCANNER_ACCOUNTS: %v", err)
	}
	if len(accounts) == 0 {
		log.Fatal("SCANNER_ACCOUNTS is required; set it to username:password pairs separated by comma, semicolon, or newline")
	}
	statusDir := env("STATUS_DIR", "/state")
	return config{
		Addr:           env("SCANNER_ADDR", ":8080"),
		StatusFile:     env("STATUS_FILE", filepath.Join(statusDir, "status.json")),
		JobsDir:        env("JOBS_DIR", filepath.Join(statusDir, "jobs")),
		ScanLockDir:    env("SCAN_LOCK_DIR", filepath.Join(statusDir, "scan.lock")),
		LogDir:         env("SCAN_LOG_DIR", "/log"),
		QuarantineDir:  env("QUARANTINE_DIR", "/quarantine"),
		ScanScript:     env("SCAN_ONCE_SCRIPT", "/scan_once.sh"),
		CronConfigFile: env("CRON_CONFIG_FILE", "/config/cron_scan.conf"),
		CronScript:     env("CRON_SCRIPT", "/cron.sh"),
		ExcludeConfig:  env("EXCLUDE_CONFIG_FILE", "/config/exclude.conf"),
		ExcludeScript:  env("EXCLUDE_SCRIPT", "/exclude.sh"),
		ClamdConf:      env("CLAMD_CONF", "/etc/clamav/clamd.conf"),
		BrowseRoots:    []string{"/scan"},
		MaxLogBytes:    64 * 1024,
		CommandTimout:  3 * time.Second,
		Accounts:       accounts,
	}
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func parseAccounts(value string) ([]account, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}

	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	accounts := make([]account, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		username, password, ok := strings.Cut(field, ":")
		username = strings.TrimSpace(username)
		if !ok || username == "" || password == "" {
			return nil, fmt.Errorf("account %q must use username:password with non-empty values", field)
		}
		if seen[username] {
			return nil, fmt.Errorf("duplicate username %q", username)
		}
		seen[username] = true
		accounts = append(accounts, account{
			Username: username,
			Password: password,
		})
	}
	return accounts, nil
}
