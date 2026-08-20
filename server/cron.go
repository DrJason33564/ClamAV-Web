package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type cronRule struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Minute  string `json:"minute"`
	Hour    string `json:"hour"`
	Day     string `json:"day"`
	Month   string `json:"month"`
	Weekday string `json:"weekday"`
	Target  string `json:"target"`
	Action  string `json:"action"`
	Wake    bool   `json:"wake"`
	User    string `json:"-"`
	Line    int    `json:"line,omitempty"`
}

type cronRulesResponse struct {
	Rules   []cronRule `json:"rules"`
	Message string     `json:"message,omitempty"`
}

type cronEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

func (s *server) handleScans(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.startScan(w, r)
	case http.MethodGet:
		s.listScans(w, r)
	default:
		methodNotAllowed(w)
	}
}

func (s *server) handleCronRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		who, _ := actorFromRequest(r)
		s.configFileMu.Lock()
		rules, err := s.readCronRules(who.Username)
		s.configFileMu.Unlock()
		if err != nil {
			s.error("cron", "read cron rules failed", "user", who.Username, "error", err)
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.debug("cron", "cron rules listed", "user", who.Username, "rules", len(rules))
		writeJSON(w, http.StatusOK, cronRulesResponse{Rules: rules})
	case http.MethodPost:
		var req cronRule
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		who, _ := actorFromRequest(r)
		s.configFileMu.Lock()
		rules, message, err := s.addCronRule(req, who)
		s.configFileMu.Unlock()
		if err != nil {
			s.warn("cron", "cron rule creation failed", "user", who.Username, "error", err)
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.info("cron", "cron rule created", "user", who.Username, "rules", len(rules))
		writeJSON(w, http.StatusCreated, cronRulesResponse{Rules: rules, Message: message})
	default:
		methodNotAllowed(w)
	}
}

func (s *server) handleCronRule(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/cron/rules/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}

	if strings.HasSuffix(rest, "/enabled") {
		id := strings.TrimSuffix(rest, "/enabled")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPatch {
			methodNotAllowed(w)
			return
		}
		var req cronEnabledRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		who, _ := actorFromRequest(r)
		s.configFileMu.Lock()
		rules, message, err := s.setCronRuleEnabled(id, req.Enabled, who)
		s.configFileMu.Unlock()
		if err != nil {
			s.warn("cron", "cron rule state change failed", "rule_id", id, "user", who.Username, "enabled", req.Enabled, "error", err)
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.info("cron", "cron rule state changed", "rule_id", id, "user", who.Username, "enabled", req.Enabled)
		writeJSON(w, http.StatusOK, cronRulesResponse{Rules: rules, Message: message})
		return
	}

	id := rest
	if strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req cronRule
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		who, _ := actorFromRequest(r)
		s.configFileMu.Lock()
		rules, message, err := s.updateCronRule(id, req, who)
		s.configFileMu.Unlock()
		if err != nil {
			s.warn("cron", "cron rule update failed", "rule_id", id, "user", who.Username, "error", err)
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.info("cron", "cron rule updated", "rule_id", id, "user", who.Username)
		writeJSON(w, http.StatusOK, cronRulesResponse{Rules: rules, Message: message})
	case http.MethodDelete:
		who, _ := actorFromRequest(r)
		s.configFileMu.Lock()
		rules, message, err := s.deleteCronRule(id, who.Username)
		s.configFileMu.Unlock()
		if err != nil {
			s.warn("cron", "cron rule deletion failed", "rule_id", id, "user", who.Username, "error", err)
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.info("cron", "cron rule deleted", "rule_id", id, "user", who.Username)
		writeJSON(w, http.StatusOK, cronRulesResponse{Rules: rules, Message: message})
	default:
		methodNotAllowed(w)
	}
}

func (s *server) handleCronReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	s.configFileMu.Lock()
	defer s.configFileMu.Unlock()
	message, err := s.runCronScript("reload")
	if err != nil {
		s.error("cron", "cron reload failed", "error", err)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.info("cron", "cron rules reloaded")
	who, _ := actorFromRequest(r)
	rules, readErr := s.readCronRules(who.Username)
	if readErr != nil {
		writeError(w, http.StatusInternalServerError, readErr)
		return
	}
	writeJSON(w, http.StatusOK, cronRulesResponse{Rules: rules, Message: message})
}

const cronRuleMetaPrefix = "# scanner-cron-rule"

type cronRuleBlock struct {
	MetaIndex int
	RuleIndex int
	Rule      cronRule
}

type cronConfigDoc struct {
	Lines  []string
	Blocks []cronRuleBlock
}

func (s *server) readCronRules(usernames ...string) ([]cronRule, error) {
	username := ""
	if len(usernames) > 0 {
		username = usernames[0]
	}
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return nil, err
	}
	rules := make([]cronRule, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		if username != "" && block.Rule.User != username {
			continue
		}
		rules = append(rules, block.Rule)
	}
	return rules, nil
}

