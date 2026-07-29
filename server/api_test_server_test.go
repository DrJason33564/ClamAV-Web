package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	templateResultLookupID     = "result-test-20260728070000"
	templateQuarantineLookupID = "quarantine-test-20260728070000"
	templateLastJobID          = "manual-20260728070000"
)

var templateCronRules = []map[string]any{
	{
		"id": "testenabled00001", "enabled": true,
		"minute": "0", "hour": "2", "day": "*", "month": "*", "weekday": "*",
		"target": "/scan/documents", "action": "warn", "line": 2,
	},
	{
		"id": "testdisabled0002", "enabled": false,
		"minute": "30", "hour": "4", "day": "*", "month": "*", "weekday": "0",
		"target": "/scan/uploads", "action": "move", "line": 4,
	},
}

var templateWhitelistEntries = []map[string]any{
	{"path": "/scan/trusted", "line": 1},
	{"path": "/scan/documents/approved", "line": 2},
	{"path": "/scan/uploads/example-safe.zip", "line": 3},
}

var templateBrowseTree = map[string][]map[string]any{
	"/scan": {
		browseTemplateEntry("documents", "/scan/documents", true, 4096),
		browseTemplateEntry("uploads", "/scan/uploads", true, 4096),
		browseTemplateEntry("README.txt", "/scan/README.txt", false, 1536),
		browseTemplateEntry("sample.iso", "/scan/sample.iso", false, 734003200),
		browseTemplateEntry("test-data.zip", "/scan/test-data.zip", false, 5242880),
	},
	"/scan/documents": {
		browseTemplateEntry("approved", "/scan/documents/approved", true, 4096),
		browseTemplateEntry("reports", "/scan/documents/reports", true, 4096),
		browseTemplateEntry("contract.pdf", "/scan/documents/contract.pdf", false, 248320),
		browseTemplateEntry("notes.txt", "/scan/documents/notes.txt", false, 4096),
	},
	"/scan/uploads": {
		browseTemplateEntry("incoming", "/scan/uploads/incoming", true, 4096),
		browseTemplateEntry("archive", "/scan/uploads/archive", true, 4096),
		browseTemplateEntry("example-safe.zip", "/scan/uploads/example-safe.zip", false, 102400),
		browseTemplateEntry("photo.jpg", "/scan/uploads/photo.jpg", false, 2097152),
		browseTemplateEntry("payload.bin", "/scan/uploads/payload.bin", false, 8192),
	},
	"/scan/documents/approved": {
		browseTemplateEntry("policy.pdf", "/scan/documents/approved/policy.pdf", false, 180224),
		browseTemplateEntry("handbook.docx", "/scan/documents/approved/handbook.docx", false, 92160),
	},
	"/scan/documents/reports": {
		browseTemplateEntry("report-q1.xlsx", "/scan/documents/reports/report-q1.xlsx", false, 65536),
		browseTemplateEntry("report-q2.xlsx", "/scan/documents/reports/report-q2.xlsx", false, 69632),
		browseTemplateEntry("summary.pdf", "/scan/documents/reports/summary.pdf", false, 122880),
	},
	"/scan/uploads/incoming": {
		browseTemplateEntry("upload-001.dat", "/scan/uploads/incoming/upload-001.dat", false, 32768),
		browseTemplateEntry("upload-002.dat", "/scan/uploads/incoming/upload-002.dat", false, 49152),
	},
	"/scan/uploads/archive": {
		browseTemplateEntry("archive-01.tar", "/scan/uploads/archive/archive-01.tar", false, 1048576),
		browseTemplateEntry("archive-02.tar", "/scan/uploads/archive/archive-02.tar", false, 2097152),
		browseTemplateEntry("manifest.json", "/scan/uploads/archive/manifest.json", false, 2048),
	},
}

func browseTemplateEntry(name, path string, isDir bool, size int64) map[string]any {
	return map[string]any{
		"name": name, "path": path, "is_dir": isDir, "size": size,
		"modified": "2026-07-28T07:00:00+08:00",
	}
}

