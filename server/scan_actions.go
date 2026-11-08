package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Separate hooks make data-loss recovery paths testable without shell commands.
type quarantineOps struct {
	link    func(string, string) error
	copy    func(string, string) error
	move    func(string, string) error
	remove  func(string) error
	publish func(string, string) error
}

func defaultQuarantineOps() quarantineOps {
	return quarantineOps{os.Link, copyRegularFile, renameNoReplace, os.Remove, os.Rename}
}
func renameNoReplace(src, dst string) error {
	return unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE)
}
func pathExists(path string) bool { _, err := os.Lstat(path); return !errors.Is(err, os.ErrNotExist) }

func (s *server) applyScanAction(file *os.File, path, action, owner string) error {
	s.quarantineFilesMu.Lock()
	defer s.quarantineFilesMu.Unlock()
	// Reject replaced paths before destructive actions. Symlink parents are
	// checked again because advisory locks do not protect directory entries.
	parent, err := openScanPath(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	oldInfo, err := file.Stat()
	if err != nil {
		return err
	}
	base := filepath.Base(path)
	check := func() error {
		var current unix.Stat_t
		if err := unix.Fstatat(int(parent.Fd()), base, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		original := oldInfo.Sys().(*syscall.Stat_t)
		if original.Dev != current.Dev || original.Ino != current.Ino {
			return errors.New("source changed after scan; refusing file action")
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	// Anchor source mutations to the verified parent descriptor, so replacing
	// an ancestor with a symlink cannot redirect deletion outside /scan.
	ops := defaultQuarantineOps()
	ops.remove = func(subject string) error {
		if subject == path {
			if err := check(); err != nil {
				return err
			}
			return unix.Unlinkat(int(parent.Fd()), base, 0)
		}
		return os.Remove(subject)
	}
	ops.link = func(src, dst string) error {
		if src == path {
			if err := check(); err != nil {
				return err
			}
			return unix.Linkat(int(parent.Fd()), base, unix.AT_FDCWD, dst, 0)
		}
		return os.Link(src, dst)
	}
	ops.copy = func(src, dst string) error {
		if src == path {
			if err := check(); err != nil {
				return err
			}
			src = fmt.Sprintf("/proc/self/fd/%d", file.Fd())
		}
		return copyRegularFile(src, dst)
	}
	ops.move = func(src, dst string) error {
		srcFD, dstFD := unix.AT_FDCWD, unix.AT_FDCWD
		if src == path {
			if err := check(); err != nil {
				return err
			}
			srcFD = int(parent.Fd())
			src = base
		}
		if dst == path {
			dstFD = int(parent.Fd())
			dst = base
		}
		return unix.Renameat2(srcFD, src, dstFD, dst, unix.RENAME_NOREPLACE)
	}
	if action == "remove" {
		return ops.remove(path)
	}
	return quarantineDetectedFile(path, owner, s.cfg.QuarantineDir, ops)
}

func quarantineDetectedFile(source, owner, dir string, ops quarantineOps) error {
	if !validUserID(owner) {
		return errors.New("invalid quarantine owner")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	nameMax := 255
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err == nil && fs.Namelen > 0 {
		nameMax = int(fs.Namelen)
	}
	base := filepath.Base(source)
	index := 0
	if strings.HasSuffix(base, ".rec") || base == clamavQuarantineLockName {
		index = 1
	}
	var target, record string
	for {
		name := base
		if index > 0 {
			name = fmt.Sprintf("%s.%03d", base, index)
		}
		// Allow space for the companion record on the destination filesystem.
		if len(name)+len(".rec") > nameMax {
			return errors.New("quarantine filename exceeds NAME_MAX")
		}
		target = filepath.Join(dir, name)
		record = target + ".rec"
		if pathExists(target) || pathExists(record) {
			index++
			continue
		}
		reservation, err := os.OpenFile(record, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(err, os.ErrExist) {
			index++
			continue
		}
		if err != nil {
			return err
		}
		reservation.Close()
		break
	}
	tmp, err := os.CreateTemp(dir, ".quarantine-rec.*")
	if err != nil {
		ops.remove(record)
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	// JSON handles quotes and newlines that the legacy quoted format cannot.
	err = json.NewEncoder(tmp).Encode(struct {
		Source string `json:"source_file"`
		Owner  string `json:"user_id"`
	}{source, owner})
	if err == nil {
		err = tmp.Sync()
	}
	err = errors.Join(err, tmp.Close())
	if err != nil {
		ops.remove(record)
		return err
	}
	if err = moveDetectedFile(source, target, ops); err != nil {
		if !pathExists(source) && pathExists(target) {
			// A failed move may still leave the only copy at its destination.
			// Preserve both that copy and enough metadata for manual recovery.
			if publishErr := ops.publish(tmpPath, record); publishErr != nil {
				data, _ := os.ReadFile(tmpPath)
				err = errors.Join(err, publishErr, os.WriteFile(record, data, 0600))
			}
		} else {
			ops.remove(record)
		}
		return err
	}
	if err = ops.publish(tmpPath, record); err == nil {
		return nil
	}
	// Never overwrite a newly created original path when rolling back. If
	// rollback fails, publish recovery metadata rather than hide the only copy.
	rollbackErr := ops.move(target, source)
	if rollbackErr == nil {
		ops.remove(record)
	} else {
		data, _ := os.ReadFile(tmpPath)
		if writeErr := os.WriteFile(record, data, 0600); writeErr != nil {
			rollbackErr = errors.Join(rollbackErr, writeErr)
		}
	}
	return fmt.Errorf("publish quarantine metadata: %w; rollback: %v; preserved target: %s", err, rollbackErr, target)
}

func moveDetectedFile(source, target string, ops quarantineOps) error {
	verify := func() error {
		if pathExists(source) || !pathExists(target) {
			return errors.New("move verification failed")
		}
		return nil
	}
	cleanup := func() error {
		if !pathExists(source) {
			return fmt.Errorf("source disappeared; preserving possible only copy: %s", target)
		}
		if err := ops.remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clean incomplete quarantine target: %w", err)
		}
		return nil
	}
	if !pathExists(source) {
		return os.ErrNotExist
	}
	if err := ops.link(source, target); err == nil {
		if err = ops.remove(source); err == nil {
			return verify()
		}
	} else if errors.Is(err, os.ErrExist) {
		return err
	}
	if err := cleanup(); err != nil {
		return err
	}
	if err := ops.copy(source, target); err == nil {
		if err = ops.remove(source); err == nil {
			return verify()
		}
	} else if errors.Is(err, os.ErrExist) {
		return err
	}
	if err := cleanup(); err != nil {
		return err
	}
	if err := ops.move(source, target); err != nil {
		if pathExists(source) {
			return errors.Join(err, cleanup())
		}
		return fmt.Errorf("move failed; source disappeared, preserving target %s: %w", target, err)
	}
	return verify()
}
