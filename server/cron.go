package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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
	UserID  string `json:"-"`
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
		rules, err := s.readCronRules(who.ID)
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
		rules, message, err := s.deleteCronRule(id, who.ID)
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
	message, err := s.reloadCron()
	if err != nil {
		s.error("cron", "cron reload failed", "error", err)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.info("cron", "cron rules reloaded")
	who, _ := actorFromRequest(r)
	rules, readErr := s.readCronRules(who.ID)
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

func (s *server) readCronRules(userIDs ...string) ([]cronRule, error) {
	userID := ""
	if len(userIDs) > 0 {
		userID = userIDs[0]
	}
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return nil, err
	}
	rules := make([]cronRule, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		if userID != "" && block.Rule.UserID != userID {
			continue
		}
		rules = append(rules, block.Rule)
	}
	return rules, nil
}

func (s *server) readCronConfigDoc() (cronConfigDoc, error) {
	return readCronConfigDoc(s.cfg.CronConfigFile)
}

func readCronConfigDoc(path string) (cronConfigDoc, error) {
	data, err := os.ReadFile(path)
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
			text := strings.TrimSpace(lines[i])
			if text == "" || strings.HasPrefix(text, "#") {
				continue
			}
			rule, err := parseCronRuleLine(text)
			if err != nil {
				return doc, fmt.Errorf("invalid cron rule at line %d: %w", i+1, err)
			}
			hash := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", i, text)))
			rule.ID = fmt.Sprintf("%x", hash[:8])
			rule.Enabled = true
			rule.Line = i + 1
			doc.Blocks = append(doc.Blocks, cronRuleBlock{MetaIndex: -1, RuleIndex: i, Rule: rule})
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
	seen := map[string]bool{}
	for _, block := range doc.Blocks {
		if seen[block.Rule.ID] {
			return doc, fmt.Errorf("duplicate cron rule id %s", block.Rule.ID)
		}
		seen[block.Rule.ID] = true
	}
	// Normalize metadata-free legacy rows in memory, so edits can disable or
	// delete them without losing their owner/ID. A later save persists metadata.
	var normalized []string
	metaAt, ruleAt := map[int]int{}, map[int]int{}
	original := append([]cronRuleBlock(nil), doc.Blocks...)
	for i, block := range original {
		ruleAt[block.RuleIndex] = i
		if block.MetaIndex >= 0 {
			metaAt[block.MetaIndex] = i
		}
	}
	for i, line := range doc.Lines {
		if j, ok := metaAt[i]; ok {
			doc.Blocks[j].MetaIndex = len(normalized)
		}
		if j, ok := ruleAt[i]; ok {
			if original[j].MetaIndex < 0 {
				doc.Blocks[j].MetaIndex = len(normalized)
				normalized = append(normalized, cronMetaLine(original[j].Rule))
			}
			doc.Blocks[j].RuleIndex = len(normalized)
		}
		normalized = append(normalized, line)
	}
	doc.Lines = normalized
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
		return cronRule{}, errors.New("expected minute hour day month weekday target action owner_user_id wake")
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
		return cronRule{}, errors.New("expected exactly one action, one owner user ID and one wake flag")
	}
	rule.Target = target
	rule.Action = actionFields[0]
	rule.UserID = actionFields[1]
	switch actionFields[2] {
	case "Y":
		rule.Wake = true
	case "N":
		rule.Wake = false
	default:
		return cronRule{}, errors.New("wake flag must be Y or N")
	}
	if !validUserID(rule.UserID) {
		return cronRule{}, errors.New("invalid rule owner user ID")
	}
	if err := validateCronRuleFields(rule); err != nil {
		return cronRule{}, err
	}
	if !filepath.IsAbs(rule.Target) || strings.ContainsAny(rule.Target, "\x00\r\n") {
		return cronRule{}, errors.New("invalid cron target")
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
	who := actor{ID: req.UserID}
	if len(actors) > 0 {
		who = actors[0]
	}
	userID := who.ID
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return nil, "", err
	}
	rule, err := s.prepareCronRule(req, who)
	if err != nil {
		return nil, "", err
	}
	rule.ID = uniqueCronRuleID(doc.Blocks)
	rule.UserID = userID
	doc.Lines = append(doc.Lines, cronMetaLine(rule), cronConfigLine(rule))
	return s.saveCronConfigDoc(doc, userID)
}