// TestTemplateAPIServer starts the template-only backend when
// SCANNER_TEST_SERVER is set. Example:
//
//	SCANNER_TEST_SERVER=1 SCANNER_TEST_ADDR=:8081 \
//	  go test -run '^TestTemplateAPIServer$' -count=1 -v
func TestTemplateAPIServer(t *testing.T) {
	if os.Getenv("SCANNER_TEST_SERVER") == "" {
		t.Skip("set SCANNER_TEST_SERVER=1 to start the template API server")
	}
	addr := strings.TrimSpace(os.Getenv("SCANNER_TEST_ADDR"))
	if addr == "" {
		addr = ":8081"
	}
	t.Logf("template API server listening on %s", addr)
	if err := http.ListenAndServe(addr, newTemplateAPIHandler()); err != nil {
		t.Fatal(err)
	}
}

func newTemplateAPIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", templateRoot)
	mux.HandleFunc("/api/status", templateStatus)
	mux.HandleFunc("/api/clamav/sleep", templateClamAVSleep)
	mux.HandleFunc("/api/clamav/wake", templateClamAVWake)
	mux.HandleFunc("/api/browse", templateBrowse)
	mux.HandleFunc("/api/scans", templateScans)
	mux.HandleFunc("/api/scans/reorder", templateScanReorder)
	mux.HandleFunc("/api/scans/cancel", templateScanCancel)
	mux.HandleFunc("/api/cron/rules", templateCronRulesHandler)
	mux.HandleFunc("/api/cron/rules/", templateCronRuleHandler)
	mux.HandleFunc("/api/cron/reload", templateCronReload)
	mux.HandleFunc("/api/whitelist", templateWhitelist)
	mux.HandleFunc("/api/results/lookups", templateResultLookupStart)
	mux.HandleFunc("/api/results/lookups/", templateResultLookup)
	mux.HandleFunc("/api/results/detection", templateDetection)
	mux.HandleFunc("/api/results/clean", templateResultsClean)
	mux.HandleFunc("/api/quarantine/lookups", templateQuarantineLookupStart)
	mux.HandleFunc("/api/quarantine/lookups/", templateQuarantineLookup)
	mux.HandleFunc("/api/quarantine/delete/", templateQuarantineAction)
	mux.HandleFunc("/api/quarantine/recover/", templateQuarantineAction)
	mux.HandleFunc("/api/quarantine/clean", templateQuarantineClean)
	return acceptAnyBasicAuth(mux)
}

func acceptAnyBasicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="clamav-test"`)
			writeTemplateJSON(w, http.StatusUnauthorized, map[string]any{"error": "basic auth is required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func templateRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !templateMethod(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><html><body><h1>ClamAV Template API Test Server</h1></body></html>"))
}

func templateStatus(w http.ResponseWriter, r *http.Request) {
	if !templateMethod(w, r, http.MethodGet) {
		return
	}
	now := time.Now()
	timestamp := now.Format(time.RFC3339)
	states := []struct {
		status  string
		message string
	}{
		{"ready", "clamd is ready"},
		{"error", "clamd test connection failed"},
		{"timeout", "clamd ping timed out"},
	}
	state := states[rand.Intn(len(states))]
	writeTemplateJSON(w, http.StatusOK, map[string]any{
		"source": map[string]any{
			"version":    1,
			"updated_at": timestamp,
			"clamd": map[string]any{
				"status":          state.status,
				"last_checked_at": timestamp,
				"message":         state.message,
			},
			"scan": map[string]any{
				"active_job_id":   nil,
				"last_job_id":     templateLastJobID,
				"last_job_status": "finished",
				"last_job_result": "clean",
			},
		},
		"ping":         state.status,
		"ping_message": state.message,
		"checked_at":   timestamp,
	})
}

func templateClamAVSleep(w http.ResponseWriter, r *http.Request) {
	if !templateMethod(w, r, http.MethodPost) {
		return
	}
	writeTemplateJSON(w, http.StatusOK, map[string]any{
		"status": "sleeping", "message": "ClamAV entered sleep mode (test only)",
	})
}

func templateClamAVWake(w http.ResponseWriter, r *http.Request) {
	if !templateMethod(w, r, http.MethodPost) {
		return
	}
	writeTemplateJSON(w, http.StatusOK, map[string]any{
		"status": "awake", "message": "ClamAV woke successfully (test only)",
	})
}

func templateBrowse(w http.ResponseWriter, r *http.Request) {
	if !templateMethod(w, r, http.MethodGet) {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		path = "/scan"
	}
	entries, ok := templateBrowseTree[path]
	if !ok {
		writeTemplateJSON(w, http.StatusNotFound, map[string]any{"error": "test path not found"})
		return
	}
	response := map[string]any{"path": path, "entries": entries, "roots": []string{"/scan"}}
	if path != "/scan" {
		response["parent"] = path[:strings.LastIndex(path, "/")]
	}
	writeTemplateJSON(w, http.StatusOK, response)
}

func templateScans(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		writeTemplateJSON(w, http.StatusAccepted, map[string]any{
			"id": "web-test2026072801", "status": "queued", "message": "Queued (test only; no scan started)",
		})
	case http.MethodGet:
		writeTemplateJSON(w, http.StatusOK, []map[string]any{
			{
				"id": "web-test-running", "status": "running",
				"job_ids": []string{templateLastJobID}, "targets": []string{"/scan/documents"},
				"action": "warn", "started_at": "2026-07-28T07:00:00+08:00", "queue_number": 0,
			},
			{
				"id": "web-test-queued", "status": "queued",
				"job_ids": []string{}, "targets": []string{"/scan/uploads"},
				"action": "move", "started_at": nil, "queue_number": 1,
			},
		})
	default:
		templateMethodNotAllowed(w)
	}
}

func templateScanReorder(w http.ResponseWriter, r *http.Request) {
	if templateMethod(w, r, http.MethodPost) {
		writeTemplateJSON(w, http.StatusOK, map[string]any{
			"status": "success", "message": "Queue reordered (test only; no changes made)",
		})
	}
}

func templateScanCancel(w http.ResponseWriter, r *http.Request) {
	if templateMethod(w, r, http.MethodPost) {
		writeTemplateJSON(w, http.StatusOK, map[string]any{
			"status": "success", "message": "Queued scan canceled (test only; no changes made)",
		})
	}
}

func templateCronRulesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeTemplateJSON(w, http.StatusOK, map[string]any{"rules": templateCronRules})
	case http.MethodPost:
		writeTemplateJSON(w, http.StatusCreated, map[string]any{
			"rules": templateCronRules, "message": "Cron rules reloaded: 1 rule(s). (test only; no changes made)",
		})
	default:
		templateMethodNotAllowed(w)
	}
}

func templateCronRuleHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/cron/rules/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	allowed := r.Method == http.MethodPut || r.Method == http.MethodDelete ||
		(r.Method == http.MethodPatch && strings.HasSuffix(rest, "/enabled"))
	if !allowed {
		templateMethodNotAllowed(w)
		return
	}
	writeTemplateJSON(w, http.StatusOK, map[string]any{
		"rules": templateCronRules, "message": "Cron rules reloaded: 1 rule(s). (test only; no changes made)",
	})
}

func templateCronReload(w http.ResponseWriter, r *http.Request) {
	if templateMethod(w, r, http.MethodPost) {
		writeTemplateJSON(w, http.StatusOK, map[string]any{
			"rules": templateCronRules, "message": "Cron rules reloaded: 1 rule(s). (test only)",
		})
	}
}

func templateWhitelist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
		templateMethodNotAllowed(w)
		return
	}
	response := map[string]any{"status": "success", "entries": templateWhitelistEntries}
	if r.Method != http.MethodGet {
		response["message"] = "Exclude allow-list refreshed. (test only; no changes made)"
	}
	writeTemplateJSON(w, http.StatusOK, response)
}

func templateResultLookupStart(w http.ResponseWriter, r *http.Request) {
	if templateMethod(w, r, http.MethodPost) {
		writeTemplateJSON(w, http.StatusAccepted, map[string]any{
			"status": "success", "lookup_id": templateResultLookupID,
			"message": "result list is ready; fetch /api/results/lookups/" + templateResultLookupID,
		})
	}
}

func templateResultLookup(w http.ResponseWriter, r *http.Request) {
	if !templateMethod(w, r, http.MethodGet) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/results/lookups/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	now := time.Now().Format(time.RFC3339)
	writeTemplateJSON(w, http.StatusOK, map[string]any{
		"lookup_id": id, "status": "success", "total": 20,
		"results": templateResultItems(), "started_at": now, "updated_at": now,
	})
}

func templateResultItems() []map[string]any {
	items := make([]map[string]any, 0, 20)
	results := []any{"clean", "found", "error", nil}
	baseTime := time.Date(2026, 7, 28, 7, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	for i := 0; i < 20; i++ {
		jobType := "manual"
		if i%3 == 0 {
			jobType = "cron"
		}
		date := baseTime.Add(-time.Duration(i) * time.Minute).Format("20060102150405")
		items = append(items, map[string]any{
			"id": jobType + "-" + date, "type": jobType, "date": date, "result": results[i%len(results)],
		})
	}
	return items
}

func templateDetection(w http.ResponseWriter, r *http.Request) {
	if !templateMethod(w, r, http.MethodPost) {
		return
	}
	writeTemplateJSON(w, http.StatusOK, map[string]any{
		"job_id": templateLastJobID,
		"detections": []map[string]any{
			{"source_file": "/scan/example.txt", "detection_reason": "Eicar-Test-Signature"},
		},
		"original": "Example Log File",
		"log":      "Example Log File",
	})
}

func templateResultsClean(w http.ResponseWriter, r *http.Request) {
	if templateMethod(w, r, http.MethodPost) {
		writeTemplateJSON(w, http.StatusOK, map[string]any{
			"status": "success", "deleted": 0, "message": "Test only; no files deleted",
		})
	}
}

func templateQuarantineLookupStart(w http.ResponseWriter, r *http.Request) {
	if templateMethod(w, r, http.MethodPost) {
		writeTemplateJSON(w, http.StatusAccepted, map[string]any{
			"status": "success", "lookup_id": templateQuarantineLookupID,
			"message": "quarantine list is ready; fetch /api/quarantine/lookups/" + templateQuarantineLookupID,
		})
	}
}

func templateQuarantineLookup(w http.ResponseWriter, r *http.Request) {
	if !templateMethod(w, r, http.MethodGet) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/quarantine/lookups/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	subjects := make([]map[string]any, 0, 20)
	for i := 1; i <= 20; i++ {
		subjects = append(subjects, map[string]any{
			"name":        fmt.Sprintf("quarantined-example-%02d.dat", i),
			"source_file": fmt.Sprintf("/scan/uploads/example-%02d.dat", i),
		})
	}
	now := time.Now().Format(time.RFC3339)
	writeTemplateJSON(w, http.StatusOK, map[string]any{
		"lookup_id": id, "status": "success", "subjects": subjects,
		"started_at": now, "updated_at": now,
	})
}

func templateQuarantineAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		templateMethodNotAllowed(w)
		return
	}
	writeTemplateJSON(w, http.StatusOK, map[string]any{
		"status": "success", "message": "Test only; no quarantine file changed",
	})
}

func templateQuarantineClean(w http.ResponseWriter, r *http.Request) {
	if templateMethod(w, r, http.MethodPost) {
		writeTemplateJSON(w, http.StatusOK, map[string]any{
			"status": "success", "deleted": 0, "message": "Test only; no files deleted",
		})
	}
}

func templateMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	templateMethodNotAllowed(w)
	return false
}

func templateMethodNotAllowed(w http.ResponseWriter) {
	writeTemplateJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
}

func writeTemplateJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func TestTemplateAPIResponses(t *testing.T) {
	handler := newTemplateAPIHandler()

	request := func(method, path string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.SetBasicAuth("any-user", "any-password")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		var body map[string]any
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return response, body
	}

	t.Run("basic auth is required but credentials are not validated", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/status", nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 without basic auth, got %d", response.Code)
		}
	})

	t.Run("status fields match", func(t *testing.T) {
		response, body := request(http.MethodGet, "/api/status")
		if response.Code != http.StatusOK {
			t.Fatalf("unexpected status: %d", response.Code)
		}
		source := body["source"].(map[string]any)
		clamd := source["clamd"].(map[string]any)
		if body["ping"] != clamd["status"] || body["ping_message"] != clamd["message"] {
			t.Fatalf("status fields do not match: %#v", body)
		}
		scan := source["scan"].(map[string]any)
		if scan["last_job_id"] != templateLastJobID {
			t.Fatalf("unexpected last job id: %#v", scan["last_job_id"])
		}
	})

	t.Run("clamav power endpoints return state", func(t *testing.T) {
		response, body := request(http.MethodPost, "/api/clamav/sleep")
		if response.Code != http.StatusOK || body["status"] != "sleeping" {
			t.Fatalf("unexpected sleep response: %d %#v", response.Code, body)
		}
		response, body = request(http.MethodPost, "/api/clamav/wake")
		if response.Code != http.StatusOK || body["status"] != "awake" {
			t.Fatalf("unexpected wake response: %d %#v", response.Code, body)
		}
	})

	t.Run("browse returns fixed tree", func(t *testing.T) {
		response, body := request(http.MethodGet, "/api/browse?path=/scan/documents")
		if response.Code != http.StatusOK {
			t.Fatalf("unexpected status: %d", response.Code)
		}
		if len(body["entries"].([]any)) != len(templateBrowseTree["/scan/documents"]) {
			t.Fatalf("unexpected browse entries: %#v", body["entries"])
		}
	})

	t.Run("lookups return twenty items immediately", func(t *testing.T) {
		response, start := request(http.MethodPost, "/api/results/lookups")
		if response.Code != http.StatusAccepted || start["status"] != "success" {
			t.Fatalf("unexpected lookup start: %d %#v", response.Code, start)
		}
		response, lookup := request(http.MethodGet, "/api/results/lookups/"+start["lookup_id"].(string))
		if response.Code != http.StatusOK || len(lookup["results"].([]any)) != 20 {
			t.Fatalf("unexpected lookup result: %d %#v", response.Code, lookup)
		}
	})

	t.Run("detection always returns example log", func(t *testing.T) {
		response, body := request(http.MethodPost, "/api/results/detection")
		if response.Code != http.StatusOK || body["log"] != "Example Log File" {
			t.Fatalf("unexpected detection response: %d %#v", response.Code, body)
		}
	})

	t.Run("every documented API route responds", func(t *testing.T) {
		routes := []struct {
			method string
			path   string
			status int
		}{
			{http.MethodGet, "/api/status", http.StatusOK},
			{http.MethodGet, "/api/browse", http.StatusOK},
			{http.MethodPost, "/api/scans", http.StatusAccepted},
			{http.MethodGet, "/api/scans", http.StatusOK},
			{http.MethodPost, "/api/scans/reorder", http.StatusOK},
			{http.MethodPost, "/api/scans/cancel", http.StatusOK},
			{http.MethodGet, "/api/cron/rules", http.StatusOK},
			{http.MethodPost, "/api/cron/rules", http.StatusCreated},
			{http.MethodPut, "/api/cron/rules/testenabled00001", http.StatusOK},
			{http.MethodPatch, "/api/cron/rules/testenabled00001/enabled", http.StatusOK},
			{http.MethodDelete, "/api/cron/rules/testenabled00001", http.StatusOK},
			{http.MethodPost, "/api/cron/reload", http.StatusOK},
			{http.MethodGet, "/api/whitelist", http.StatusOK},
			{http.MethodPost, "/api/whitelist", http.StatusOK},
			{http.MethodDelete, "/api/whitelist", http.StatusOK},
			{http.MethodPost, "/api/results/lookups", http.StatusAccepted},
			{http.MethodGet, "/api/results/lookups/" + templateResultLookupID, http.StatusOK},
			{http.MethodPost, "/api/results/detection", http.StatusOK},
			{http.MethodPost, "/api/results/clean", http.StatusOK},
			{http.MethodPost, "/api/quarantine/clean", http.StatusOK},
			{http.MethodPost, "/api/quarantine/lookups", http.StatusAccepted},
			{http.MethodGet, "/api/quarantine/lookups/" + templateQuarantineLookupID, http.StatusOK},
			{http.MethodPost, "/api/quarantine/delete/example.dat", http.StatusOK},
			{http.MethodPost, "/api/quarantine/recover/example.dat", http.StatusOK},
		}
		for _, route := range routes {
			route := route
			t.Run(route.method+" "+route.path, func(t *testing.T) {
				req := httptest.NewRequest(route.method, route.path, nil)
				req.SetBasicAuth("ignored-user", "ignored-password")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, req)
				if response.Code != route.status {
					t.Fatalf("expected %d, got %d", route.status, response.Code)
				}
				if !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
					t.Fatalf("expected JSON response, got %q", response.Header().Get("Content-Type"))
				}
			})
		}
	})
}
