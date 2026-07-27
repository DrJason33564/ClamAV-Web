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
		rules, err := s.readCronRules()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, cronRulesResponse{Rules: rules})
	case http.MethodPost:
		var req cronRule
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		rules, message, err := s.addCronRule(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
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
		rules, message, err := s.setCronRuleEnabled(id, req.Enabled)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
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
		rules, message, err := s.updateCronRule(id, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, cronRulesResponse{Rules: rules, Message: message})
	case http.MethodDelete:
		rules, message, err := s.deleteCronRule(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
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
	message, err := s.runCronScript("reload")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rules, readErr := s.readCronRules()
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

func (s *server) readCronRules() ([]cronRule, error) {
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return nil, err
	}
	rules := make([]cronRule, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
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
	if len(fields) < 7 {
		return cronRule{}, errors.New("expected minute hour day month weekday target action")
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
	if len(actionFields) != 1 {
		return cronRule{}, errors.New("expected exactly one action")
	}
	rule.Target = target
	rule.Action = actionFields[0]
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

func (s *server) addCronRule(req cronRule) ([]cronRule, string, error) {
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return nil, "", err
	}
	rule, err := s.prepareCronRule(req)
	if err != nil {
		return nil, "", err
	}
	rule.ID = uniqueCronRuleID(doc.Blocks)
	doc.Lines = append(doc.Lines, cronMetaLine(rule), cronConfigLine(rule))
	return s.saveCronConfigDoc(doc)
}

func (s *server) updateCronRule(id string, req cronRule) ([]cronRule, string, error) {
	doc, block, err := s.findCronRuleBlock(id)
	if err != nil {
		return nil, "", err
	}
	rule, err := s.prepareCronRule(req)
	if err != nil {
		return nil, "", err
	}
	rule.ID = id
	doc.Lines[block.MetaIndex] = cronMetaLine(rule)
	doc.Lines[block.RuleIndex] = cronConfigLine(rule)
	return s.saveCronConfigDoc(doc)
}

func (s *server) setCronRuleEnabled(id string, enabled bool) ([]cronRule, string, error) {
	doc, block, err := s.findCronRuleBlock(id)
	if err != nil {
		return nil, "", err
	}
	rule := block.Rule
	rule.Enabled = enabled
	doc.Lines[block.MetaIndex] = cronMetaLine(rule)
	doc.Lines[block.RuleIndex] = cronConfigLine(rule)
	return s.saveCronConfigDoc(doc)
}

func (s *server) deleteCronRule(id string) ([]cronRule, string, error) {
	doc, block, err := s.findCronRuleBlock(id)
	if err != nil {
		return nil, "", err
	}
	doc.Lines = append(doc.Lines[:block.MetaIndex], doc.Lines[block.RuleIndex+1:]...)
	return s.saveCronConfigDoc(doc)
}

func (s *server) findCronRuleBlock(id string) (cronConfigDoc, cronRuleBlock, error) {
	if !validCronRuleID(id) {
		return cronConfigDoc{}, cronRuleBlock{}, errors.New("invalid rule id")
	}
	doc, err := s.readCronConfigDoc()
	if err != nil {
		return cronConfigDoc{}, cronRuleBlock{}, err
	}
	for _, block := range doc.Blocks {
		if block.Rule.ID == id {
			return doc, block, nil
		}
	}
	return doc, cronRuleBlock{}, errors.New("cron rule not found")
}

func (s *server) prepareCronRule(req cronRule) (cronRule, error) {
	rule := cronRule{
		Enabled: req.Enabled,
		Minute:  strings.TrimSpace(req.Minute),
		Hour:    strings.TrimSpace(req.Hour),
		Day:     strings.TrimSpace(req.Day),
		Month:   strings.TrimSpace(req.Month),
		Weekday: strings.TrimSpace(req.Weekday),
		Target:  strings.TrimSpace(req.Target),
		Action:  strings.TrimSpace(req.Action),
	}
	if rule.Action == "" {
		rule.Action = "warn"
	}
	if err := validateCronRuleFields(rule); err != nil {
		return cronRule{}, err
	}
	target, err := s.safePath(rule.Target)
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

func (s *server) saveCronConfigDoc(doc cronConfigDoc) ([]cronRule, string, error) {
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
	rules, err := s.readCronRules()
	if err != nil {
		return nil, "", err
	}
	return rules, message, nil
}

func (s *server) runCronScript(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.cfg.CronScript, args...)
	out, err := cmd.CombinedOutput()
	message := cronScriptMessage(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return message, errors.New("cron command timed out")
	}
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		return message, errors.New(message)
	}
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
	line := fmt.Sprintf("%s %s %s %s %s %s %s",
		rule.Minute,
		rule.Hour,
		rule.Day,
		rule.Month,
		rule.Weekday,
		quoteCronTarget(rule.Target),
		rule.Action,
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
