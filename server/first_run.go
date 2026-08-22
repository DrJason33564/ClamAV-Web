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
		s.error("config", "mark first run completed failed", "error", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	who, _ := actorFromRequest(r)
	s.info("config", "first run completed", "user", who.Username)
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
			HistoryIndexRefreshInterval *int    `json:"history_index_refresh_interval"`
			ClamAVSleepTimer            *int    `json:"clamav_sleep_timer"`
			WebLoginMaxTries            *int    `json:"web_login_max_tries"`
			WebLoginMaxTriesOverall     *int    `json:"web_login_max_tries_overall"`
			WebLoginCooldownInterval    *int    `json:"web_login_cooldown_interval"`
			ServerTrustedReverseProxy   *string `json:"server_trusted_reverseproxy"`
		}
		if err := decodeJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cfg := s.appConfig.get()
		oldHistoryInterval := cfg.HistoryIndexRefreshInterval
		oldClamAVSleepTimer := cfg.ClamAVSleepTimer
		oldLoginCfg := cfg
		if req.HistoryIndexRefreshInterval != nil {
			cfg.HistoryIndexRefreshInterval = *req.HistoryIndexRefreshInterval
		}
		if req.ClamAVSleepTimer != nil {
			cfg.ClamAVSleepTimer = *req.ClamAVSleepTimer
		}
		if req.WebLoginMaxTries != nil {
			cfg.WebLoginMaxTries = *req.WebLoginMaxTries
		}
		if req.WebLoginMaxTriesOverall != nil {
			cfg.WebLoginMaxTriesOverall = *req.WebLoginMaxTriesOverall
		}
		if req.WebLoginCooldownInterval != nil {
			cfg.WebLoginCooldownInterval = *req.WebLoginCooldownInterval
		}
		if req.ServerTrustedReverseProxy != nil {
			trustedProxies, err := normalizeTrustedReverseProxyList(*req.ServerTrustedReverseProxy)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			cfg.ServerTrustedReverseProxy = trustedProxies
		}
		if err := s.appConfig.update(cfg); err != nil {
			s.warn("config", "service configuration update rejected", "error", err)
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if s.loginLimiter != nil && (oldLoginCfg.WebLoginMaxTries != cfg.WebLoginMaxTries || oldLoginCfg.WebLoginMaxTriesOverall != cfg.WebLoginMaxTriesOverall || oldLoginCfg.WebLoginCooldownInterval != cfg.WebLoginCooldownInterval) {
			s.loginLimiter.reset()
			s.info("auth", "login limiter state reset after configuration change")
		}
		if oldHistoryInterval != cfg.HistoryIndexRefreshInterval {
			s.notifyHistoryIntervalChanged()
		}
		if oldClamAVSleepTimer != cfg.ClamAVSleepTimer {
			s.notifyClamAVSleepTimerChanged("configuration_updated")
		}
		who, _ := actorFromRequest(r)
		s.info("config", "service configuration updated", "user", who.Username)
		writeJSON(w, http.StatusOK, s.appConfig.get())
	default:
		methodNotAllowed(w)
	}
}
