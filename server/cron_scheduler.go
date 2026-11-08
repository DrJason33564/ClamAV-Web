package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var scanCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func cronSpec(rule cronRule) string {
	// The persisted Debian grammar accepts both 0 and 7 for Sunday. Robfig
	// accepts 0..6, so expand numeric weekday ranges while retaining '*' parts
	// (the wildcard flag determines day-of-month/day-of-week semantics).
	var weekdays []string
	for _, part := range strings.Split(rule.Weekday, ",") {
		if strings.HasPrefix(part, "*") {
			weekdays = append(weekdays, part)
			continue
		}
		base, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			step, _ = strconv.Atoi(stepText)
		}
		start, end, ranged := strings.Cut(base, "-")
		lo, _ := strconv.Atoi(start)
		hi := lo
		if ranged {
			hi, _ = strconv.Atoi(end)
		}
		for n := lo; n <= hi && step > 0; {
			weekdays = append(weekdays, strconv.Itoa(n%7))
			if step > hi-n {
				break
			}
			n += step
		}
	}
	return strings.Join([]string{rule.Minute, rule.Hour, rule.Day, rule.Month, strings.Join(weekdays, ",")}, " ")
}

func (s *server) runCronScheduler(ctx context.Context) {
	logger := cronLog{s}
	scheduler := cron.New(cron.WithParser(scanCronParser), cron.WithLocation(time.Local), cron.WithLogger(logger), cron.WithChain(cron.Recover(logger)))
	replace := func() {
		for _, entry := range scheduler.Entries() {
			scheduler.Remove(entry.ID)
		}
		s.cronMu.RLock()
		rules := append([]cronRule(nil), s.cronRules...)
		s.cronMu.RUnlock()
		for _, rule := range rules {
			if !rule.Enabled {
				continue
			}
			if _, err := scheduler.AddFunc(cronSpec(rule), func() { s.submitCronRule(rule, time.Now()) }); err != nil {
				s.error("cron", "schedule registration failed", "rule_id", rule.ID, "error", err)
			}
		}
	}
	replace()
	scheduler.Start()
	defer func() { <-scheduler.Stop().Done() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.cronChanged:
			replace()
		}
	}
}

type cronLog struct{ s *server }

func (l cronLog) Info(msg string, args ...interface{}) { l.s.debug("cron", msg, args...) }
func (l cronLog) Error(err error, msg string, args ...interface{}) {
	l.s.error("cron", msg, append(args, "error", err)...)
}

func (s *server) submitCronRule(rule cronRule, now time.Time) {
	s.accountDeleteMu.Lock()
	defer s.accountDeleteMu.Unlock()
	s.configFileMu.Lock()
	// A callback already dispatched by the old scheduler cannot resurrect a
	// disabled/deleted/edited rule. Deduplicate reloads by actual minute.
	s.cronMu.Lock()
	current := false
	for _, active := range s.cronRules {
		if active == rule && active.Enabled {
			current = true
			break
		}
	}
	minute := now.Unix() / 60
	if !current || s.cronLastMinute[rule.ID] >= minute {
		s.cronMu.Unlock()
		s.configFileMu.Unlock()
		return
	}
	if s.cronLastMinute == nil {
		s.cronLastMinute = map[string]int64{}
	}
	s.cronLastMinute[rule.ID] = minute
	s.cronMu.Unlock()
	s.configFileMu.Unlock()
	if s.scanContext().Err() != nil {
		return
	}
	who, err := s.scanActor(rule.UserID)
	if err != nil {
		s.warn("cron", "scheduled scan owner unavailable", "rule_id", rule.ID, "user_id", rule.UserID, "error", err)
		return
	}
	s.clamavPowerMu.Lock()
	defer s.clamavPowerMu.Unlock()
	if s.scanContext().Err() != nil {
		return
	}
	batch := &scanBatch{ID: "scheduled-" + randomHex(8), Type: "cron", UserID: who.ID, Username: who.Username, Actor: who, Status: "queued", Action: rule.Action, Targets: []string{rule.Target}, Wake: rule.Wake, Message: "Queued", SubmittedAt: now}
	s.enqueueBatch(batch)
	s.info("cron", "scheduled scan queued", "rule_id", rule.ID, "batch_id", batch.ID, "user_id", who.ID)
}

func validateCronSchedule(rule cronRule) error {
	if _, err := scanCronParser.Parse(cronSpec(rule)); err != nil {
		return fmt.Errorf("invalid cron schedule: %w", err)
	}
	return nil
}