func (s *server) readCronConfigDoc() (cronConfigDoc, error) {
	data, err := os.ReadFile(s.cfg.CronConfigFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cronConfigDoc{}, nil
		}
		return cronConfigDoc{}, err
	}

	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	doc := cronConfigDoc{Lines: lines}
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], cronRuleMetaPrefix) {
			continue
		}
		if i+1 >= len(lines) {
			return doc, fmt.Errorf("cron rule metadata at line %d has no rule line", i+1)
		}
		id, enabled, err := parseCronRuleMeta(lines[i])
		if err != nil {
			return doc, fmt.Errorf("invalid cron rule metadata at line %d: %w", i+1, err)
		}
		ruleLine := lines[i+1]
		if !enabled {
			ruleLine = uncommentCronRuleLine(ruleLine)
		}
		rule, err := parseCronRuleLine(ruleLine)
		if err != nil {
			return doc, fmt.Errorf("invalid cron rule at line %d: %w", i+2, err)
		}
		rule.ID = id
		rule.Enabled = enabled
		rule.Line = i + 2
		doc.Blocks = append(doc.Blocks, cronRuleBlock{
			MetaIndex: i,
			RuleIndex: i + 1,
			Rule:      rule,
		})
		i++
	}
	return doc, nil
}

func parseCronRuleMeta(line string) (string, bool, error) {
	fields := strings.Fields(line)
	id := ""
	enabled := true
	for _, field := range fields[2:] {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "id":
			id = value
		case "enabled":
			switch value {
			case "true":
				enabled = true
			case "false":
				enabled = false
			default:
				return "", false, fmt.Errorf("unsupported enabled value %q", value)
			}
		}
	}
	if !validCronRuleID(id) {
		return "", false, fmt.Errorf("invalid rule id %q", id)
	}
	return id, enabled, nil
}

func parseCronRuleLine(line string) (cronRule, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return cronRule{}, errors.New("empty rule")
	}

	fields := strings.Fields(line)
	if len(fields) < 9 {
		return cronRule{}, errors.New("expected minute hour day month weekday target action owner wake")
	}
	rule := cronRule{
		Minute:  fields[0],
		Hour:    fields[1],
		Day:     fields[2],
		Month:   fields[3],
		Weekday: fields[4],
	}

	tail := cronLineTailAfterFields(line, 5)
	target, rest, err := parseCronTargetAndRest(tail)
	if err != nil {
		return cronRule{}, err
	}
	actionFields := strings.Fields(rest)
	if len(actionFields) != 3 {
		return cronRule{}, errors.New("expected exactly one action, one owner and one wake flag")
	}
	rule.Target = target
	rule.Action = actionFields[0]
	rule.User = actionFields[1]
	switch actionFields[2] {
	case "Y":
		rule.Wake = true
	case "N":
		rule.Wake = false
	default:
		return cronRule{}, errors.New("wake flag must be Y or N")
	}
	if !usernamePattern.MatchString(rule.User) {
		return cronRule{}, errors.New("invalid rule owner")
	}
	return rule, nil
}

