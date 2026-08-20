package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newDatabaseTestServer(t *testing.T) *server {
	t.Helper()
	tmp := t.TempDir()
	userDB, err := openUserDatabase(filepath.Join(tmp, "data", "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = userDB.Close() })
	historyDB, err := openHistoryDatabase(filepath.Join(tmp, "data", "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = historyDB.Close() })
	app, err := newAppConfigStore(filepath.Join(tmp, "config", "clamavweb.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		cfg:               config{JobsDir: filepath.Join(tmp, "jobs"), LogDir: filepath.Join(tmp, "log"), AdminRegisterToken: "test-admin-token"},
		userDB:            userDB,
		historyDB:         historyDB,
		appConfig:         app,
		batches:           map[string]*scanBatch{},
		resultLookups:     map[string]*resultLookup{},
		quarantineLookups: map[string]*quarantineLookup{},
		loginLimiter:      newLoginLimiter(),
	}
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s.history = &historyIndexer{db: historyDB, jobsDir: s.cfg.JobsDir}
	return s
}

func TestFirstRegistrationCreatesAdminAndCookieLogin(t *testing.T) {
	s := newDatabaseTestServer(t)
	register := httptest.NewRecorder()
	s.handleAuthRegister(register, httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"username":"alice","password":"correct horse battery staple","token":"test-admin-token"}`)))
	if register.Code != http.StatusCreated {
		t.Fatalf("register failed: %d %s", register.Code, register.Body.String())
	}
	var role string
	if err := s.userDB.QueryRow("SELECT role FROM users WHERE username='alice'").Scan(&role); err != nil || role != "admin" {
		t.Fatalf("expected first user to be admin, role=%q err=%v", role, err)
	}
	if _, err := s.userDB.Exec("UPDATE users SET timedock_account='Jason' WHERE username='alice'"); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	s.handleAuthLogin(login, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"alice","password":"correct horse battery staple"}`)))
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login failed: %d %s", login.Code, login.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	request.AddCookie(login.Result().Cookies()[0])
	response := httptest.NewRecorder()
	s.requireAuth(http.HandlerFunc(s.handleAuthMe)).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"username":"alice"`) || !strings.Contains(response.Body.String(), `"timedock_account":"Jason"`) {
		t.Fatalf("authenticated request failed: %d %s", response.Code, response.Body.String())
	}
}

func TestFirstRegistrationRequiresConfiguredToken(t *testing.T) {
	for _, test := range []struct {
		name, configured, provided string
		want                       int
	}{
		{"missing environment token", "", "", http.StatusForbidden},
		{"missing request token", "test-admin-token", "", http.StatusForbidden},
		{"incorrect token", "test-admin-token", "wrong", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newDatabaseTestServer(t)
			s.cfg.AdminRegisterToken = test.configured
			body := `{"username":"alice","password":"correct horse battery staple","token":"` + test.provided + `"}`
			response := httptest.NewRecorder()
			s.handleAuthRegister(response, httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(body)))
			if response.Code != test.want {
				t.Fatalf("expected %d, got %d: %s", test.want, response.Code, response.Body.String())
			}
			var count int
			if err := s.userDB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid token must not create a user, count=%d err=%v", count, err)
			}
		})
	}
}

func TestFirstRegistrationDoesNotReopenAfterFirstRunCompleted(t *testing.T) {
	s := newDatabaseTestServer(t)
	cfg := s.appConfig.get()
	cfg.WebFirstRunCompleted = 2
	if err := s.appConfig.update(cfg); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.handleAuthRegister(response, httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"username":"alice","password":"correct horse battery staple","token":"test-admin-token"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected completed first-run state to return 409, got %d: %s", response.Code, response.Body.String())
	}
}

func TestFirstRegistrationIsSerialized(t *testing.T) {
	s := newDatabaseTestServer(t)
	var workers sync.WaitGroup
	statuses := make(chan int, 2)
	for _, username := range []string{"alice", "bob"} {
		workers.Add(1)
		go func(username string) {
			defer workers.Done()
			body := `{"username":"` + username + `","password":"correct horse battery staple","token":"test-admin-token"}`
			response := httptest.NewRecorder()
			s.handleAuthRegister(response, httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(body)))
			statuses <- response.Code
		}(username)
	}
	workers.Wait()
	close(statuses)
	created := 0
	for status := range statuses {
		if status == http.StatusCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("expected exactly one first administrator, got %d", created)
	}
	var count int
	if err := s.userDB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected one user row, count=%d err=%v", count, err)
	}
}

func TestLoginLimitUsesIPInsteadOfUsername(t *testing.T) {
	s := newDatabaseTestServer(t)
	cfg := s.appConfig.get()
	cfg.WebLoginMaxTries = 2
	cfg.WebLoginMaxTriesOverall = 10
	cfg.WebLoginCooldownInterval = 60
	if err := s.appConfig.update(cfg); err != nil {
		t.Fatal(err)
	}
	for attempt, username := range []string{"missing-one", "missing-two"} {
		request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"`+username+`","password":"incorrect password"}`))
		request.RemoteAddr = "192.0.2.10:1234"
		response := httptest.NewRecorder()
		s.handleAuthLogin(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d expected 401, got %d: %s", attempt+1, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"another-name","password":"incorrect password"}`))
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	s.handleAuthLogin(response, request)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("username changes must not bypass the source limit: %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if len(s.loginLimiter.byIP) != 1 {
		t.Fatalf("one source should create one limiter entry, got %d", len(s.loginLimiter.byIP))
	}
}

func TestHistoryIndexIsVersion2AndOwnerScoped(t *testing.T) {
	s := newDatabaseTestServer(t)
	jobs := map[string]string{
		"manual-1783433000.json": `{"version":2,"job_id":"manual-1783433000","type":"manual","status":"finished","result":"clean","action":"warn","started_at":1783433000,"finished_at":1783433010,"user":"alice"}`,
		"cron-1783432000.json":   `{"version":2,"job_id":"cron-1783432000","type":"cron","status":"finished","result":"found","action":"move","started_at":1783432000,"finished_at":1783432010,"user":"bob"}`,
		"manual-1783431000.json": `{"version":1,"job_id":"manual-1783431000","type":"manual","user":"alice"}`,
	}
	for name, body := range jobs {
		if err := os.WriteFile(filepath.Join(s.cfg.JobsDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.history.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.readResultLogItems(resultScope{All: true}, "alice")
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != "manual-1783433000" {
		t.Fatalf("unexpected owner-scoped history: total=%d items=%#v err=%v", total, items, err)
	}
}

func TestRandomUserSchedulerAlwaysSelectsOwnerHead(t *testing.T) {
	s := &server{
		queuedBatchIDs: []string{"alice-first", "alice-second", "bob-first"},
		batches: map[string]*scanBatch{
			"alice-first":  {ID: "alice-first", User: "alice"},
			"alice-second": {ID: "alice-second", User: "alice"},
			"bob-first":    {ID: "bob-first", User: "bob"},
		},
	}
	for i := 0; i < 100; i++ {
		id, ok := s.nextQueuedBatchID()
		if !ok || (id != "alice-first" && id != "bob-first") {
			t.Fatalf("scheduler selected a non-head task: %q", id)
		}
	}
}

func TestQuarantineRecordsAreOwnerScoped(t *testing.T) {
	tmp := t.TempDir()
	for _, item := range []struct{ name, owner string }{{"a.dat", "alice"}, {"b.dat", "bob"}} {
		if err := os.WriteFile(filepath.Join(tmp, item.name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, item.name+".rec"), []byte(`"/scan/`+item.name+`" `+item.owner+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &server{cfg: config{QuarantineDir: tmp}}
	items, err := s.readQuarantineSubjects("alice")
	if err != nil || len(items) != 1 || items[0].Name != "a.dat" {
		t.Fatalf("unexpected quarantine view: %#v err=%v", items, err)
	}
	if err := s.deleteQuarantineSubject("b.dat", "alice"); !os.IsNotExist(err) {
		t.Fatalf("expected cross-owner delete to look missing, got %v", err)
	}
}

func TestCronAndWhitelistReadsAreOwnerScoped(t *testing.T) {
	tmp := t.TempDir()
	cronFile := filepath.Join(tmp, "cron.conf")
	cronBody := strings.Join([]string{
		"# scanner-cron-rule id=aaaaaaaaaaaaaaaa enabled=true",
		"0 1 * * * /scan warn alice Y",
		"# scanner-cron-rule id=bbbbbbbbbbbbbbbb enabled=true",
		"0 2 * * * /scan move bob N",
	}, "\n") + "\n"
	if err := os.WriteFile(cronFile, []byte(cronBody), 0o600); err != nil {
		t.Fatal(err)
	}
	excludeFile := filepath.Join(tmp, "exclude.conf")
	if err := os.WriteFile(excludeFile, []byte("/scan/a alice\n/scan/b bob\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: config{CronConfigFile: cronFile, ExcludeConfig: excludeFile}}
	rules, err := s.readCronRules("alice")
	if err != nil || len(rules) != 1 || rules[0].ID != "aaaaaaaaaaaaaaaa" || !rules[0].Wake {
		t.Fatalf("unexpected cron view: %#v err=%v", rules, err)
	}
	entries, err := s.readWhitelistEntries("alice")
	if err != nil || len(entries) != 1 || entries[0].Path != "/scan/a" {
		t.Fatalf("unexpected whitelist view: %#v err=%v", entries, err)
	}
}

func TestAppConfigRoundTrip(t *testing.T) {
	store, err := newAppConfigStore(filepath.Join(t.TempDir(), "clamavweb.conf"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.get()
	cfg.HistoryIndexRefreshInterval = 120
	cfg.ClamAVSleepTimer = 0
	cfg.WebFirstRunCompleted = 2
	cfg.WebLoginMaxTries = 12
	cfg.WebLoginMaxTriesOverall = 120
	cfg.WebLoginCooldownInterval = 300
	cfg.ServerTrustedReverseProxy = "192.0.2.10,2001:db8::10"
	cfg.LogFileMaxSize = 8 * 1024 * 1024
	cfg.LogFileNum = 9
	cfg.LogLevel = "debug"
	if err := store.update(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseAppConfig(string(data))
	if err != nil || parsed != cfg {
		t.Fatalf("config did not round trip: %#v err=%v", parsed, err)
	}
	if !strings.Contains(string(data), "CLAMAV_SLEEP_TIMER=0\n") {
		t.Fatalf("disabled sleep timer was not written as zero: %s", data)
	}
}

func TestAppConfigAddsMissingKeysWithoutReplacingExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clamavweb.conf")
	original := "# keep this comment\n  HISTORY_INDEX_REFRESH_INTERVAL = 120\nCLAMAV_SLEEP_TIMER=0\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}

	store, err := newAppConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.HasPrefix(content, original) {
		t.Fatalf("existing configuration was replaced:\n%s", content)
	}
	for _, expected := range []string{
		"WEB_FIRSTRUN_COMPLETED=0\n",
		"WEB_LOGIN_MAX_TRIES=10\n",
		"LOG_LEVEL=info\n",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("missing default entry %q in:\n%s", expected, content)
		}
	}
	if cfg := store.get(); cfg.HistoryIndexRefreshInterval != 120 || cfg.ClamAVSleepTimer != 0 {
		t.Fatalf("existing values were not retained: %#v", cfg)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("existing permissions changed to %o", info.Mode().Perm())
	}
}

func TestAppConfigUpdatePreservesCommentsAndUnchangedFormatting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clamavweb.conf")
	original := "# operator note\nHISTORY_INDEX_REFRESH_INTERVAL = 60\nCLAMAV_SLEEP_TIMER=3600\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newAppConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.get()
	cfg.ClamAVSleepTimer = 0
	if err := store.update(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "# operator note\nHISTORY_INDEX_REFRESH_INTERVAL = 60\nCLAMAV_SLEEP_TIMER=0\n") {
		t.Fatalf("update replaced unrelated content:\n%s", content)
	}
}

func TestAppConfigSleepTimerDefaultsAndZeroValue(t *testing.T) {
	cfg, err := parseAppConfig("")
	if err != nil || cfg.ClamAVSleepTimer != defaultClamAVSleepTimer {
		t.Fatalf("missing sleep timer did not use default: %#v err=%v", cfg, err)
	}
	if _, err := parseAppConfig("CLAMAV_SLEEP_TIMER=\n"); err == nil {
		t.Fatal("empty sleep timer configuration was accepted")
	}
	cfg, err = parseAppConfig("CLAMAV_SLEEP_TIMER=0\n")
	if err != nil || cfg.ClamAVSleepTimer != 0 {
		t.Fatalf("zero sleep timer did not disable scheduling: %#v err=%v", cfg, err)
	}
}

func TestAppConfigRejectsInvalidLoginAndProxySettings(t *testing.T) {
	base := defaultAppConfig()
	for name, mutate := range map[string]func(*appConfig){
		"zero per-IP limit":       func(cfg *appConfig) { cfg.WebLoginMaxTries = 0 },
		"overall below per-IP":    func(cfg *appConfig) { cfg.WebLoginMaxTriesOverall = cfg.WebLoginMaxTries - 1 },
		"zero cooldown":           func(cfg *appConfig) { cfg.WebLoginCooldownInterval = 0 },
		"invalid trusted proxy":   func(cfg *appConfig) { cfg.ServerTrustedReverseProxy = "proxy.example.com" },
		"trusted proxy with port": func(cfg *appConfig) { cfg.ServerTrustedReverseProxy = "192.0.2.1:8080" },
		"empty proxy list item":   func(cfg *appConfig) { cfg.ServerTrustedReverseProxy = "192.0.2.1,,192.0.2.2" },
		"small log file":          func(cfg *appConfig) { cfg.LogFileMaxSize = minLogFileMaxSize - 1 },
		"zero log files":          func(cfg *appConfig) { cfg.LogFileNum = 0 },
		"invalid log level":       func(cfg *appConfig) { cfg.LogLevel = "verbose" },
		"short sleep timer":       func(cfg *appConfig) { cfg.ClamAVSleepTimer = minClamAVSleepTimer - 1 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if err := validateAppConfig(cfg); err == nil {
				t.Fatalf("expected invalid configuration to be rejected: %#v", cfg)
			}
		})
	}
}

func TestServiceConfigUpdatesLoginLimitsAndTrustedProxy(t *testing.T) {
	s := newDatabaseTestServer(t)
	s.historyIntervalChanged = make(chan struct{}, 1)
	s.clamavSleepTimer = newClamAVSleepTimerState()
	if allowed, _ := s.loginLimiter.allow("192.0.2.1", time.Now(), s.appConfig.get()); !allowed {
		t.Fatal("failed to seed login limiter")
	}
	request := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{
	  "history_index_refresh_interval": 120,
	  "clamav_sleep_timer": 600,
	  "web_login_max_tries": 5,
  "web_login_max_tries_overall": 50,
  "web_login_cooldown_interval": 120,
  "server_trusted_reverseproxy": "192.0.2.10, 2001:db8::10"
}`))
	request = request.WithContext(context.WithValue(request.Context(), actorContextKey{}, actor{Username: "admin", Role: "admin"}))
	response := httptest.NewRecorder()
	s.handleServiceConfig(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("config update failed: %d %s", response.Code, response.Body.String())
	}
	cfg := s.appConfig.get()
	if cfg.ClamAVSleepTimer != 600 || cfg.WebLoginMaxTries != 5 || cfg.WebLoginMaxTriesOverall != 50 || cfg.WebLoginCooldownInterval != 120 || cfg.ServerTrustedReverseProxy != "192.0.2.10,2001:db8::10" {
		t.Fatalf("unexpected updated config: %#v", cfg)
	}
	if len(s.loginLimiter.byIP) != 0 {
		t.Fatal("changing login limits should reset in-memory limiter state")
	}
	select {
	case <-s.historyIntervalChanged:
	default:
		t.Fatal("changing the history refresh interval did not notify the indexer")
	}
	select {
	case <-s.clamavSleepTimer.changed:
	default:
		t.Fatal("changing the ClamAV sleep timer did not notify the scheduler")
	}
	for _, key := range []string{"log_file_max_size", "log_file_num", "log_level"} {
		if strings.Contains(response.Body.String(), key) {
			t.Fatalf("file-only log setting %q leaked through the API: %s", key, response.Body.String())
		}
	}
}

func TestServiceConfigSleepTimerUsesZeroToDisable(t *testing.T) {
	s := newDatabaseTestServer(t)
	s.clamavSleepTimer = newClamAVSleepTimerState()
	actorCtx := context.WithValue(context.Background(), actorContextKey{}, actor{Username: "admin", Role: "admin"})

	emptyRequest := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"clamav_sleep_timer":""}`)).WithContext(actorCtx)
	emptyResponse := httptest.NewRecorder()
	s.handleServiceConfig(emptyResponse, emptyRequest)
	if emptyResponse.Code != http.StatusBadRequest {
		t.Fatalf("empty string should be rejected, got %d %s", emptyResponse.Code, emptyResponse.Body.String())
	}
	if s.appConfig.get().ClamAVSleepTimer != defaultClamAVSleepTimer {
		t.Fatal("rejected empty string changed the sleep timer")
	}

	zeroRequest := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"clamav_sleep_timer":0}`)).WithContext(actorCtx)
	zeroResponse := httptest.NewRecorder()
	s.handleServiceConfig(zeroResponse, zeroRequest)
	if zeroResponse.Code != http.StatusOK {
		t.Fatalf("zero sleep timer update failed: %d %s", zeroResponse.Code, zeroResponse.Body.String())
	}
	if s.appConfig.get().ClamAVSleepTimer != 0 {
		t.Fatal("zero API value did not disable the sleep timer")
	}
	body := decodeMap(t, zeroResponse)
	if timer, ok := body["clamav_sleep_timer"].(float64); !ok || timer != 0 {
		t.Fatalf("disabled sleep timer response was not numeric zero: %#v", body["clamav_sleep_timer"])
	}
	data, err := os.ReadFile(s.appConfig.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "CLAMAV_SLEEP_TIMER=0\n") {
		t.Fatalf("disabled timer was not persisted as zero: %s", data)
	}
}

func decodeMap(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
