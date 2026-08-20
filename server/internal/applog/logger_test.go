package applog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMaskSecret(t *testing.T) {
	for value, want := range map[string]string{
		"":                 "",
		"short":            "********",
		"12345678":         "********",
		"abcdefghijklmnop": "abcd********mnop",
		"中文字符一二三四五六七八九": "中文字符********六七八九",
	} {
		if got := MaskSecret(value); got != want {
			t.Errorf("MaskSecret(%q)=%q, want %q", value, got, want)
		}
	}
}

func TestLoggerFiltersAndRedacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application.log")
	logger, err := New(Config{Path: path, MaxSize: 4096, MaxFiles: 2, Level: "info"})
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("hidden debug")
	logger.Info("login", "password", "abcdefghijklmnop", Secret("credential", "1234567890"), "user", "alice")
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, unwanted := range []string{"hidden debug", "abcdefghijklmnop", "1234567890"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("log contains sensitive or filtered text %q: %s", unwanted, text)
		}
	}
	for _, wanted := range []string{"abcd********mnop", "1234********7890", "user=alice"} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("log does not contain %q: %s", wanted, text)
		}
	}
}

func TestLoggerRotatesAndKeepsConfiguredCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application.log")
	logger, err := New(Config{Path: path, MaxSize: 160, MaxFiles: 3, Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		logger.Info("rotation record", "index", index, "padding", strings.Repeat("x", 40))
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + ".1", path + ".2"} {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("expected rotated log %s: %v", name, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("unexpected excess rotated log: %v", err)
	}
}

func TestLoggerWithOneFileTruncatesOnRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application.log")
	logger, err := New(Config{Path: path, MaxSize: 120, MaxFiles: 1, Level: "info"})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 5; index++ {
		logger.Info("single file rotation", "index", index, "padding", strings.Repeat("x", 40))
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("single-file mode retained an archive: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		t.Fatalf("active log is unavailable after rotation: size=%d err=%v", len(data), err)
	}
}

func TestLoggerConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application.log")
	logger, err := New(Config{Path: path, MaxSize: 1 << 20, MaxFiles: 2, Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for record := 0; record < 100; record++ {
				logger.Debug("concurrent", "worker", worker, "record", record)
			}
		}(worker)
	}
	wg.Wait()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 800 {
		t.Fatalf("got %d complete log lines, want 800", lines)
	}
}