func (s *server) updateCronRule(id string, req cronRule, actors ...actor) ([]cronRule, string, error) {
	who := actor{ID: req.UserID}
	if len(actors) > 0 {
		who = actors[0]
	}
	userID := who.ID
	doc, block, err := s.findCronRuleBlock(id, userID)
	if err != nil {
		return nil, "", err
	}
	rule, err := s.prepareCronRule(req, who)
	if err != nil {
		return nil, "", err
	}
	rule.ID = id
	rule.UserID = userID
	if block.MetaIndex >= 0 {
		doc.Lines[block.MetaIndex] = cronMetaLine(rule)
	}
	doc.Lines[block.RuleIndex] = cronConfigLine(rule)
	return s.saveCronConfigDoc(doc, userID)
}

func (s *server) setCronRuleEnabled(id string, enabled bool, actors ...actor) ([]cronRule, string, error) {
	who := actor{}
	if len(actors) > 0 {
		who = actors[0]
	}
	userID := who.ID
	doc, block, err := s.findCronRuleBlock(id, userID)
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
	if block.MetaIndex >= 0 {
		doc.Lines[block.MetaIndex] = cronMetaLine(rule)
	}
	doc.Lines[block.RuleIndex] = cronConfigLine(rule)
	return s.saveCronConfigDoc(doc, userID)
}

func (s *server) deleteCronRule(id string, userIDs ...string) ([]cronRule, string, error) {
	userID := ""
	if len(userIDs) > 0 {
		userID = userIDs[0]
	}
	doc, block, err := s.findCronRuleBlock(id, userID)
	if err != nil {
		return nil, "", err
	}
	start := block.MetaIndex
	if start < 0 {
		start = block.RuleIndex
	}
	doc.Lines = append(doc.Lines[:start], doc.Lines[block.RuleIndex+1:]...)
	return s.saveCronConfigDoc(doc, userID)
}

func (s *server) findCronRuleBlock(id string, userIDs ...string) (cronConfigDoc, cronRuleBlock, error) {
	userID := ""
	if len(userIDs) > 0 {
		userID = userIDs[0]
	}
	if !validCronRuleID(id) {
		return cronConfigDoc{}, cronRuleBlock{}, errors.New("invalid rule id")
	}
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return cronConfigDoc{}, cronRuleBlock{}, err
	}
	for _, block := range doc.Blocks {
		if block.Rule.ID == id && (userID == "" || block.Rule.UserID == userID) {
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
	if strings.ContainsAny(target, "\"\r\n") {
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
		return validateCronSchedule(rule)
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

func (s *server) saveCronConfigDoc(doc cronConfigDoc, userIDs ...string) ([]cronRule, string, error) {
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

	if _, err := readCronConfigDoc(tmpPath); err != nil {
		return nil, "", err
	}
	if err := os.Rename(tmpPath, s.cfg.CronConfigFile); err != nil {
		return nil, "", err
	}
	cleanup = false

	message, err := s.reloadCron()
	if err != nil {
		return nil, "", err
	}
	rules, err := s.readCronRules(userIDs...)
	if err != nil {
		return nil, "", err
	}
	return rules, message, nil
}

// Caller holds configFileMu. Replace the scheduler snapshot only after the
// entire rule file validates, leaving the previous schedule on any error.
func (s *server) reloadCron() (string, error) {
	rules, err := s.readCronRules()
	if err != nil {
		return "", err
	}
	s.cronMu.Lock()
	s.cronRules = rules
	s.cronMu.Unlock()
	if s.cronChanged != nil {
		select {
		case s.cronChanged <- struct{}{}:
		default:
		}
	}
	return fmt.Sprintf("Cron rules reloaded: %d rule(s).", len(rules)), nil
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
		rule.UserID,
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

func (s *server) disableCronRulesForUser(userID string) error {
	s.configFileMu.Lock()
	defer s.configFileMu.Unlock()
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return err
	}
	changed := false
	for _, block := range doc.Blocks {
		if block.Rule.UserID != userID || !block.Rule.Enabled {
			continue
		}
		rule := block.Rule
		rule.Enabled = false
		if block.MetaIndex >= 0 {
			doc.Lines[block.MetaIndex] = cronMetaLine(rule)
		}
		doc.Lines[block.RuleIndex] = cronConfigLine(rule)
		changed = true
	}
	if !changed {
		return nil
	}
	_, _, err = s.saveCronConfigDoc(doc, userID)
	if err == nil {
		s.info("cron", "cron rules disabled for user", "user_id", userID)
	}
	return err
}

func (s *server) removeCronRulesForUser(userID string) error {
	s.configFileMu.Lock()
	defer s.configFileMu.Unlock()
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return err
	}
	remove := map[int]bool{}
	for _, block := range doc.Blocks {
		if block.Rule.UserID == userID {
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
	_, _, err = s.saveCronConfigDoc(doc, userID)
	if err == nil {
		s.info("cron", "cron rules removed for user", "user_id", userID, "rules", len(remove)/2)
	}
	return err
}
