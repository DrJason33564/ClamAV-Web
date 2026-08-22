package main

import (
	"context"
	"sync"
	"time"
)

const (
	maxPendingLookupsPerUser  = 2
	maxRetainedLookupsPerUser = 10
	lookupExecutionTimeout    = 30 * time.Second
	lookupRetention           = 15 * time.Minute
	lookupCleanupInterval     = time.Minute
)

type lookupWorker struct {
	userID string
	cancel context.CancelFunc
	done   chan struct{}
}

// lookupRuntime owns asynchronous lookup execution so concurrency limits,
// cancellation and shutdown waiting cannot drift between lookup types.
type lookupRuntime struct {
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	stopped       bool
	pendingByUser map[string]int
	workers       map[string]lookupWorker
	wg            sync.WaitGroup
}

func newLookupRuntime(parent context.Context) *lookupRuntime {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &lookupRuntime{
		ctx:           ctx,
		cancel:        cancel,
		pendingByUser: make(map[string]int),
		workers:       make(map[string]lookupWorker),
	}
}

func (runtime *lookupRuntime) start(id, userID string, work func(context.Context)) bool {
	runtime.mu.Lock()
	if runtime.stopped || runtime.ctx.Err() != nil || runtime.pendingByUser[userID] >= maxPendingLookupsPerUser {
		runtime.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithTimeout(runtime.ctx, lookupExecutionTimeout)
	done := make(chan struct{})
	runtime.pendingByUser[userID]++
	runtime.workers[id] = lookupWorker{userID: userID, cancel: cancel, done: done}
	runtime.wg.Add(1)
	runtime.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			runtime.mu.Lock()
			delete(runtime.workers, id)
			runtime.pendingByUser[userID]--
			if runtime.pendingByUser[userID] == 0 {
				delete(runtime.pendingByUser, userID)
			}
			runtime.mu.Unlock()
			close(done)
			runtime.wg.Done()
		}()
		work(ctx)
	}()
	return true
}

func (runtime *lookupRuntime) canStart(userID string) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return !runtime.stopped && runtime.ctx.Err() == nil && runtime.pendingByUser[userID] < maxPendingLookupsPerUser
}

func (runtime *lookupRuntime) cancelLookup(id string) {
	runtime.mu.Lock()
	worker, ok := runtime.workers[id]
	runtime.mu.Unlock()
	if ok {
		worker.cancel()
	}
}

func (runtime *lookupRuntime) cancelUserAndWait(userID string) {
	runtime.mu.Lock()
	workers := make([]lookupWorker, 0, runtime.pendingByUser[userID])
	for _, worker := range runtime.workers {
		if worker.userID == userID {
			workers = append(workers, worker)
		}
	}
	runtime.mu.Unlock()
	for _, worker := range workers {
		worker.cancel()
	}
	for _, worker := range workers {
		<-worker.done
	}
}

func (runtime *lookupRuntime) stopAndWait() {
	runtime.mu.Lock()
	runtime.stopped = true
	runtime.mu.Unlock()
	runtime.cancel()
	runtime.wg.Wait()
}

type lookupKind uint8

const (
	resultLookupKind lookupKind = iota
	historyStatisticsLookupKind
	quarantineLookupKind
)

type lookupRecord struct {
	id        string
	kind      lookupKind
	status    string
	userID    string
	startedAt time.Time
	updatedAt time.Time
}

func (s *server) startLookup(userID, id string, insert, remove func(), work func(context.Context)) bool {
	s.lookupLifecycleMu.Lock()
	defer s.lookupLifecycleMu.Unlock()

	s.cleanupLookupsLocked(time.Now())
	if s.lookupRuntime == nil || !s.lookupRuntime.canStart(userID) {
		return false
	}
	if !s.makeLookupRoomLocked(userID) {
		return false
	}
	insert()
	if !s.lookupRuntime.start(id, userID, work) {
		remove()
		return false
	}
	return true
}

func (s *server) runLookupJanitor(ctx context.Context) {
	ticker := time.NewTicker(lookupCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.cleanupLookups(now)
		}
	}
}

func (s *server) cleanupLookups(now time.Time) {
	s.lookupLifecycleMu.Lock()
	defer s.lookupLifecycleMu.Unlock()
	s.cleanupLookupsLocked(now)
}

func (s *server) cleanupLookupsLocked(now time.Time) {
	for _, record := range s.lookupRecordsLocked("") {
		// Allow a timed-out worker one janitor interval to publish its failed
		// state before treating a still-pending entry as abandoned.
		remove := record.status == "pending" && !record.startedAt.Add(lookupExecutionTimeout+lookupCleanupInterval).After(now)
		remove = remove || (record.status != "pending" && !record.updatedAt.Add(lookupRetention).After(now))
		if remove {
			s.deleteLookupLocked(record)
			if s.lookupRuntime != nil {
				s.lookupRuntime.cancelLookup(record.id)
			}
		}
	}
}

func (s *server) makeLookupRoomLocked(userID string) bool {
	for {
		records := s.lookupRecordsLocked(userID)
		if len(records) < maxRetainedLookupsPerUser {
			return true
		}
		oldest := -1
		for index, record := range records {
			if record.status == "pending" {
				continue
			}
			if oldest == -1 || record.updatedAt.Before(records[oldest].updatedAt) {
				oldest = index
			}
		}
		if oldest == -1 {
			return false
		}
		s.deleteLookupLocked(records[oldest])
	}
}

func (s *server) lookupRecordsLocked(userID string) []lookupRecord {
	records := make([]lookupRecord, 0)
	s.resultMu.RLock()
	for _, lookup := range s.resultLookups {
		if userID == "" || lookup.UserID == userID {
			records = append(records, lookupRecord{lookup.ID, resultLookupKind, lookup.Status, lookup.UserID, lookup.StartedAt, lookup.UpdatedAt})
		}
	}
	s.resultMu.RUnlock()
	s.historyStatisticsMu.RLock()
	for _, lookup := range s.historyStatisticsLookups {
		if userID == "" || lookup.UserID == userID {
			records = append(records, lookupRecord{lookup.ID, historyStatisticsLookupKind, lookup.Status, lookup.UserID, lookup.StartedAt, lookup.UpdatedAt})
		}
	}
	s.historyStatisticsMu.RUnlock()
	s.quarantineMu.RLock()
	for _, lookup := range s.quarantineLookups {
		if userID == "" || lookup.UserID == userID {
			records = append(records, lookupRecord{lookup.ID, quarantineLookupKind, lookup.Status, lookup.UserID, lookup.StartedAt, lookup.UpdatedAt})
		}
	}
	s.quarantineMu.RUnlock()
	return records
}

func (s *server) deleteLookupLocked(record lookupRecord) {
	switch record.kind {
	case resultLookupKind:
		s.resultMu.Lock()
		delete(s.resultLookups, record.id)
		s.resultMu.Unlock()
	case historyStatisticsLookupKind:
		s.historyStatisticsMu.Lock()
		delete(s.historyStatisticsLookups, record.id)
		s.historyStatisticsMu.Unlock()
	case quarantineLookupKind:
		s.quarantineMu.Lock()
		delete(s.quarantineLookups, record.id)
		s.quarantineMu.Unlock()
	}
}

func (s *server) removeLookupsForUser(userID string) {
	s.lookupLifecycleMu.Lock()
	defer s.lookupLifecycleMu.Unlock()
	if s.lookupRuntime != nil {
		s.lookupRuntime.cancelUserAndWait(userID)
	}
	for _, record := range s.lookupRecordsLocked(userID) {
		s.deleteLookupLocked(record)
	}
}
