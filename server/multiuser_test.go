package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newDatabaseTestServer(t *testing.T) *server {
	t.Helper()
	tmp := t.TempDir()
	db, err := openDatabase(filepath.Join(tmp, "data", "clamavweb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	app, err := newAppConfigStore(filepath.Join(tmp, "config", "clamavweb.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		cfg:               config{JobsDir: filepath.Join(tmp, "jobs"), LogDir: filepath.Join(tmp, "log")},
		db:                db,
		appConfig:         app,
		batches:           map[string]*scanBatch{},
		resultLookups:     map[string]*resultLookup{},
		quarantineLookups: map[string]*quarantineLookup{},
	}
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s.history = &historyIndexer{db: db, jobsDir: s.cfg.JobsDir}
	return s
}

func TestFirstRegistrationCreatesAdminAndCookieLogin(t *testing.T) {
	s := newDatabaseTestServer(t)
	register := httptest.NewRecorder()
	s.handleAuthRegister(register, httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"username":"alice","password":"correct horse battery staple"}`)))
	if register.Code != http.StatusCreated {
		t.Fatalf("register failed: %d %s", register.Code, register.Body.String())
	}
	var role string
	if err := s.db.QueryRow("SELECT role FROM users WHERE username='alice'").Scan(&role); err != nil || role != "admin" {
		t.Fatalf("expected first user to be admin, role=%q err=%v", role, err)
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
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"username":"alice"`) {
		t.Fatalf("authenticated request failed: %d %s", response.Code, response.Body.String())
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
		"0 1 * * * /scan warn alice",
		"# scanner-cron-rule id=bbbbbbbbbbbbbbbb enabled=true",
		"0 2 * * * /scan move bob",
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
	if err != nil || len(rules) != 1 || rules[0].ID != "aaaaaaaaaaaaaaaa" {
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
	cfg.WebFirstRunCompleted = 2
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
}

func decodeMap(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
