package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	defaultHistoryIndexRefreshInterval = 60
	minHistoryIndexRefreshInterval     = 5
	maxHistoryIndexRefreshInterval     = 86400
	defaultWebLoginMaxTries            = 10
	defaultWebLoginMaxTriesOverall     = 100
	defaultWebLoginCooldownInterval    = 600
	maxWebLoginMaxTries                = 10000
	maxWebLoginMaxTriesOverall         = 1000000
	maxWebLoginCooldownInterval        = 86400
)

type appConfig struct {
	HistoryIndexRefreshInterval int    `json:"history_index_refresh_interval"`
	WebFirstRunCompleted        int    `json:"web_firstrun_completed"`
	WebLoginMaxTries            int    `json:"web_login_max_tries"`
	WebLoginMaxTriesOverall     int    `json:"web_login_max_tries_overall"`
	WebLoginCooldownInterval    int    `json:"web_login_cooldown_interval"`
	ServerTrustedReverseProxy   string `json:"server_trusted_reverseproxy"`
}

type appConfigStore struct {
	path string
	mu   sync.RWMutex
	cfg  appConfig
}

func newAppConfigStore(path string) (*appConfigStore, error) {
	store := &appConfigStore{path: path}
	if err := store.ensureAndReload(); err != nil {
		return nil, err
	}
	return store, nil
}

func defaultAppConfig() appConfig {
	return appConfig{
		HistoryIndexRefreshInterval: defaultHistoryIndexRefreshInterval,
		WebLoginMaxTries:            defaultWebLoginMaxTries,
		WebLoginMaxTriesOverall:     defaultWebLoginMaxTriesOverall,
		WebLoginCooldownInterval:    defaultWebLoginCooldownInterval,
	}
}

func (s *appConfigStore) get() appConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *appConfigStore) update(next appConfig) error {
	if err := validateAppConfig(next); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeAppConfigFile(s.path, next); err != nil {
		return err
	}
	s.cfg = next
	return nil
}

func (s *appConfigStore) ensureAndReload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := defaultAppConfig()
		if err := writeAppConfigFile(s.path, cfg); err != nil {
			return err
		}
		s.cfg = cfg
		return nil
	}
	if err != nil {
		return err
	}
	cfg, err := parseAppConfig(string(data))
	if err != nil {
		return err
	}
	s.cfg = cfg
	return nil
}

func parseAppConfig(content string) (appConfig, error) {
	cfg := defaultAppConfig()
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(content))
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" {
			return appConfig{}, fmt.Errorf("invalid clamavweb config at line %d", lineNo)
		}
		if seen[key] {
			return appConfig{}, fmt.Errorf("duplicate clamavweb config key %q", key)
		}
		seen[key] = true
		switch key {
		case "HISTORY_INDEX_REFRESH_INTERVAL":
			if err := parseAppConfigInt(key, value, &cfg.HistoryIndexRefreshInterval); err != nil {
				return appConfig{}, err
			}
		case "WEB_FIRSTRUN_COMPLETED":
			if err := parseAppConfigInt(key, value, &cfg.WebFirstRunCompleted); err != nil {
				return appConfig{}, err
			}
		case "WEB_LOGIN_MAX_TRIES":
			if err := parseAppConfigInt(key, value, &cfg.WebLoginMaxTries); err != nil {
				return appConfig{}, err
			}
		case "WEB_LOGIN_MAX_TRIES_OVERALL":
			if err := parseAppConfigInt(key, value, &cfg.WebLoginMaxTriesOverall); err != nil {
				return appConfig{}, err
			}
		case "WEB_LOGIN_COOLDOWN_INTERVAL":
			if err := parseAppConfigInt(key, value, &cfg.WebLoginCooldownInterval); err != nil {
				return appConfig{}, err
			}
		case "SERVER_TRUSTED_REVERSEPROXY":
			cfg.ServerTrustedReverseProxy = value
		default:
			return appConfig{}, fmt.Errorf("unsupported clamavweb config key %q", key)
		}
	}
	if err := scanner.Err(); err != nil {
		return appConfig{}, err
	}
	return cfg, validateAppConfig(cfg)
}

func parseAppConfigInt(key, value string, target *int) error {
	if value == "" {
		return fmt.Errorf("invalid value for %s: value is empty", key)
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("invalid value for %s: %w", key, err)
	}
	*target = n
	return nil
}

func validateAppConfig(cfg appConfig) error {
	if cfg.HistoryIndexRefreshInterval < minHistoryIndexRefreshInterval || cfg.HistoryIndexRefreshInterval > maxHistoryIndexRefreshInterval {
		return fmt.Errorf("HISTORY_INDEX_REFRESH_INTERVAL must be between %d and %d", minHistoryIndexRefreshInterval, maxHistoryIndexRefreshInterval)
	}
	if cfg.WebFirstRunCompleted != 0 && cfg.WebFirstRunCompleted != 2 {
		return errors.New("WEB_FIRSTRUN_COMPLETED must be 0 or 2")
	}
	if cfg.WebLoginMaxTries < 1 || cfg.WebLoginMaxTries > maxWebLoginMaxTries {
		return fmt.Errorf("WEB_LOGIN_MAX_TRIES must be between 1 and %d", maxWebLoginMaxTries)
	}
	if cfg.WebLoginMaxTriesOverall < cfg.WebLoginMaxTries || cfg.WebLoginMaxTriesOverall > maxWebLoginMaxTriesOverall {
		return fmt.Errorf("WEB_LOGIN_MAX_TRIES_OVERALL must be between WEB_LOGIN_MAX_TRIES and %d", maxWebLoginMaxTriesOverall)
	}
	if cfg.WebLoginCooldownInterval < 1 || cfg.WebLoginCooldownInterval > maxWebLoginCooldownInterval {
		return fmt.Errorf("WEB_LOGIN_COOLDOWN_INTERVAL must be between 1 and %d seconds", maxWebLoginCooldownInterval)
	}
	if cfg.ServerTrustedReverseProxy != "" && net.ParseIP(cfg.ServerTrustedReverseProxy) == nil {
		return errors.New("SERVER_TRUSTED_REVERSEPROXY must be empty or a valid IPv4/IPv6 address")
	}
	return nil
}

func writeAppConfigFile(path string, cfg appConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".clamavweb.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	content := fmt.Sprintf("HISTORY_INDEX_REFRESH_INTERVAL=%d\nWEB_FIRSTRUN_COMPLETED=%d\nWEB_LOGIN_MAX_TRIES=%d\nWEB_LOGIN_MAX_TRIES_OVERALL=%d\nWEB_LOGIN_COOLDOWN_INTERVAL=%d\nSERVER_TRUSTED_REVERSEPROXY=%s\n", cfg.HistoryIndexRefreshInterval, cfg.WebFirstRunCompleted, cfg.WebLoginMaxTries, cfg.WebLoginMaxTriesOverall, cfg.WebLoginCooldownInterval, cfg.ServerTrustedReverseProxy)
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
