package main

import (
	"errors"
	"net/http"
)

func (s *server) handleFirstRunStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	status := "not_completed"
	if s.appConfig.get().WebFirstRunCompleted == 2 {
		status = "completed"
	}
	writeJSON(w, http.StatusOK, map[string]string{"first_run": status})
}

func (s *server) handleFirstRunComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, err := requireAdminActor(r); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	var count int
	if err := s.userDB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users WHERE role='admin' AND status='active'").Scan(&count); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if count == 0 {
		writeError(w, http.StatusConflict, errors.New("an active administrator is required"))
		return
	}
	cfg := s.appConfig.get()
	cfg.WebFirstRunCompleted = 2
	if err := s.appConfig.update(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "success", "first_run": "completed"})
}

func (s *server) handleServiceConfig(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.appConfig.get())
	case http.MethodPatch, http.MethodPut:
		var req struct {
			HistoryIndexRefreshInterval *int `json:"history_index_refresh_interval"`
		}
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cfg := s.appConfig.get()
		if req.HistoryIndexRefreshInterval != nil {
			cfg.HistoryIndexRefreshInterval = *req.HistoryIndexRefreshInterval
		}
		if err := s.appConfig.update(cfg); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	default:
		methodNotAllowed(w)
	}
}
