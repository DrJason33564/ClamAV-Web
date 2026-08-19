package main

import (
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func loginLimitTestConfig(perIP, overall, cooldown int) appConfig {
	cfg := defaultAppConfig()
	cfg.WebLoginMaxTries = perIP
	cfg.WebLoginMaxTriesOverall = overall
	cfg.WebLoginCooldownInterval = cooldown
	return cfg
}

func TestLoginLimiterEnforcesGlobalLimitConcurrently(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter()
	cfg := loginLimitTestConfig(100, 20, 60)
	var accepted atomic.Int32
	var workers sync.WaitGroup
	for index := 0; index < 100; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			if allowed, _ := limiter.allow("192.0.2."+strconv.Itoa(index+1), now, cfg); allowed {
				accepted.Add(1)
			}
		}(index)
	}
	workers.Wait()
	if accepted.Load() != 20 {
		t.Fatalf("expected exactly 20 accepted requests, got %d", accepted.Load())
	}
	if len(limiter.byIP) != 0 {
		t.Fatal("global limit should leave no source entries")
	}
}

func TestLoginLimiterSeparatesPerIPAndGlobalCooldowns(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter()
	cfg := loginLimitTestConfig(2, 10, 60)

	for attempt := 1; attempt <= 2; attempt++ {
		if allowed, _ := limiter.allow("192.0.2.1", now, cfg); !allowed {
			t.Fatalf("expected attempt %d to be accepted", attempt)
		}
	}
	if allowed, wait := limiter.allow("192.0.2.1", now, cfg); allowed || wait != time.Minute {
		t.Fatalf("expected source cooldown, allowed=%v wait=%s", allowed, wait)
	}
	if allowed, _ := limiter.allow("192.0.2.2", now, cfg); !allowed {
		t.Fatal("one source cooldown must not block another source")
	}

	limiter = newLoginLimiter()
	cfg = loginLimitTestConfig(3, 3, 90)
	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		if allowed, _ := limiter.allow(ip, now, cfg); !allowed {
			t.Fatalf("expected limit-triggering request from %s to be accepted", ip)
		}
	}
	if len(limiter.byIP) != 0 {
		t.Fatalf("global cooldown must immediately clear source entries: %#v", limiter.byIP)
	}
	if allowed, wait := limiter.allow("192.0.2.4", now, cfg); allowed || wait != 90*time.Second {
		t.Fatalf("expected global cooldown, allowed=%v wait=%s", allowed, wait)
	}
	if len(limiter.byIP) != 0 {
		t.Fatal("global cooldown must not create source entries")
	}
}

func TestLoginLimiterWindowCleanupAndCapacity(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter()
	cfg := loginLimitTestConfig(10, 2, 60)
	if allowed, _ := limiter.allow("192.0.2.1", now, cfg); !allowed {
		t.Fatal("first source was rejected")
	}
	if allowed, _ := limiter.allow("192.0.2.2", now, cfg); !allowed {
		t.Fatal("second source was rejected")
	}
	if len(limiter.byIP) != 0 { // The second accepted request reaches the global limit.
		t.Fatal("expected global limit to clear the capacity map")
	}
	limiter.cleanup(now.Add(11 * time.Minute))
	if allowed, _ := limiter.allow("192.0.2.3", now.Add(11*time.Minute), cfg); !allowed {
		t.Fatal("expired cooldown/window should accept a new request")
	}
}

func TestRequestClientIPTrustsOnlyConfiguredDirectProxy(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		trusted    string
		forwarded  string
		want       string
	}{
		{"no proxy configured", "192.0.2.10:1234", "", "198.51.100.20", "192.0.2.10"},
		{"untrusted peer", "192.0.2.10:1234", "192.0.2.11", "198.51.100.20", "192.0.2.10"},
		{"trusted ipv4 proxy", "192.0.2.10:1234", "192.0.2.10", "198.51.100.20, 192.0.2.10", "198.51.100.20"},
		{"trusted ipv6 proxy", "[2001:db8::10]:1234", "2001:db8::10", "2001:db8::20", "2001:db8::20"},
		{"missing header fallback", "[2001:db8::10]:1234", "2001:db8::10", "", "2001:db8::10"},
		{"invalid header fallback", "192.0.2.10:1234", "192.0.2.10", "not-an-ip", "192.0.2.10"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/auth/login", nil)
			r.RemoteAddr = test.remoteAddr
			if test.forwarded != "" {
				r.Header.Set("X-Forwarded-For", test.forwarded)
			}
			if got := requestClientIP(r, test.trusted); got != test.want {
				t.Fatalf("requestClientIP() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAdminRegisterTokenComparison(t *testing.T) {
	if !validAdminRegisterToken("correct-token", "correct-token") {
		t.Fatal("matching non-empty tokens should pass")
	}
	for _, test := range [][2]string{{"", ""}, {"", "configured"}, {"provided", ""}, {"wrong", "configured"}} {
		if validAdminRegisterToken(test[0], test[1]) {
			t.Fatalf("tokens %#v should be rejected", test)
		}
	}
}
