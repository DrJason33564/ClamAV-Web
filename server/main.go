package main

import (
	"log"
	"net/http"
	"sync"
)

type server struct {
	cfg               config
	mu                sync.RWMutex
	activeBatchID     string
	queuedBatchIDs    []string
	queueRunnerActive bool
	batches           map[string]*scanBatch
	resultMu          sync.RWMutex
	resultLookups     map[string]*resultLookup
	quarantineMu      sync.RWMutex
	quarantineLookups map[string]*quarantineLookup
}

func main() {
	cfg := loadConfig()
	s := &server{
		cfg:               cfg,
		batches:           make(map[string]*scanBatch),
		resultLookups:     make(map[string]*resultLookup),
		quarantineLookups: make(map[string]*quarantineLookup),
	}

	mux := http.NewServeMux()
	mux.Handle("/", frontendHandler())
	mux.HandleFunc("/api/status", s.handleStatus)
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
	mux.HandleFunc("/api/results/detection", s.handleDetectionResult)
	mux.HandleFunc("/api/results/clean", s.handleResultsClean)
	mux.HandleFunc("/api/quarantine/lookups", s.handleQuarantineLookupStart)
	mux.HandleFunc("/api/quarantine/lookups/", s.handleQuarantineLookup)
	mux.HandleFunc("/api/quarantine/delete/", s.handleQuarantineDelete)
	mux.HandleFunc("/api/quarantine/recover/", s.handleQuarantineRecover)
	mux.HandleFunc("/api/quarantine/clean", s.handleQuarantineClean)

	log.Printf("clamav scanner web service listening on %s", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, logRequests(s.requireAuth(mux))); err != nil {
		log.Fatal(err)
	}
}
