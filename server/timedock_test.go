package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseYNFlag(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"N", false},
		{"Y", true},
	} {
		got, err := parseYNFlag("IS_TIMEDOCK", test.value)
		if err != nil || got != test.want {
			t.Fatalf("parseYNFlag(%q) = %v, %v; want %v", test.value, got, err, test.want)
		}
	}
	if _, err := parseYNFlag("IS_TIMEDOCK", "yes"); err == nil {
		t.Fatal("expected an unsupported environment value to fail")
	}
}

func TestNormalizeTimeDockAccount(t *testing.T) {
	for _, value := range []string{"Jason30", "杰森30", strings.Repeat("中", 64)} {
		if got, err := normalizeTimeDockAccount(value); err != nil || got != value {
			t.Fatalf("expected %q to be valid, got %q err=%v", value, got, err)
		}
	}
	if got, err := normalizeTimeDockAccount(""); err != nil || got != "" {
		t.Fatalf("expected an empty input to clear the account, got %q err=%v", got, err)
	}
	for _, value := range []string{" ", " Jason ", "Jason Smith", "Jason/Smith", "Jason.Smith", "Jason_30", strings.Repeat("中", 65)} {
		if _, err := normalizeTimeDockAccount(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestUserSchemaAndAdminTimeDockAccount(t *testing.T) {
	s := newDatabaseTestServer(t)
	now := time.Now().Unix()
	if _, err := s.userDB.Exec(`INSERT INTO users(username,password_hash,role,created_at,updated_at) VALUES('alice',NULL,'admin',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/admin/users/alice", strings.NewReader(`{"timedock_account":"杰森30"}`))
	req = req.WithContext(context.WithValue(req.Context(), actorContextKey{}, actor{ID: 1, Username: "alice", Role: "admin"}))
	response := httptest.NewRecorder()
	s.handleAdminUser(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("patch failed: %d %s", response.Code, response.Body.String())
	}
	var stored string
	if err := s.userDB.QueryRow("SELECT timedock_account FROM users WHERE username='alice'").Scan(&stored); err != nil || stored != "杰森30" {
		t.Fatalf("unexpected stored account %q err=%v", stored, err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	listReq = listReq.WithContext(context.WithValue(listReq.Context(), actorContextKey{}, actor{Username: "alice", Role: "admin"}))
	listResponse := httptest.NewRecorder()
	s.handleAdminUsers(listResponse, listReq)
	if !strings.Contains(listResponse.Body.String(), `"timedock_account":"杰森30"`) || strings.Contains(listResponse.Body.String(), "allowed_dirs") {
		t.Fatalf("unexpected users response: %s", listResponse.Body.String())
	}
}

func TestAdminCreateUserSetsTimeDockAccountAndListResult(t *testing.T) {
	s := newDatabaseTestServer(t)
	register := httptest.NewRecorder()
	s.handleAuthRegister(register, httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"username":"admin","password":"correct horse battery staple"}`)))
	if register.Code != http.StatusCreated {
		t.Fatalf("admin registration failed: %d %s", register.Code, register.Body.String())
	}
	login := httptest.NewRecorder()
	s.handleAuthLogin(login, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"correct horse battery staple"}`)))
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatalf("admin login failed: %d %s", login.Code, login.Body.String())
	}

	createRequest := httptest.NewRequest(http.MethodPost, "/api/admin/users", strings.NewReader(`{"username":"bob","password":"another secure password","role":"user","timedock_account":"鲍勃30"}`))
	createRequest.AddCookie(login.Result().Cookies()[0])
	createResponse := httptest.NewRecorder()
	s.requireAuth(http.HandlerFunc(s.handleAdminUsers)).ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated || !strings.Contains(createResponse.Body.String(), `"timedock_account":"鲍勃30"`) || !strings.Contains(createResponse.Body.String(), `"password_set":true`) {
		t.Fatalf("user creation failed: %d %s", createResponse.Code, createResponse.Body.String())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	listRequest.AddCookie(login.Result().Cookies()[0])
	listResponse := httptest.NewRecorder()
	s.requireAuth(http.HandlerFunc(s.handleAdminUsers)).ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), `"status":"success"`) || !strings.Contains(listResponse.Body.String(), `"password_set":true`) {
		t.Fatalf("unexpected users list: %d %s", listResponse.Code, listResponse.Body.String())
	}
	var stored string
	if err := s.userDB.QueryRow("SELECT timedock_account FROM users WHERE username='bob'").Scan(&stored); err != nil || stored != "鲍勃30" {
		t.Fatalf("unexpected stored account %q err=%v", stored, err)
	}
}

func TestTimeDockBrowseKeepsDeviceLevelAndFiltersAccounts(t *testing.T) {
	root := makeTimeDockTree(t)
	s := &server{cfg: config{BrowseRoots: []string{root}, IsTimeDock: true}}
	who := actor{Username: "alice", TimeDockAccount: "Jason"}

	rootResponse := browseTimeDock(t, s, root, who)
	if got := browseNames(rootResponse); got != "extdev,usb1,usb2" {
		t.Fatalf("unexpected TimeDock root entries: %s", got)
	}
	deviceResponse := browseTimeDock(t, s, filepath.Join(root, "usb1"), who)
	if got := browseNames(deviceResponse); got != "Jason" {
		t.Fatalf("unexpected device entries: %s", got)
	}
	accountResponse := browseTimeDock(t, s, filepath.Join(root, "usb1", "Jason"), who)
	if got := browseNames(accountResponse); got != "documents" {
		t.Fatalf("unexpected account entries: %s", got)
	}
	caseSensitive := browseTimeDock(t, s, root, actor{Username: "alice", TimeDockAccount: "jason"})
	if len(caseSensitive.Entries) != 0 {
		t.Fatalf("expected account folder matching to be case-sensitive: %#v", caseSensitive.Entries)
	}

	denied := httptest.NewRequest(http.MethodGet, "/api/browse?path="+filepath.Join(root, "usb3"), nil)
	denied = denied.WithContext(context.WithValue(denied.Context(), actorContextKey{}, who))
	deniedResponse := httptest.NewRecorder()
	s.handleBrowse(deniedResponse, denied)
	if deniedResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected unmatched device to be denied, got %d %s", deniedResponse.Code, deniedResponse.Body.String())
	}

	missing := httptest.NewRequest(http.MethodGet, "/api/browse?path="+root, nil)
	missing = missing.WithContext(context.WithValue(missing.Context(), actorContextKey{}, actor{Username: "alice"}))
	missingResponse := httptest.NewRecorder()
	s.handleBrowse(missingResponse, missing)
	if missingResponse.Code != http.StatusBadRequest || !strings.Contains(missingResponse.Body.String(), "Please set your TimeDock account") {
		t.Fatalf("unexpected missing-account response: %d %s", missingResponse.Code, missingResponse.Body.String())
	}
}

func TestTimeDockPathProtectionAcrossAPIs(t *testing.T) {
	root := makeTimeDockTree(t)
	s := &server{cfg: config{BrowseRoots: []string{root}, IsTimeDock: true}}
	who := actor{Username: "alice", TimeDockAccount: "Jason"}
	allowed := filepath.Join(root, "usb1", "Jason", "documents")
	denied := filepath.Join(root, "usb1", "James")

	if _, err := s.safePathForActor(allowed, who); err != nil {
		t.Fatalf("allowed scan path was rejected: %v", err)
	}
	if _, err := s.safePathForActor(denied, who); err == nil {
		t.Fatal("expected another account path to be rejected")
	}
	s.batches = make(map[string]*scanBatch)
	s.cfg.SleepLockDir = filepath.Join(root, "missing-sleep-lock")
	request := httptest.NewRequest(http.MethodPost, "/api/scans", strings.NewReader(`{"targets":["`+denied+`"],"action":"warn"}`))
	request = request.WithContext(context.WithValue(request.Context(), actorContextKey{}, who))
	response := httptest.NewRecorder()
	s.startScan(response, request)
	if response.Code != http.StatusBadRequest || len(s.batches) != 0 {
		t.Fatalf("out-of-account scan submission was not rejected: %d %s", response.Code, response.Body.String())
	}
	if _, err := s.prepareWhitelistPath(allowed, who); err != nil {
		t.Fatalf("allowed whitelist path was rejected: %v", err)
	}
	if _, err := s.prepareCronRule(cronRule{Minute: "0", Hour: "1", Day: "*", Month: "*", Weekday: "*", Target: denied, Action: "warn"}, who); err == nil {
		t.Fatal("expected another account cron target to be rejected")
	}
	if err := s.authorizeRestorePath(filepath.Join(allowed, "restored", "file.dat"), who); err != nil {
		t.Fatalf("allowed quarantine restore path was rejected: %v", err)
	}
	if err := s.authorizeRestorePath(filepath.Join(denied, "file.dat"), who); err == nil {
		t.Fatal("expected another account restore path to be rejected")
	}

	if err := os.Symlink(filepath.Join(root, "usb1", "James"), filepath.Join(root, "usb1", "Jason", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.safePathForActor(filepath.Join(root, "usb1", "Jason", "escape"), who); err == nil {
		t.Fatal("expected an in-account symlink to another account to be rejected")
	}
}

func TestStatusIncludesTimeDockMode(t *testing.T) {
	s := &server{cfg: config{IsTimeDock: true, CommandTimout: time.Millisecond}}
	response := httptest.NewRecorder()
	s.handleStatus(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["is_timedock"] != true {
		t.Fatalf("unexpected status response: %#v", body)
	}
}

func makeTimeDockTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "scan")
	for _, dir := range []string{
		"usb1/Jason/documents",
		"usb1/James",
		"usb2/Jason",
		"usb3/James",
		"extdev/Jason",
		"extdev1/Jason",
		"usbA/Jason",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "usb1"), filepath.Join(root, "usb4")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "usb5"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "usb1", "Jason"), filepath.Join(root, "usb5", "Jason")); err != nil {
		t.Fatal(err)
	}
	return root
}

func browseTimeDock(t *testing.T, s *server, path string, who actor) browseResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/browse?path="+path, nil)
	req = req.WithContext(context.WithValue(req.Context(), actorContextKey{}, who))
	response := httptest.NewRecorder()
	s.handleBrowse(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("browse %s failed: %d %s", path, response.Code, response.Body.String())
	}
	var body browseResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func browseNames(response browseResponse) string {
	names := make([]string, 0, len(response.Entries))
	for _, entry := range response.Entries {
		names = append(names, entry.Name)
	}
	return strings.Join(names, ",")
}