func cronLineTailAfterFields(line string, count int) string {
	tail := line
	for i := 0; i < count; i++ {
		tail = strings.TrimLeft(tail, " \t")
		pos := strings.IndexAny(tail, " \t")
		if pos < 0 {
			return ""
		}
		tail = tail[pos+1:]
	}
	return strings.TrimSpace(tail)
}

func parseCronTargetAndRest(tail string) (string, string, error) {
	if strings.HasPrefix(tail, "\"") {
		rest := strings.TrimPrefix(tail, "\"")
		end := strings.Index(rest, "\"")
		if end < 0 {
			return "", "", errors.New("unterminated quoted target")
		}
		return rest[:end], strings.TrimSpace(rest[end+1:]), nil
	}
	fields := strings.Fields(tail)
	if len(fields) < 2 {
		return "", "", errors.New("missing target or action")
	}
	return fields[0], strings.TrimSpace(strings.TrimPrefix(tail, fields[0])), nil
}

func uncommentCronRuleLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
	}
	return line
}

func (s *server) addCronRule(req cronRule, actors ...actor) ([]cronRule, string, error) {
	who := actor{Username: req.User}
	if len(actors) > 0 {
		who = actors[0]
	}
	username := who.Username
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return nil, "", err
	}
	rule, err := s.prepareCronRule(req, who)
	if err != nil {
		return nil, "", err
	}
	rule.ID = uniqueCronRuleID(doc.Blocks)
	rule.User = username
	doc.Lines = append(doc.Lines, cronMetaLine(rule), cronConfigLine(rule))
	return s.saveCronConfigDoc(doc, username)
}

func (s *server) updateCronRule(id string, req cronRule, actors ...actor) ([]cronRule, string, error) {
	who := actor{Username: req.User}
	if len(actors) > 0 {
		who = actors[0]
	}
	username := who.Username
	doc, block, err := s.findCronRuleBlock(id, username)
	if err != nil {
		return nil, "", err
	}
	rule, err := s.prepareCronRule(req, who)
	if err != nil {
		return nil, "", err
	}
	rule.ID = id
	rule.User = username
	doc.Lines[block.MetaIndex] = cronMetaLine(rule)
	doc.Lines[block.RuleIndex] = cronConfigLine(rule)
	return s.saveCronConfigDoc(doc, username)
}

func (s *server) setCronRuleEnabled(id string, enabled bool, actors ...actor) ([]cronRule, string, error) {
	who := actor{}
	if len(actors) > 0 {
		who = actors[0]
	}
	username := who.Username
	doc, block, err := s.findCronRuleBlock(id, username)
	if err != nil {
		return nil, "", err
	}
	rule := block.Rule
	if enabled {
		if _, err := s.safePathForActor(rule.Target, who); err != nil {
			return nil, "", err
		}
	}
	rule.Enabled = enabled
	doc.Lines[block.MetaIndex] = cronMetaLine(rule)
	doc.Lines[block.RuleIndex] = cronConfigLine(rule)
	return s.saveCronConfigDoc(doc, username)
}

func (s *server) deleteCronRule(id string, usernames ...string) ([]cronRule, string, error) {
	username := ""
	if len(usernames) > 0 {
		username = usernames[0]
	}
	doc, block, err := s.findCronRuleBlock(id, username)
	if err != nil {
		return nil, "", err
	}
	doc.Lines = append(doc.Lines[:block.MetaIndex], doc.Lines[block.RuleIndex+1:]...)
	return s.saveCronConfigDoc(doc, username)
}

func (s *server) findCronRuleBlock(id string, usernames ...string) (cronConfigDoc, cronRuleBlock, error) {
	username := ""
	if len(usernames) > 0 {
		username = usernames[0]
	}
	if !validCronRuleID(id) {
		return cronConfigDoc{}, cronRuleBlock{}, errors.New("invalid rule id")
	}
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return cronConfigDoc{}, cronRuleBlock{}, err
	}
	for _, block := range doc.Blocks {
		if block.Rule.ID == id && (username == "" || block.Rule.User == username) {
			return doc, block, nil
		}
	}
	return doc, cronRuleBlock{}, errors.New("cron rule not found")
}

