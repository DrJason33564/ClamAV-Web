package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRobfigScheduleCompatibility(t *testing.T) {
	for _, tc := range []struct{ spec, start, want string }{
		{"*/5 0-23/2 * 1,6,12 *", "2026-06-01T02:01:00Z", "2026-06-01T02:05:00Z"},
		{"0 2 * * 7", "2026-11-06T00:00:00Z", "2026-11-08T02:00:00Z"},
		{"0 2 * * 5-7", "2026-11-07T03:00:00Z", "2026-11-08T02:00:00Z"},
		{"0 2 1 * 1", "2026-11-01T03:00:00Z", "2026-11-02T02:00:00Z"},
	} {
		fields := strings.Fields(tc.spec)
		rule := cronRule{Minute: fields[0], Hour: fields[1], Day: fields[2], Month: fields[3], Weekday: fields[4], Action: "warn"}
		if err := validateCronRuleFields(rule); err != nil {
			t.Fatal(err)
		}
		schedule, err := scanCronParser.Parse(cronSpec(rule))
		if err != nil {
			t.Fatal(err)
		}
		start, _ := time.Parse(time.RFC3339, tc.start)
		if got := schedule.Next(start).Format(time.RFC3339); got != tc.want {
			t.Fatalf("%s: %s != %s", tc.spec, got, tc.want)
		}
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	schedule, _ := scanCronParser.Parse("0 2 * * *")
	start := time.Date(2026, 11, 8, 1, 0, 0, 0, loc)
	if next := schedule.Next(start); next.Hour() != 2 || next.Location() != loc {
		t.Fatalf("timezone ignored: %v", next)
	}
}

func TestCronReloadIsValidatedAndLegacyRowsRemainEditable(t *testing.T) {
	s := newScanTestServer(t)
	s.cfg.CronConfigFile = filepath.Join(t.TempDir(), "cron.conf")
	valid := "0 1 * * * /scan warn " + testAliceUserID + " Y\n"
	os.WriteFile(s.cfg.CronConfigFile, []byte(valid), 0600)
	if _, err := s.reloadCron(); err != nil {
		t.Fatal(err)
	}
	id := s.cronRules[0].ID
	if _, _, err := s.setCronRuleEnabled(id, false, actor{ID: testAliceUserID}); err != nil {
		t.Fatal(err)
	}
	if len(s.cronRules) != 1 || s.cronRules[0].Enabled {
		t.Fatalf("raw rule not disabled: %+v", s.cronRules)
	}
	os.WriteFile(s.cfg.CronConfigFile, []byte("invalid\n"), 0600)
	if _, err := s.reloadCron(); err == nil {
		t.Fatal("invalid reload succeeded")
	}
	if len(s.cronRules) != 1 {
		t.Fatal("invalid reload discarded previous schedule")
	}
}

func TestCronCallbackDeduplicatesAndRejectsRemovedRule(t *testing.T) {
	s := newScanTestServer(t)
	if _, err := s.userDB.Exec("INSERT INTO users(id,username,role,created_at,updated_at) VALUES(?,?,?,1,1)", testAliceUserID, "alice", "user"); err != nil {
		t.Fatal(err)
	}
	var owner string
	s.userDB.QueryRow("SELECT id FROM users WHERE username='alice'").Scan(&owner)
	rule := cronRule{ID: "abc123def456gh78", Enabled: true, UserID: owner, Minute: "*", Hour: "*", Day: "*", Month: "*", Weekday: "*", Target: s.cfg.BrowseRoots[0], Action: "warn"}
	s.cronRules = []cronRule{rule}
	s.queueRunnerActive = true // Keep accepted work observable without starting scans.
	now := time.Now()
	s.submitCronRule(rule, now)
	s.submitCronRule(rule, now)
	if len(s.batches) != 1 {
		t.Fatalf("duplicate callback: %d", len(s.batches))
	}
	s.cronRules = nil
	s.submitCronRule(rule, now.Add(time.Minute))
	if len(s.batches) != 1 {
		t.Fatal("removed callback accepted")
	}
}

func TestScheduledWakeAndSleepFailureHistory(t *testing.T) {
	for _, wake := range []bool{false, true} {
		t.Run(map[bool]string{false: "sleep fails", true: "wake succeeds"}[wake], func(t *testing.T) {
			s := newScanTestServer(t)
			s.engineSleeping = true
			script := filepath.Join(t.TempDir(), "wake.sh")
			os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0700)
			s.cfg.StartupScript = script
			s.cfg.WakeTimeout = time.Second
			batch := &scanBatch{ID: "scheduled-test", Type: "cron", UserID: testAliceUserID, Status: "running", Action: "warn", Targets: []string{filepath.Join(s.cfg.BrowseRoots[0], "missing")}, Wake: wake, SubmittedAt: time.Now()}
			s.batches[batch.ID] = batch
			s.activeBatchID = batch.ID
			s.runBatch(batch.ID)
			var result string
			if err := s.historyDB.QueryRow("SELECT result FROM history_jobs WHERE user_id=?", testAliceUserID).Scan(&result); err != nil || result != "error" {
				t.Fatalf("history %s %v", result, err)
			}
			if s.isSleeping() == wake {
				t.Fatalf("wake state unchanged: %v", s.isSleeping())
			}
		})
	}
}

func TestCronSchedulerStopsWithContext(t *testing.T) {
	s := newScanTestServer(t)
	s.cronChanged = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.runCronScheduler(ctx) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}
