package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
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
	clamavPowerMu            sync.Mutex
	configFileMu             sync.Mutex
	accountDeleteMu          sync.Mutex
	registrationMu           sync.Mutex
	userDB                   *sql.DB
	historyDB                *sql.DB
	appConfig                *appConfigStore
	history                  *historyIndexer
	loginLimiter             *loginLimiter
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("load environment config: %v", err)
	}
	appConfig, err := newAppConfigStore(cfg.AppConfigFile)
	if err != nil {
		log.Fatalf("load server config: %v", err)
	}
	userDB, err := openUserDatabase(cfg.UserDatabaseFile)
	if err != nil {
		log.Fatalf("open user database: %v", err)
	}
	defer userDB.Close()
	historyDB, err := openHistoryDatabase(cfg.HistoryDatabaseFile)
	if err != nil {
		log.Fatalf("open history database: %v", err)
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
	}
	s.history = &historyIndexer{db: historyDB, jobsDir: cfg.JobsDir}
	if err := s.history.refresh(context.Background()); err != nil {
		log.Printf("initial history index refresh failed: %v", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go s.runHistoryIndexer(ctx)
	go s.runSessionJanitor(ctx)
	go s.loginLimiter.runJanitor(ctx)

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

	log.Printf("clamav scanner web service listening on %s", cfg.Addr)
	httpServer := &http.Server{Addr: cfg.Addr, Handler: logRequests(s.requireAuth(mux))}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
