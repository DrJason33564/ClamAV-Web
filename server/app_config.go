package main

import (
	"bufio"
	"errors"
	"fmt"
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
)

type appConfig struct {
	HistoryIndexRefreshInterval int `json:"history_index_refresh_interval"`
	WebFirstRunCompleted        int `json:"web_firstrun_completed"`
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
	return appConfig{HistoryIndexRefreshInterval: defaultHistoryIndexRefreshInterval}
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
		if !ok || key == "" || value == "" {
			return appConfig{}, fmt.Errorf("invalid clamavweb config at line %d", lineNo)
		}
		if seen[key] {
			return appConfig{}, fmt.Errorf("duplicate clamavweb config key %q", key)
		}
		seen[key] = true
		n, err := strconv.Atoi(value)
		if err != nil {
			return appConfig{}, fmt.Errorf("invalid value for %s: %w", key, err)
		}
		switch key {
		case "HISTORY_INDEX_REFRESH_INTERVAL":
			cfg.HistoryIndexRefreshInterval = n
		case "WEB_FIRSTRUN_COMPLETED":
			cfg.WebFirstRunCompleted = n
		default:
			return appConfig{}, fmt.Errorf("unsupported clamavweb config key %q", key)
		}
	}
	if err := scanner.Err(); err != nil {
		return appConfig{}, err
	}
	return cfg, validateAppConfig(cfg)
}

func validateAppConfig(cfg appConfig) error {
	if cfg.HistoryIndexRefreshInterval < minHistoryIndexRefreshInterval || cfg.HistoryIndexRefreshInterval > maxHistoryIndexRefreshInterval {
		return fmt.Errorf("HISTORY_INDEX_REFRESH_INTERVAL must be between %d and %d", minHistoryIndexRefreshInterval, maxHistoryIndexRefreshInterval)
	}
	if cfg.WebFirstRunCompleted != 0 && cfg.WebFirstRunCompleted != 2 {
		return errors.New("WEB_FIRSTRUN_COMPLETED must be 0 or 2")
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
	content := fmt.Sprintf("HISTORY_INDEX_REFRESH_INTERVAL=%d\nWEB_FIRSTRUN_COMPLETED=%d\n", cfg.HistoryIndexRefreshInterval, cfg.WebFirstRunCompleted)
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
