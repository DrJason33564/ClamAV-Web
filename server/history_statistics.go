package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxHistoryStatisticsScope = 31
	historyStatisticsTimeout  = 30 * time.Second
)

type historyStatisticsDay struct {
	Unknown int `json:"unknown"`
	Clean   int `json:"clean"`
	Found   int `json:"found"`
	Error   int `json:"error"`
}

type historyStatisticsLookup struct {
	ID        string                          `json:"lookup_id"`
	Status    string                          `json:"status"`
	Scope     int                             `json:"scope"`
	Total     int                             `json:"total"`
	Days      map[string]historyStatisticsDay `json:"-"`
	Error     string                          `json:"error,omitempty"`
	StartedAt time.Time                       `json:"started_at"`
	UpdatedAt time.Time                       `json:"updated_at"`
	User      string                          `json:"-"`
}

func (s *server) handleHistoryStatisticsStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	scope, err := parseHistoryStatisticsScope(r.URL.Query().Get("scope"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	who, _ := actorFromRequest(r)
	now := time.Now()
	lookup := &historyStatisticsLookup{
		ID:        "statistics-" + randomHex(8),
		Status:    "pending",
		Scope:     scope,
		StartedAt: now,
		UpdatedAt: now,
		User:      who.Username,
	}

	s.historyStatisticsMu.Lock()
	s.cleanupHistoryStatisticsLookupsLocked(now.Add(-15 * time.Minute))
	s.historyStatisticsLookups[lookup.ID] = lookup
	s.historyStatisticsMu.Unlock()

	go s.runHistoryStatisticsLookup(lookup.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "pending",
		"lookup_id": lookup.ID,
		"scope":     scope,
		"message":   "history statistics are loading; poll /api/results/statistics/lookups/" + lookup.ID,
	})
}

func (s *server) handleHistoryStatisticsLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/results/statistics/lookups/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}

	s.historyStatisticsMu.RLock()
	lookup, ok := s.historyStatisticsLookups[id]
	if !ok {
		s.historyStatisticsMu.RUnlock()
		http.NotFound(w, r)
		return
	}
	who, _ := actorFromRequest(r)
	if lookup.User != who.Username {
		s.historyStatisticsMu.RUnlock()
		http.NotFound(w, r)
		return
	}
	snapshot := *lookup
	if lookup.Days != nil {
		snapshot.Days = make(map[string]historyStatisticsDay, len(lookup.Days))
		for date, counts := range lookup.Days {
			snapshot.Days[date] = counts
		}
	}
	s.historyStatisticsMu.RUnlock()

	response := historyStatisticsResponse(snapshot)
	switch snapshot.Status {
	case "pending":
		writeJSON(w, http.StatusAccepted, response)
	case "failed":
		writeJSON(w, http.StatusInternalServerError, response)
	default:
		writeJSON(w, http.StatusOK, response)
	}
}

func (s *server) runHistoryStatisticsLookup(id string) {
	s.historyStatisticsMu.RLock()
	lookup, ok := s.historyStatisticsLookups[id]
	if !ok {
		s.historyStatisticsMu.RUnlock()
		return
	}
	scope, username := lookup.Scope, lookup.User
	// Keep the requested calendar window stable even if the worker starts near
	// a local-midnight boundary.
	asOf := lookup.StartedAt.In(time.Local)
	s.historyStatisticsMu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), historyStatisticsTimeout)
	defer cancel()
	days, total, err := s.readHistoryStatistics(ctx, scope, username, asOf)

	s.historyStatisticsMu.Lock()
	defer s.historyStatisticsMu.Unlock()
	lookup, ok = s.historyStatisticsLookups[id]
	if !ok {
		return
	}
	lookup.UpdatedAt = time.Now()
	if err != nil {
		lookup.Status = "failed"
		lookup.Error = err.Error()
		return
	}
	lookup.Status = "success"
	lookup.Days = days
	lookup.Total = total
}

func parseHistoryStatisticsScope(text string) (int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, errors.New("scope is required")
	}
	scope, err := strconv.Atoi(text)
	if err != nil || scope < 1 || scope > maxHistoryStatisticsScope {
		return 0, errors.New("scope must be a positive integer between 1 and 31")
	}
	return scope, nil
}

func (s *server) readHistoryStatistics(ctx context.Context, scope int, username string, now time.Time) (map[string]historyStatisticsDay, int, error) {
	location := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	firstDay := today.AddDate(0, 0, -(scope - 1))
	days := make(map[string]historyStatisticsDay, scope)
	clauses := make([]string, 0, scope)
	args := make([]any, 0, scope*4)

	for offset := 0; offset < scope; offset++ {
		dayStart := firstDay.AddDate(0, 0, offset)
		dayEnd := dayStart.AddDate(0, 0, 1)
		// The current day ends just after the current Unix second so a job
		// indexed during this request is included, while future-dated jobs are not.
		endUnix := dayEnd.Unix()
		if dayEnd.After(now) {
			endUnix = now.Unix() + 1
		}
		dateKey := dayStart.Format("20060102")
		days[dateKey] = historyStatisticsDay{}
		clauses = append(clauses, `SELECT ? AS date_key,result,COUNT(*) FROM history_jobs
WHERE user=? AND started_at>=? AND started_at<? GROUP BY result`)
		args = append(args, dateKey, username, dayStart.Unix(), endUnix)
	}

	// Each branch uses the user/started_at index and returns only grouped
	// counts. Even a busy 31-day history therefore transfers very few rows.
	rows, err := s.historyDB.QueryContext(ctx, strings.Join(clauses, " UNION ALL "), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var dateKey, result string
		var count int
		if err := rows.Scan(&dateKey, &result, &count); err != nil {
			return nil, 0, err
		}
		counts := days[dateKey]
		switch result {
		case "clean":
			counts.Clean += count
		case "found":
			counts.Found += count
		case "error":
			counts.Error += count
		default:
			counts.Unknown += count
		}
		days[dateKey] = counts
		total += count
	}
	return days, total, rows.Err()
}

func historyStatisticsResponse(lookup historyStatisticsLookup) map[string]any {
	response := map[string]any{
		"lookup_id":  lookup.ID,
		"status":     lookup.Status,
		"scope":      lookup.Scope,
		"total":      lookup.Total,
		"started_at": lookup.StartedAt,
		"updated_at": lookup.UpdatedAt,
	}
	if lookup.Error != "" {
		response["error"] = lookup.Error
	}
	// Date keys intentionally live at the response top level to match the
	// chart-oriented API contract agreed for this endpoint.
	for date, counts := range lookup.Days {
		response[date] = counts
	}
	return response
}

func (s *server) cleanupHistoryStatisticsLookupsLocked(before time.Time) {
	for id, lookup := range s.historyStatisticsLookups {
		if lookup.UpdatedAt.Before(before) {
			delete(s.historyStatisticsLookups, id)
		}
	}
}
