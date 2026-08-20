package main

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

func (s *server) debug(module, message string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Debug(message, append([]any{"module", module}, args...)...)
	}
}

func (s *server) info(module, message string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Info(message, append([]any{"module", module}, args...)...)
	}
}

func (s *server) warn(module, message string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Warn(message, append([]any{"module", module}, args...)...)
	}
}

func (s *server) error(module, message string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Error(message, append([]any{"module", module}, args...)...)
	}
}

type requestLogInfo struct {
	username string
}

type responseLogWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseLogWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseLogWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController retain optional capabilities such as
// flushing and hijacking when a handler needs them.
func (w *responseLogWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestInfo := &requestLogInfo{}
		r = r.WithContext(withRequestLogInfo(r.Context(), requestInfo))
		wrapped := &responseLogWriter{ResponseWriter: w}
		next.ServeHTTP(wrapped, r)
		if wrapped.status == 0 {
			wrapped.status = http.StatusOK
		}
		peerIP, clientIP, source, trusted := requestIPs(r, s.appConfig.get().ServerTrustedReverseProxy)
		args := []any{
			"module", "http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"response_bytes", wrapped.bytes,
			"peer_ip", peerIP,
			"client_ip", clientIP,
			"client_ip_source", source,
			"trusted_proxy", trusted,
		}
		if requestInfo.username != "" {
			args = append(args, "user", requestInfo.username)
		}
		switch {
		case wrapped.status >= http.StatusInternalServerError:
			s.logger.Error("http request completed", args...)
		case wrapped.status == http.StatusUnauthorized || wrapped.status == http.StatusForbidden || wrapped.status == http.StatusTooManyRequests:
			s.logger.Warn("http request rejected", args...)
		case r.Method == http.MethodGet && wrapped.status < http.StatusBadRequest:
			// Polling, list reads, and static asset requests are intentionally
			// debug-only to keep the default info log operationally useful.
			s.logger.Debug("http request completed", args...)
		default:
			s.logger.Info("http request completed", args...)
		}
	})
}

type requestLogContextKey struct{}

func withRequestLogInfo(ctx context.Context, info *requestLogInfo) context.Context {
	return context.WithValue(ctx, requestLogContextKey{}, info)
}

func setRequestLogActor(r *http.Request, username string) {
	if info, ok := r.Context().Value(requestLogContextKey{}).(*requestLogInfo); ok {
		info.username = username
	}
}

func requestIPs(r *http.Request, trustedProxy string) (peerIP, clientIP, source string, trusted bool) {
	peerIP = canonicalAddress(r.RemoteAddr)
	clientIP = peerIP
	source = "remote_addr"
	remote := addressIP(remoteHost(r.RemoteAddr))
	if remote == nil || !trustedProxyContains(trustedProxy, remote) {
		return peerIP, clientIP, source, false
	}
	trusted = true
	forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
	if ip := addressIP(forwarded); ip != nil {
		clientIP = ip.String()
		source = "x_forwarded_for"
	}
	return peerIP, clientIP, source, trusted
}

// Compile-time checks ensure wrappers continue to support the common optional
// interfaces used by net/http handlers.
func (w *responseLogWriter) Flush() {
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *responseLogWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

var _ http.Flusher = (*responseLogWriter)(nil)
var _ http.Hijacker = (*responseLogWriter)(nil)
