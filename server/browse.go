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
	path, err := s.safePath(requested)
	if err != nil {
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
		writeError(w, http.StatusForbidden, err)
		return
	}

	entries := make([]browseEntry, 0, len(dirEntries))
	for _, entry := range dirEntries {
		full := filepath.Join(path, entry.Name())
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

	parent := ""
	if parentPath := filepath.Dir(path); parentPath != path {
		if _, err := s.safePath(parentPath); err == nil {
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
