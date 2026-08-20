package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestParseHistoryStatisticsScope(t *testing.T) {
	for _, value := range []string{"1", "14", "31"} {
		if _, err := parseHistoryStatisticsScope(value); err != nil {
			t.Fatalf("expected scope %q to be valid: %v", value, err)
		}
	}
	for _, value := range []string{"", "0", "-1", "1.5", "all", "32"} {
		if _, err := parseHistoryStatisticsScope(value); err == nil {
			t.Fatalf("expected scope %q to be rejected", value)
		}
	}
}

func TestReadHistoryStatisticsUsesLocalDaysAndUser(t *testing.T) {
	db, err := openHistoryDatabase(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &server{historyDB: db}
	location := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, 8, 5, 12, 30, 0, 0, location)

	insert := func(id, userID, result string, startedAt time.Time) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO history_jobs
(job_id,job_type,status,result,action,started_at,finished_at,user_id,json_file,file_mtime_ns,indexed_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, "manual", "finished", result, "warn", startedAt.Unix(), startedAt.Unix(), userID, "/state/jobs/"+id+".json", 1, now.Unix())
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("manual-1", testAliceUserID, "clean", time.Date(2026, 8, 3, 0, 0, 0, 0, location))
	insert("manual-2", testAliceUserID, "found", time.Date(2026, 8, 3, 23, 59, 59, 0, location))
	insert("manual-3", testAliceUserID, "error", time.Date(2026, 8, 5, 9, 0, 0, 0, location))
	insert("manual-4", testAliceUserID, "unexpected", time.Date(2026, 8, 5, 10, 0, 0, 0, location))
	insert("manual-5", testBobUserID, "clean", time.Date(2026, 8, 5, 10, 0, 0, 0, location))
	insert("manual-6", testAliceUserID, "clean", time.Date(2026, 8, 2, 23, 59, 59, 0, location))
	insert("manual-7", testAliceUserID, "clean", time.Date(2026, 8, 5, 13, 0, 0, 0, location))

	days, total, err := s.readHistoryStatistics(t.Context(), 3, testAliceUserID, now)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Fatalf("expected four in-range jobs, got %d", total)
	}
	if got := days["20260803"]; got.Clean != 1 || got.Found != 1 || got.Unknown != 0 || got.Error != 0 {
		t.Fatalf("unexpected first-day counts: %#v", got)
	}
	if got := days["20260804"]; got != (historyStatisticsDay{}) {
		t.Fatalf("expected an empty day to contain zero counts, got %#v", got)
	}
	if got := days["20260805"]; got.Error != 1 || got.Unknown != 1 || got.Clean != 0 || got.Found != 0 {
		t.Fatalf("unexpected current-day counts: %#v", got)
	}
}

func TestHistoryStatisticsLookupStartsPendingAndIsUserBound(t *testing.T) {
	db, err := openHistoryDatabase(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &server{
		historyDB:                db,
		historyStatisticsLookups: make(map[string]*historyStatisticsLookup),
	}

	request := httptest.NewRequest(http.MethodPost, "/api/results/statistics/lookups?scope=2", nil)
	request = request.WithContext(context.WithValue(request.Context(), actorContextKey{}, actor{ID: testAliceUserID, Username: "alice"}))
	response := httptest.NewRecorder()
	s.handleHistoryStatisticsStart(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected lookup creation to return 202, got %d", response.Code)
	}
	var started map[string]any
	if err := json.NewDecoder(response.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	if started["status"] != "pending" || started["scope"] != float64(2) {
		t.Fatalf("unexpected start response: %#v", started)
	}

	id := started["lookup_id"].(string)
	poll := httptest.NewRequest(http.MethodGet, "/api/results/statistics/lookups/"+id, nil)
	poll = poll.WithContext(context.WithValue(poll.Context(), actorContextKey{}, actor{ID: testBobUserID, Username: "bob"}))
	pollResponse := httptest.NewRecorder()
	s.handleHistoryStatisticsLookup(pollResponse, poll)
	if pollResponse.Code != http.StatusNotFound {
		t.Fatalf("expected another user to receive 404, got %d", pollResponse.Code)
	}
	deadline := time.Now().Add(time.Second)
	for {
		s.historyStatisticsMu.RLock()
		status := s.historyStatisticsLookups[id].Status
		s.historyStatisticsMu.RUnlock()
		if status != "pending" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("statistics lookup did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHistoryStatisticsResponseUsesTopLevelDateKeys(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	response := historyStatisticsResponse(historyStatisticsLookup{
		ID: "statistics-test", Status: "success", Scope: 1, Total: 3,
		Days:      map[string]historyStatisticsDay{"20260805": {Clean: 2, Found: 1}},
		StartedAt: now, UpdatedAt: now,
	})
	if _, ok := response["20260805"]; !ok {
		t.Fatalf("expected date data at the response top level: %#v", response)
	}
	if response["scope"] != 1 || response["total"] != 3 {
		t.Fatalf("unexpected response metadata: %#v", response)
	}
}
