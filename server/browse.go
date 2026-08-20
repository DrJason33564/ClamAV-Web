package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type browseEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

type browseResponse struct {
	Path    string        `json:"path"`
	Parent  string        `json:"parent,omitempty"`
	Entries []browseEntry `json:"entries"`
	Roots   []string      `json:"roots"`
}

func (s *server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	requested := r.URL.Query().Get("path")
	if requested == "" {
		requested = s.cfg.BrowseRoots[0]
	}
	who, _ := actorFromRequest(r)
	path, devices, err := s.safeBrowsePath(requested, who)
	if err != nil {
		s.warn("browse", "browse path rejected", "user", who.Username, "path", requested, "error", err)
		writeError(w, http.StatusBadRequest, err)
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if !info.IsDir() {
		path = filepath.Dir(path)
	}

	dirEntries, err := os.ReadDir(path)
	if err != nil {
		s.warn("browse", "browse directory failed", "user", who.Username, "path", path, "error", err)
		writeError(w, http.StatusForbidden, err)
		return
	}
	entries := make([]browseEntry, 0, len(dirEntries))
	for _, entry := range dirEntries {
		full := filepath.Join(path, entry.Name())
		if s.cfg.IsTimeDock {
			switch {
			case path == filepath.Clean(s.cfg.BrowseRoots[0]):
				// At /scan, expose only devices that contain this account.
				if _, ok := devices[full]; !ok {
					continue
				}
			case devices[path] != "":
				// At a device, expose only the account's first-level directory.
				if full != devices[path] {
					continue
				}
			}
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		entries = append(entries, browseEntry{
			Name:     entry.Name(),
			Path:     full,
			IsDir:    entry.IsDir(),
			Size:     info.Size(),
			Modified: info.ModTime().Format(time.RFC3339),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	s.debug("browse", "browse directory completed", "user", who.Username, "path", path, "entries", len(entries))

	parent := ""
	if parentPath := filepath.Dir(path); parentPath != path {
		if _, _, err := s.safeBrowsePath(parentPath, who); err == nil {
			parent = parentPath
		}
	}

	writeJSON(w, http.StatusOK, browseResponse{
		Path:    path,
		Parent:  parent,
		Entries: entries,
		Roots:   s.cfg.BrowseRoots,
	})
}

var errTimeDockAccountRequired = errors.New("Please set your TimeDock account")
var errTimeDockPathDenied = errors.New("path is outside the current TimeDock account")

func (s *server) safeBrowsePath(input string, who actor) (string, map[string]string, error) {
	if s.cfg.IsTimeDock && who.TimeDockAccount == "" {
		return "", nil, errTimeDockAccountRequired
	}
	path, err := s.safePath(input)
	if err != nil {
		return "", nil, err
	}
	if !s.cfg.IsTimeDock {
		return path, nil, nil
	}
	devices, err := s.timeDockDeviceAccounts(who.TimeDockAccount)
	if err != nil {
		return "", nil, err
	}
	root := filepath.Clean(s.cfg.BrowseRoots[0])
	if path == root || devices[path] != "" {
		return path, devices, nil
	}
	if err := authorizeTimeDockPath(path, devices, false); err != nil {
		return "", nil, err
	}
	return path, devices, nil
}

// safePathForActor applies the TimeDock account boundary after the ordinary
// /scan and symlink checks. It is shared by every API that accepts scan paths.
func (s *server) safePathForActor(input string, who actor) (string, error) {
	if s.cfg.IsTimeDock && who.TimeDockAccount == "" {
		return "", errTimeDockAccountRequired
	}
	path, err := s.safePath(input)
	if err != nil {
		return "", err
	}
	if !s.cfg.IsTimeDock {
		return path, nil
	}
	devices, err := s.timeDockDeviceAccounts(who.TimeDockAccount)
	if err != nil {
		return "", err
	}
	if err := authorizeTimeDockPath(path, devices, false); err != nil {
		return "", err
	}
	return path, nil
}

// authorizeRestorePath allows a missing quarantine destination, but resolves
// its nearest existing ancestor so an existing symlink cannot cross accounts.
func (s *server) authorizeRestorePath(input string, who actor) error {
	if !s.cfg.IsTimeDock {
		return nil
	}
	if who.TimeDockAccount == "" {
		return errTimeDockAccountRequired
	}
	if strings.Contains(input, "\x00") || !filepath.IsAbs(input) {
		return errors.New("invalid restore path")
	}
	devices, err := s.timeDockDeviceAccounts(who.TimeDockAccount)
	if err != nil {
		return err
	}
	return authorizeTimeDockPath(filepath.Clean(input), devices, true)
}

func (s *server) timeDockDeviceAccounts(account string) (map[string]string, error) {
	root := filepath.Clean(s.cfg.BrowseRoots[0])
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	devices := make(map[string]string)
	for _, entry := range entries {
		// IsDir is false for directory symlinks, so only real device directories
		// named extdev or usb followed by ASCII digits are considered.
		if !entry.IsDir() || !isTimeDockDeviceName(entry.Name()) {
			continue
		}
		devicePath := filepath.Join(root, entry.Name())
		accountPath := filepath.Join(devicePath, account)
		info, err := os.Lstat(accountPath)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		devices[devicePath] = accountPath
	}
	return devices, nil
}

func isTimeDockDeviceName(name string) bool {
	if name == "extdev" {
		return true
	}
	if !strings.HasPrefix(name, "usb") || len(name) == len("usb") {
		return false
	}
	for _, r := range name[len("usb"):] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func authorizeTimeDockPath(path string, devices map[string]string, allowMissing bool) error {
	for _, accountRoot := range devices {
		if !pathWithin(path, accountRoot) {
			continue
		}
		realRoot, err := filepath.EvalSymlinks(accountRoot)
		if err != nil {
			continue
		}
		realPath, err := resolvedExistingPath(path, allowMissing)
		if err == nil && pathWithin(realPath, realRoot) {
			return nil
		}
	}
	return errTimeDockPathDenied
}

func resolvedExistingPath(path string, allowMissing bool) (string, error) {
	if !allowMissing {
		return filepath.EvalSymlinks(path)
	}
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		current = parent
	}
}

func pathWithin(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

func (s *server) safePath(input string) (string, error) {
	if strings.Contains(input, "\x00") {
		return "", errors.New("path contains null byte")
	}
	clean := filepath.Clean(input)
	if !strings.HasPrefix(clean, "/") {
		return "", errors.New("path must be absolute")
	}

	realPath, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	for _, root := range s.cfg.BrowseRoots {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", err
		}
		// Check the resolved path so symlinks under /scan cannot escape the
		// configured browsing and scanning root.
		if realPath == realRoot || strings.HasPrefix(realPath, realRoot+"/") {
			return clean, nil
		}
	}
	return "", fmt.Errorf("path must be under one of: %s", strings.Join(s.cfg.BrowseRoots, ", "))
}
