package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type config struct {
	Addr                string
	StatusFile          string
	JobsDir             string
	ScanLockDir         string
	SleepLockDir        string
	LogDir              string
	QuarantineDir       string
	ScanScript          string
	StartupScript       string
	CronConfigFile      string
	CronScript          string
	ExcludeConfig       string
	ExcludeScript       string
	ClamdConf           string
	ClamdSocket         string
	BrowseRoots         []string
	MaxLogBytes         int64
	CommandTimout       time.Duration
	WakeTimeout         time.Duration
	DataDir             string
	UserDatabaseFile    string
	HistoryDatabaseFile string
	AppConfigFile       string
	IsTimeDock          bool
}

func loadConfig() (config, error) {
	statusDir := env("STATUS_DIR", "/state")
	dataDir := env("DATA_DIR", "/data")
	isTimeDock, err := parseYNFlag("IS_TIMEDOCK", os.Getenv("IS_TIMEDOCK"))
	if err != nil {
		return config{}, err
	}
	return config{
		Addr:           env("SCANNER_ADDR", ":8080"),
		StatusFile:     env("STATUS_FILE", filepath.Join(statusDir, "status.json")),
		JobsDir:        env("JOBS_DIR", filepath.Join(statusDir, "jobs")),
		ScanLockDir:    env("SCAN_LOCK_DIR", filepath.Join(statusDir, "scan.lock")),
		SleepLockDir:   env("SLEEP_LOCK_DIR", filepath.Join(statusDir, "sleep.lock")),
		LogDir:         env("SCAN_LOG_DIR", "/log"),
		QuarantineDir:  env("QUARANTINE_DIR", "/quarantine"),
		ScanScript:     env("SCAN_ONCE_SCRIPT", "/scan_once.sh"),
		StartupScript:  env("STARTUP_SCRIPT", "/startup.sh"),
		CronConfigFile: env("CRON_CONFIG_FILE", "/config/cron_scan.conf"),
		CronScript:     env("CRON_SCRIPT", "/cron.sh"),
		ExcludeConfig:  env("EXCLUDE_CONFIG_FILE", "/config/exclude.conf"),
		ExcludeScript:  env("EXCLUDE_SCRIPT", "/exclude.sh"),
		ClamdConf:      env("CLAMD_CONF", "/etc/clamav/clamd.conf"),
		ClamdSocket:    env("CLAMD_SOCKET", "/tmp/clamd.sock"),
		BrowseRoots:    []string{"/scan"},
		MaxLogBytes:    64 * 1024,
		CommandTimout:  3 * time.Second,
		// startup.sh waits up to 20 minutes (240 attempts * 5 seconds).
		// Keep the caller alive slightly longer so the script owns the timeout
		// and can terminate a partially started /init process itself.
		WakeTimeout:         21 * time.Minute,
		DataDir:             dataDir,
		UserDatabaseFile:    env("USER_DATABASE_FILE", filepath.Join(dataDir, "users.db")),
		HistoryDatabaseFile: env("HISTORY_DATABASE_FILE", filepath.Join(dataDir, "history.db")),
		AppConfigFile:       env("CLAMAVWEB_CONFIG_FILE", "/config/clamavweb.conf"),
		IsTimeDock:          isTimeDock,
	}, nil
}

func parseYNFlag(name, value string) (bool, error) {
	switch strings.TrimSpace(value) {
	case "", "N":
		return false, nil
	case "Y":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be Y or N", name)
	}
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
