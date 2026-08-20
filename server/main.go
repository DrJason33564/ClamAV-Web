package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"clamav-scanner/internal/applog"
)

const (
	// Keep connection limits as fixed security boundaries rather than runtime
	// settings that can be accidentally disabled. The write deadline stays
	// above the 21-minute ClamAV wake timeout so a valid wake can still reply.
	httpReadHeaderTimeout = 10 * time.Second
	httpReadTimeout       = 30 * time.Second
	httpIdleTimeout       = 60 * time.Second
	httpWriteTimeout      = 25 * time.Minute
	httpMaxHeaderBytes    = 64 * 1024
)

type server struct {
	cfg                      config
	mu                       sync.RWMutex
	activeBatchID            string
	queuedBatchIDs           []string
	queueRunnerActive        bool
	batches                  map[string]*scanBatch
	resultMu                 sync.RWMutex
	resultLookups            map[string]*resultLookup
	historyStatisticsMu      sync.RWMutex
	historyStatisticsLookups map[string]*historyStatisticsLookup
	quarantineMu             sync.RWMutex
	quarantineLookups        map[string]*quarantineLookup
	lookupLifecycleMu        sync.Mutex
	lookupRuntime            *lookupRuntime
	clamavPowerMu            sync.Mutex
	clamdPingMu              sync.Mutex
	clamdPingCache           clamdPingCacheEntry
	configFileMu             sync.Mutex
	accountDeleteMu          sync.Mutex
	registrationMu           sync.Mutex
	argon2Limiter            argon2Limiter
	userDB                   *sql.DB
	historyDB                *sql.DB
	appConfig                *appConfigStore
	history                  *historyIndexer
	loginLimiter             *loginLimiter
	logger                   *applog.Logger
	historyIntervalChanged   chan struct{}
	clamavSleepTimer         *clamavSleepTimerState
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load environment config: %w", err)
	}
	logPath := filepath.Join(cfg.LogDir, "clamavweb.log")
	defaults := defaultAppConfig()
	logger, err := applog.New(applog.Config{
		Path:     logPath,
		MaxSize:  defaults.LogFileMaxSize,
		MaxFiles: defaults.LogFileNum,
		Level:    defaults.LogLevel,
	})
	if err != nil {
		return fmt.Errorf("initialize application logger: %w", err)
	}
	appConfig, err := newAppConfigStore(cfg.AppConfigFile)
	if err != nil {
		logger.Error("load server config failed", "module", "config", "path", cfg.AppConfigFile, "error", err)
		_ = logger.Close()
		return fmt.Errorf("load server config: %w", err)
	}
	appCfg := appConfig.get()
	if appCfg.LogFileMaxSize != defaults.LogFileMaxSize || appCfg.LogFileNum != defaults.LogFileNum || appCfg.LogLevel != defaults.LogLevel {
		configuredLogger, configErr := applog.New(applog.Config{
			Path:     logPath,
			MaxSize:  appCfg.LogFileMaxSize,
			MaxFiles: appCfg.LogFileNum,
			Level:    appCfg.LogLevel,
		})
		if configErr != nil {
			logger.Error("apply application log configuration failed", "module", "config", "error", configErr)
			_ = logger.Close()
			return fmt.Errorf("apply application log configuration: %w", configErr)
		}
		_ = logger.Close()
		logger = configuredLogger
	}
	defer logger.Close()
	logger.Info("application logger initialized", "module", "startup", "path", logPath, "max_size", appCfg.LogFileMaxSize, "max_files", appCfg.LogFileNum, "level", appCfg.LogLevel)
	userDB, err := openUserDatabase(cfg.UserDatabaseFile)
	if err != nil {
		logger.Error("open user database failed", "module", "database", "path", cfg.UserDatabaseFile, "error", err)
		return fmt.Errorf("open user database: %w", err)
	}
	defer userDB.Close()
	historyDB, err := openHistoryDatabase(cfg.HistoryDatabaseFile)
	if err != nil {
		logger.Error("open history database failed", "module", "database", "path", cfg.HistoryDatabaseFile, "error", err)
		return fmt.Errorf("open history database: %w", err)
	}
	defer historyDB.Close()
	s := &server{
		cfg:                      cfg,
		batches:                  make(map[string]*scanBatch),
		resultLookups:            make(map[string]*resultLookup),
		historyStatisticsLookups: make(map[string]*historyStatisticsLookup),
		quarantineLookups:        make(map[string]*quarantineLookup),
		userDB:                   userDB,
		historyDB:                historyDB,
		appConfig:                appConfig,
		loginLimiter:             newLoginLimiter(),
		logger:                   logger,
		historyIntervalChanged:   make(chan struct{}, 1),
		clamavSleepTimer:         newClamAVSleepTimerState(),
	}
	s.history = &historyIndexer{db: historyDB, jobsDir: cfg.JobsDir, logger: logger}
	if err := s.history.refresh(context.Background()); err != nil {
		s.error("history", "initial history index refresh failed", "error", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	s.lookupRuntime = newLookupRuntime(ctx)
	defer s.lookupRuntime.stopAndWait()
	go s.runHistoryIndexer(ctx)
	go s.runSessionJanitor(ctx)
	go s.loginLimiter.runJanitor(ctx)
	go s.runClamAVSleepTimer(ctx)
	go s.runLookupJanitor(ctx)

	mux := http.NewServeMux()
	mux.Handle("/", frontendHandler())
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/first-run/status", s.handleFirstRunStatus)
	mux.HandleFunc("/api/first-run/complete", s.handleFirstRunComplete)
	mux.HandleFunc("/api/auth/register", s.handleAuthRegister)
	mux.HandleFunc("/api/auth/login", s.handleAuthLogin)
	mux.HandleFunc("/api/auth/logout", s.handleAuthLogout)
	mux.HandleFunc("/api/auth/me", s.handleAuthMe)
	mux.HandleFunc("/api/auth/password", s.handleAuthPassword)
	mux.HandleFunc("/api/auth/account", s.handleAuthAccount)
	mux.HandleFunc("/api/admin/users", s.handleAdminUsers)
	mux.HandleFunc("/api/admin/users/", s.handleAdminUser)
	mux.HandleFunc("/api/config", s.handleServiceConfig)
	mux.Handle("/api/clamav/sleep", s.adminOnly(http.HandlerFunc(s.handleClamAVSleep)))
	mux.Handle("/api/clamav/wake", s.adminOnly(http.HandlerFunc(s.handleClamAVWake)))
	mux.HandleFunc("/api/browse", s.handleBrowse)
	mux.HandleFunc("/api/scans", s.handleScans)
	mux.HandleFunc("/api/scans/reorder", s.handleScanReorder)
	mux.HandleFunc("/api/scans/cancel", s.handleScanCancel)
	mux.HandleFunc("/api/cron/rules", s.handleCronRules)
	mux.HandleFunc("/api/cron/rules/", s.handleCronRule)
	mux.HandleFunc("/api/cron/reload", s.handleCronReload)
	mux.HandleFunc("/api/whitelist", s.handleWhitelist)
	mux.HandleFunc("/api/results/lookups", s.handleResultLookupStart)
	mux.HandleFunc("/api/results/lookups/", s.handleResultLookup)
	mux.HandleFunc("/api/results/statistics/lookups", s.handleHistoryStatisticsStart)
	mux.HandleFunc("/api/results/statistics/lookups/", s.handleHistoryStatisticsLookup)
	mux.HandleFunc("/api/results/detection", s.handleDetectionResult)
	mux.HandleFunc("/api/results/clean", s.handleResultsClean)
	mux.HandleFunc("/api/quarantine/lookups", s.handleQuarantineLookupStart)
	mux.HandleFunc("/api/quarantine/lookups/", s.handleQuarantineLookup)
	mux.HandleFunc("/api/quarantine/delete/", s.handleQuarantineDelete)
	mux.HandleFunc("/api/quarantine/recover/", s.handleQuarantineRecover)
	mux.HandleFunc("/api/quarantine/clean", s.handleQuarantineClean)

	httpServer := newHTTPServer(cfg.Addr, securityHeaders(s.logRequests(s.requireAuth(mux))))
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		s.error("startup", "http server listen failed", "address", cfg.Addr, "error", err)
		return err
	}
	// Keep the successful listen event in both the rotated application log and
	// the container's standard output for docker logs and similar collectors.
	s.info("startup", "clamav scanner web service listening", "address", cfg.Addr)
	_, _ = fmt.Fprintf(os.Stdout, "clamav scanner web service listening on %s\n", cfg.Addr)
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		s.info("startup", "shutdown signal received")
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			s.error("startup", "http server shutdown failed", "error", err)
		}
	}()
	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.error("startup", "http server stopped unexpectedly", "error", err)
		return err
	}
	s.info("startup", "clamav scanner web service stopped")
	return nil
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		IdleTimeout:       httpIdleTimeout,
		WriteTimeout:      httpWriteTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
	}
}
