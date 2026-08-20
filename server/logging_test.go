package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clamav-scanner/internal/applog"
)

func TestRequestLogUsesTrustedClientAndPeerIPs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clamavweb.log")
	logger, err := applog.New(applog.Config{Path: path, MaxSize: 1 << 20, MaxFiles: 2, Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		logger: logger,
		appConfig: &appConfigStore{cfg: appConfig{
			ServerTrustedReverseProxy: "192.0.2.10",
		}},
	}
	handler := s.logRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	request.RemoteAddr = "192.0.2.10:4321"
	request.Header.Set("X-Forwarded-For", "203.0.113.25, 192.0.2.10")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{
		"level=DEBUG",
		"peer_ip=192.0.2.10",
		"client_ip=203.0.113.25",
		"client_ip_source=x_forwarded_for",
		"trusted_proxy=true",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("request log does not contain %q: %s", expected, text)
		}
	}
}

func TestRequestIPsIgnoreUntrustedForwardedHeader(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	request.RemoteAddr = "198.51.100.4:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.25")
	peer, client, source, trusted := requestIPs(request, "192.0.2.10")
	if peer != "198.51.100.4" || client != peer || source != "remote_addr" || trusted {
		t.Fatalf("unexpected IP decision: peer=%q client=%q source=%q trusted=%t", peer, client, source, trusted)
	}
}