func (s *server) prepareCronRule(req cronRule, actors ...actor) (cronRule, error) {
	rule := cronRule{
		Enabled: req.Enabled,
		Minute:  strings.TrimSpace(req.Minute),
		Hour:    strings.TrimSpace(req.Hour),
		Day:     strings.TrimSpace(req.Day),
		Month:   strings.TrimSpace(req.Month),
		Weekday: strings.TrimSpace(req.Weekday),
		Target:  strings.TrimSpace(req.Target),
		Action:  strings.TrimSpace(req.Action),
		Wake:    req.Wake,
	}
	if rule.Action == "" {
		rule.Action = "warn"
	}
	if err := validateCronRuleFields(rule); err != nil {
		return cronRule{}, err
	}
	who := actor{}
	if len(actors) > 0 {
		who = actors[0]
	}
	target, err := s.safePathForActor(rule.Target, who)
	if err != nil {
		return cronRule{}, err
	}
	if _, err := os.Stat(target); err != nil {
		return cronRule{}, err
	}
	if strings.Contains(target, "\"") {
		return cronRule{}, errors.New("Invalid crontab rule at: target")
	}
	rule.Target = target
	return rule, nil
}

func validateCronRuleFields(rule cronRule) error {
	checks := []struct {
		name  string
		value string
		min   int
		max   int
	}{
		{"minute", rule.Minute, 0, 59},
		{"hour", rule.Hour, 0, 23},
		{"day", rule.Day, 1, 31},
		{"month", rule.Month, 1, 12},
		{"weekday", rule.Weekday, 0, 7},
	}
	for _, check := range checks {
		if !validCronField(check.value, check.min, check.max) {
			return fmt.Errorf("Invalid crontab rule at: %s", check.name)
		}
	}
	switch rule.Action {
	case "warn", "move", "remove":
		return nil
	default:
		return errors.New("Invalid crontab rule at: action")
	}
}

func validCronField(field string, min int, max int) bool {
	if field == "" {
		return false
	}
	for _, part := range strings.Split(field, ",") {
		if part == "" || !validCronPart(part, min, max) {
			return false
		}
	}
	return true
}

func validCronPart(part string, min int, max int) bool {
	if strings.Contains(part, "/") {
		base, stepText, ok := strings.Cut(part, "/")
		if !ok || stepText == "" {
			return false
		}
		step, err := strconv.Atoi(stepText)
		if err != nil || step < 1 {
			return false
		}
		return validCronAtom(base, min, max)
	}
	return validCronAtom(part, min, max)
}

func validCronAtom(atom string, min int, max int) bool {
	if atom == "*" {
		return true
	}
	if strings.Contains(atom, "-") {
		startText, endText, ok := strings.Cut(atom, "-")
		if !ok {
			return false
		}
		start, err := strconv.Atoi(startText)
		if err != nil {
			return false
		}
		end, err := strconv.Atoi(endText)
		if err != nil {
			return false
		}
		return start >= min && end <= max && start <= end
	}
	value, err := strconv.Atoi(atom)
	return err == nil && value >= min && value <= max
}

