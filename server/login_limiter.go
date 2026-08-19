package main

import (
	"context"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const loginRateWindow = 10 * time.Minute

type loginIPState struct {
	Attempts      int
	WindowEndsAt  time.Time
	CooldownUntil time.Time
}

type loginLimiter struct {
	mu                  sync.Mutex
	byIP                map[string]loginIPState
	globalAttempts      int
	globalWindowEndsAt  time.Time
	globalCooldownUntil time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{byIP: make(map[string]loginIPState)}
}

// allow atomically checks and consumes one login attempt. The request which
// reaches a configured limit is processed; subsequent requests enter cooldown.
func (l *loginLimiter) allow(ip string, now time.Time, cfg appConfig) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Before(l.globalCooldownUntil) {
		return false, l.globalCooldownUntil.Sub(now)
	}
	if !l.globalCooldownUntil.IsZero() {
		l.resetLocked()
	}
	if l.globalWindowEndsAt.IsZero() || !now.Before(l.globalWindowEndsAt) {
		l.globalAttempts = 0
		l.globalWindowEndsAt = now.Add(loginRateWindow)
	}

	state, exists := l.byIP[ip]
	if exists && now.Before(state.CooldownUntil) {
		return false, state.CooldownUntil.Sub(now)
	}
	// Finishing a cooldown starts a fresh allowance rather than permitting only
	// one request per cooldown until the original ten-minute window expires.
	if !exists || !state.CooldownUntil.IsZero() || !now.Before(state.WindowEndsAt) {
		state = loginIPState{WindowEndsAt: now.Add(loginRateWindow)}
	}

	// A new source consumes at least one global attempt, so this condition is
	// normally reached together with the global limit. Keep the explicit cap to
	// remain safe if limits are lowered while the process is running.
	if !exists && len(l.byIP) >= cfg.WebLoginMaxTriesOverall {
		l.startGlobalCooldownLocked(now, cfg)
		return false, time.Duration(cfg.WebLoginCooldownInterval) * time.Second
	}

	state.Attempts++
	l.globalAttempts++
	if state.Attempts >= cfg.WebLoginMaxTries {
		state.CooldownUntil = now.Add(time.Duration(cfg.WebLoginCooldownInterval) * time.Second)
	}
	l.byIP[ip] = state
	if l.globalAttempts >= cfg.WebLoginMaxTriesOverall {
		l.startGlobalCooldownLocked(now, cfg)
	}
	return true, 0
}

func (l *loginLimiter) startGlobalCooldownLocked(now time.Time, cfg appConfig) {
	l.globalCooldownUntil = now.Add(time.Duration(cfg.WebLoginCooldownInterval) * time.Second)
	l.globalAttempts = 0
	l.globalWindowEndsAt = time.Time{}
	// No per-IP entry is useful while every login is rejected. Clearing here
	// also releases attacker-created source entries as soon as the global limit
	// is reached.
	clear(l.byIP)
}

func (l *loginLimiter) reset() {
	l.mu.Lock()
	l.resetLocked()
	l.mu.Unlock()
}

func (l *loginLimiter) resetLocked() {
	clear(l.byIP)
	l.globalAttempts = 0
	l.globalWindowEndsAt = time.Time{}
	l.globalCooldownUntil = time.Time{}
}

func (l *loginLimiter) cleanup(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.globalCooldownUntil.IsZero() && !now.Before(l.globalCooldownUntil) {
		l.resetLocked()
		return
	}
	for ip, state := range l.byIP {
		if !now.Before(state.WindowEndsAt) && !now.Before(state.CooldownUntil) {
			delete(l.byIP, ip)
		}
	}
	if !l.globalWindowEndsAt.IsZero() && !now.Before(l.globalWindowEndsAt) {
		l.globalAttempts = 0
		l.globalWindowEndsAt = time.Time{}
	}
}

func (l *loginLimiter) runJanitor(ctx context.Context) {
	ticker := time.NewTicker(loginRateWindow)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			l.cleanup(now)
		}
	}
}

func retryAfterSeconds(wait time.Duration) string {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}

func requestClientIP(r *http.Request, trustedProxy string) string {
	remote := addressIP(remoteHost(r.RemoteAddr))
	trusted := net.ParseIP(strings.TrimSpace(trustedProxy))
	if trusted == nil || remote == nil || !remote.Equal(trusted) {
		return canonicalAddress(r.RemoteAddr)
	}
	// A trusted reverse proxy is expected to overwrite/sanitize X-Forwarded-For.
	// The first element is the original client address by the header convention.
	forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
	if ip := addressIP(forwarded); ip != nil {
		return ip.String()
	}
	return remote.String()
}

func canonicalAddress(value string) string {
	host := remoteHost(strings.TrimSpace(value))
	if ip := addressIP(host); ip != nil {
		return ip.String()
	}
	return host
}

func addressIP(value string) net.IP {
	value = strings.Trim(strings.TrimSpace(value), "[]")
	if zone := strings.LastIndexByte(value, '%'); zone >= 0 {
		value = value[:zone]
	}
	return net.ParseIP(value)
}
