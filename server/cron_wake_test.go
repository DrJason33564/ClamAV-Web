package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCronConfigRejectsInvalidWakeFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cron.conf")
	if err := os.WriteFile(path, []byte("0 1 * * * /scan warn alice001 X\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCronConfigDoc(path); err == nil {
		t.Fatal("expected invalid wake flag")
	}
}