func (s *server) saveCronConfigDoc(doc cronConfigDoc, usernames ...string) ([]cronRule, string, error) {
	s.debug("cron", "saving cron configuration", "lines", len(doc.Lines))
	dir := filepath.Dir(s.cfg.CronConfigFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	tmp, err := os.CreateTemp(dir, ".cron_scan.*")
	if err != nil {
		return nil, "", err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	content := strings.Join(doc.Lines, "\n")
	if content != "" {
		content += "\n"
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return nil, "", err
	}
	if err := tmp.Close(); err != nil {
		return nil, "", err
	}

	if _, err := s.runCronScript("validate", tmpPath); err != nil {
		return nil, "", err
	}
	if err := os.Rename(tmpPath, s.cfg.CronConfigFile); err != nil {
		return nil, "", err
	}
	cleanup = false

	message, err := s.runCronScript("reload")
	if err != nil {
		return nil, "", err
	}
	rules, err := s.readCronRules(usernames...)
	if err != nil {
		return nil, "", err
	}
	return rules, message, nil
}

func (s *server) runCronScript(args ...string) (string, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.CronScript, args...)
	out, err := cmd.CombinedOutput()
	message := cronScriptMessage(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		s.error("cron", "cron command timed out", "args", strings.Join(args, " "), "duration_ms", time.Since(started).Milliseconds())
		return message, errors.New("cron command timed out")
	}
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		s.error("cron", "cron command failed", "args", strings.Join(args, " "), "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return message, errors.New(message)
	}
	s.debug("cron", "cron command completed", "args", strings.Join(args, " "), "duration_ms", time.Since(started).Milliseconds())
	if len(args) > 0 && args[0] == "validate" && !strings.HasPrefix(message, "Cron config is valid") {
		return message, errors.New(message)
	}
	return message, nil
}

func cronScriptMessage(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Invalid") {
			return line
		}
		if strings.HasPrefix(line, "Cron config is valid") {
			return line
		}
		if strings.HasPrefix(line, "Cron rules reloaded") {
			return line
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

func cronMetaLine(rule cronRule) string {
	return fmt.Sprintf("%s id=%s enabled=%t", cronRuleMetaPrefix, rule.ID, rule.Enabled)
}

func cronConfigLine(rule cronRule) string {
	wake := "N"
	if rule.Wake {
		wake = "Y"
	}
	line := fmt.Sprintf("%s %s %s %s %s %s %s %s %s",
		rule.Minute,
		rule.Hour,
		rule.Day,
		rule.Month,
		rule.Weekday,
		quoteCronTarget(rule.Target),
		rule.Action,
		rule.User,
		wake,
	)
	if !rule.Enabled {
		return "# " + line
	}
	return line
}

func quoteCronTarget(target string) string {
	if strings.ContainsAny(target, " \t") {
		return `"` + target + `"`
	}
	return target
}

func uniqueCronRuleID(blocks []cronRuleBlock) string {
	used := make(map[string]bool, len(blocks))
	for _, block := range blocks {
		used[block.Rule.ID] = true
	}
	for {
		id := randomAlphaNum(16)
		if !used[id] {
			return id
		}
	}
}

func randomAlphaNum(length int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, length)
	random := make([]byte, length)
	if _, err := rand.Read(random); err != nil {
		fallback := fmt.Sprintf("%x", time.Now().UnixNano())
		for len(fallback) < length {
			fallback += fallback
		}
		return fallback[:length]
	}
	for i, b := range random {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(buf)
}

func validCronRuleID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, ch := range id {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}

func (s *server) disableCronRulesForUser(username string) error {
	s.configFileMu.Lock()
	defer s.configFileMu.Unlock()
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return err
	}
	changed := false
	for _, block := range doc.Blocks {
		if block.Rule.User != username || !block.Rule.Enabled {
			continue
		}
		rule := block.Rule
		rule.Enabled = false
		doc.Lines[block.MetaIndex] = cronMetaLine(rule)
		doc.Lines[block.RuleIndex] = cronConfigLine(rule)
		changed = true
	}
	if !changed {
		return nil
	}
	_, _, err = s.saveCronConfigDoc(doc, username)
	if err == nil {
		s.info("cron", "cron rules disabled for user", "user", username)
	}
	return err
}

func (s *server) removeCronRulesForUser(username string) error {
	s.configFileMu.Lock()
	defer s.configFileMu.Unlock()
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return err
	}
	remove := map[int]bool{}
	for _, block := range doc.Blocks {
		if block.Rule.User == username {
			remove[block.MetaIndex] = true
			remove[block.RuleIndex] = true
		}
	}
	if len(remove) == 0 {
		return nil
	}
	kept := make([]string, 0, len(doc.Lines)-len(remove))
	for i, line := range doc.Lines {
		if !remove[i] {
			kept = append(kept, line)
		}
	}
	doc.Lines = kept
	_, _, err = s.saveCronConfigDoc(doc, username)
	if err == nil {
		s.info("cron", "cron rules removed for user", "user", username, "rules", len(remove)/2)
	}
	return err
}
