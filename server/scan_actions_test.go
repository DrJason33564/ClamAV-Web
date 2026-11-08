package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoQuarantineNamesAndOwners(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	for i, tc := range []struct{ name, owner, want string }{
		{"same.dat", testAliceUserID, "same.dat"}, {"same.dat", testBobUserID, "same.dat.001"}, {"report.rec", testAliceUserID, "report.rec.001"}, {clamavQuarantineLockName, testAliceUserID, clamavQuarantineLockName + ".001"},
	} {
		source := filepath.Join(root, tc.name)
		body := []byte{byte(i)}
		if err := os.WriteFile(source, body, 0640); err != nil {
			t.Fatal(err)
		}
		if err := quarantineDetectedFile(source, tc.owner, dir, defaultQuarantineOps()); err != nil {
			t.Fatal(err)
		}
		original, owner, err := readQuarantineRecord(filepath.Join(dir, tc.want+".rec"))
		if err != nil || original != source || owner != tc.owner {
			t.Fatalf("record %q %q %v", original, owner, err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, tc.want))
		if string(data) != string(body) {
			t.Fatal("quarantine content changed")
		}
	}
}

func TestGoQuarantineFallbacksAndCleanup(t *testing.T) {
	for _, fallback := range []string{"copy", "move"} {
		t.Run(fallback, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "file")
			os.WriteFile(source, []byte("payload"), 0640)
			target := filepath.Join(t.TempDir(), "file")
			ops := defaultQuarantineOps()
			ops.link = func(string, string) error { return errors.New("no hard links") }
			if fallback == "move" {
				ops.copy = func(_, dst string) error {
					os.WriteFile(dst, []byte("partial"), 0600)
					return errors.New("copy failed")
				}
			}
			if err := moveDetectedFile(source, target, ops); err != nil {
				t.Fatal(err)
			}
			if pathExists(source) {
				t.Fatal("source still exists")
			}
			data, _ := os.ReadFile(target)
			if string(data) != "payload" {
				t.Fatalf("target=%q", data)
			}
		})
	}
}

func TestGoQuarantineDoesNotOverwriteCollision(t *testing.T) {
	source := filepath.Join(t.TempDir(), "file")
	target := filepath.Join(t.TempDir(), "file")
	os.WriteFile(source, []byte("source"), 0600)
	os.WriteFile(target, []byte("existing"), 0600)
	if err := moveDetectedFile(source, target, defaultQuarantineOps()); err == nil {
		t.Fatal("collision succeeded")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "existing" {
		t.Fatal("overwrote existing target")
	}
}

func TestGoQuarantineStopsIfPartialCopyCannotBeRemoved(t *testing.T) {
	source := filepath.Join(t.TempDir(), "file")
	target := filepath.Join(t.TempDir(), "file")
	os.WriteFile(source, []byte("source"), 0600)
	ops := defaultQuarantineOps()
	ops.link = func(string, string) error { return errors.New("link failed") }
	ops.copy = func(_, dst string) error {
		os.WriteFile(dst, []byte("partial"), 0600)
		return errors.New("copy failed")
	}
	ops.remove = func(path string) error {
		if path == target && pathExists(target) {
			return errors.New("cannot clean partial")
		}
		return os.Remove(path)
	}
	ops.move = func(string, string) error { t.Error("unsafe move fallback after cleanup failure"); return nil }
	if err := moveDetectedFile(source, target, ops); err == nil || !strings.Contains(err.Error(), "clean incomplete") {
		t.Fatalf("error=%v", err)
	}
	data, _ := os.ReadFile(source)
	if string(data) != "source" {
		t.Fatal("source lost")
	}
}

func TestGoQuarantinePreservesOnlyCopyWhenSourceDisappears(t *testing.T) {
	source := filepath.Join(t.TempDir(), "file")
	target := filepath.Join(t.TempDir(), "file")
	os.WriteFile(source, []byte("payload"), 0600)
	ops := defaultQuarantineOps()
	ops.link = func(src, dst string) error {
		if err := os.Link(src, dst); err != nil {
			return err
		}
		os.Remove(src)
		return errors.New("source disappeared")
	}
	if err := moveDetectedFile(source, target, ops); err == nil {
		t.Fatal("expected disappearance error")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "payload" {
		t.Fatal("only copy lost")
	}
}

func TestGoQuarantineRollsBackMetadataFailure(t *testing.T) {
	source := filepath.Join(t.TempDir(), "file")
	dir := t.TempDir()
	os.WriteFile(source, []byte("payload"), 0600)
	ops := defaultQuarantineOps()
	ops.publish = func(string, string) error { return errors.New("publish failed") }
	if err := quarantineDetectedFile(source, testAliceUserID, dir, ops); err == nil {
		t.Fatal("expected publish failure")
	}
	data, _ := os.ReadFile(source)
	if string(data) != "payload" {
		t.Fatal("rollback lost source")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("incomplete quarantine entries: %v", entries)
	}
}

func TestGoQuarantineMetadataFailurePreservesRecoveryRecord(t *testing.T) {
	source := filepath.Join(t.TempDir(), "file")
	dir := t.TempDir()
	os.WriteFile(source, []byte("payload"), 0600)
	ops := defaultQuarantineOps()
	ops.publish = func(string, string) error {
		os.WriteFile(source, []byte("replacement"), 0600)
		return errors.New("publish failed")
	}
	if err := quarantineDetectedFile(source, testAliceUserID, dir, ops); err == nil {
		t.Fatal("expected failure")
	}
	data, _ := os.ReadFile(source)
	if string(data) != "replacement" {
		t.Fatal("rollback overwrote replacement")
	}
	_, owner, err := readQuarantineRecord(filepath.Join(dir, "file.rec"))
	if err != nil || owner != testAliceUserID {
		t.Fatalf("missing recovery metadata: %q %v", owner, err)
	}
}

func TestGoQuarantineRejectsOverlongRecordName(t *testing.T) {
	source := filepath.Join(t.TempDir(), strings.Repeat("x", 251))
	dir := t.TempDir()
	os.WriteFile(source, []byte("payload"), 0600)
	os.WriteFile(filepath.Join(dir, filepath.Base(source)), nil, 0600)
	if err := quarantineDetectedFile(source, testAliceUserID, dir, defaultQuarantineOps()); err == nil || !strings.Contains(err.Error(), "NAME_MAX") {
		t.Fatalf("err=%v", err)
	}
	if !pathExists(source) {
		t.Fatal("source lost")
	}
}

func TestExecutorContinuesAfterQuarantineFailureAndRetainsFinding(t *testing.T) {
	s := newScanTestServer(t)
	s.cfg.ClamdSocket = fakeClamd(t, func(string, *os.File) string { return "fd: Test FOUND\x00" })
	os.WriteFile(filepath.Join(s.cfg.BrowseRoots[0], strings.Repeat("x", 252)), nil, 0600)
	os.WriteFile(filepath.Join(s.cfg.BrowseRoots[0], "normal"), []byte("payload"), 0600)
	job := s.history.newScanJob("manual", testAliceUserID, s.cfg.BrowseRoots[0], "move", false)
	summary := s.executeScan(t.Context(), job)
	if summary.ActionFailed != 1 || summary.ActionSucceeded != 1 || len(summary.Detections) != 2 {
		t.Fatalf("summary %+v", summary)
	}
	if err := s.history.finishScanJob(t.Context(), job, summary); err != nil {
		t.Fatal(err)
	}
	if job.Result != "found" || job.Status != "finished" {
		t.Fatalf("job %+v", job)
	}
	if _, err := s.historyDB.Exec("DELETE FROM history_jobs"); err != nil {
		t.Fatal(err)
	}
	if result, err := s.history.rebuild(t.Context()); err != nil || result.Updated != 1 {
		t.Fatalf("rebuild %+v: %v", result, err)
	}
	var count int
	s.historyDB.QueryRow("SELECT COUNT(*) FROM history_detections").Scan(&count)
	if count != 2 {
		t.Fatalf("lost detections after rebuild: %d", count)
	}
}
