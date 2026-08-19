package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/argon2"
)

const (
	sessionCookieName   = "clamavweb_session"
	sessionIdleTTL      = 2 * time.Hour
	sessionAbsoluteTTL  = 24 * time.Hour
	sessionRefreshAfter = 5 * time.Minute
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type actor struct {
	ID              int64
	Username        string
	Role            string
	TimeDockAccount string
}

type actorContextKey struct{}

type authRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	Role            string `json:"role,omitempty"`
	TimeDockAccount string `json:"timedock_account,omitempty"`
	Token           string `json:"token,omitempty"`
}

type passwordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type accountDeleteRequest struct {
	Password string `json:"password"`
}

type userPatchRequest struct {
	Role            *string `json:"role,omitempty"`
	Status          *string `json:"status,omitempty"`
	TimeDockAccount *string `json:"timedock_account,omitempty"`
	Password        *string `json:"password,omitempty"`
}

type userRecord struct {
	Username        string `json:"username"`
	Role            string `json:"role"`
	Status          string `json:"status"`
	TimeDockAccount string `json:"timedock_account"`
	PasswordSet     bool   `json:"password_set"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

func (s *server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || publicAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		who, err := s.authenticateRequest(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, errors.New("authentication required"))
			return
		}
		if isUnsafeMethod(r.Method) && !sameOriginRequest(r) {
			writeError(w, http.StatusForbidden, errors.New("cross-origin request rejected"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorContextKey{}, who)))
	})
}

func publicAPIPath(path string) bool {
	switch path {
	case "/api/auth/login", "/api/auth/register", "/api/first-run/status":
		return true
	default:
		return false
	}
}

func isUnsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func sameOriginRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true // Non-browser API clients generally do not send Origin.
	}
	parsed, err := url.Parse(origin)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host == r.Host
}

func actorFromRequest(r *http.Request) (actor, error) {
	who, ok := r.Context().Value(actorContextKey{}).(actor)
	if !ok || who.Username == "" {
		return actor{}, errors.New("authenticated user is unavailable")
	}
	return who, nil
}

func requireAdminActor(r *http.Request) (actor, error) {
	who, err := actorFromRequest(r)
	if err != nil {
		return actor{}, err
	}
	if who.Role != "admin" {
		return actor{}, errors.New("administrator access required")
	}
	return who, nil
}

func (s *server) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := requireAdminActor(r); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) authenticateRequest(r *http.Request) (actor, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return actor{}, errors.New("missing session")
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	now := time.Now().Unix()
	var who actor
	var sessionID, lastSeen, absoluteExpires int64
	err = s.userDB.QueryRowContext(r.Context(), `
SELECT s.id, u.id, u.username, u.role, u.timedock_account, s.last_seen_at, s.absolute_expires_at
FROM sessions s JOIN users u ON u.id=s.user_id
WHERE s.token_hash=? AND s.revoked_at IS NULL AND s.idle_expires_at>? AND s.absolute_expires_at>?
  AND u.status='active'`, hash[:], now, now).Scan(&sessionID, &who.ID, &who.Username, &who.Role, &who.TimeDockAccount, &lastSeen, &absoluteExpires)
	if err != nil {
		return actor{}, err
	}
	if now-lastSeen >= int64(sessionRefreshAfter/time.Second) {
		idleExpires := now + int64(sessionIdleTTL/time.Second)
		if idleExpires > absoluteExpires {
			idleExpires = absoluteExpires
		}
		_, _ = s.userDB.ExecContext(r.Context(), "UPDATE sessions SET last_seen_at=?, idle_expires_at=? WHERE id=?", now, idleExpires, sessionID)
	}
	return who, nil
}

func (s *server) handleAuthRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	// Serialize the empty-table check and insert so a shared bootstrap token
	// cannot create two first administrators through concurrent requests.
	s.registrationMu.Lock()
	defer s.registrationMu.Unlock()
	var req authRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !usernamePattern.MatchString(req.Username) {
		writeError(w, http.StatusBadRequest, errors.New("username must use 1-64 letters, numbers, dot, underscore, or hyphen"))
		return
	}
	timeDockAccount, err := normalizeTimeDockAccount(req.TimeDockAccount)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	tx, err := s.userDB.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	role := "admin"
	if count == 0 {
		if s.appConfig.get().WebFirstRunCompleted != 0 {
			writeError(w, http.StatusConflict, errors.New("first-run registration is not available"))
			return
		}
		if !validAdminRegisterToken(req.Token, s.cfg.AdminRegisterToken) {
			writeError(w, http.StatusForbidden, errors.New("invalid administrator registration token"))
			return
		}
	} else {
		if !sameOriginRequest(r) {
			writeError(w, http.StatusForbidden, errors.New("cross-origin request rejected"))
			return
		}
		who, err := s.authenticateRequest(r)
		if err != nil || who.Role != "admin" {
			writeError(w, http.StatusForbidden, errors.New("administrator access required"))
			return
		}
		role = strings.TrimSpace(req.Role)
		if role == "" {
			role = "user"
		}
		if role != "user" && role != "admin" {
			writeError(w, http.StatusBadRequest, errors.New("role must be user or admin"))
			return
		}
	}
	var encoded *string
	if req.Password != "" {
		hash, err := hashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		encoded = &hash
	} else if count == 0 {
		writeError(w, http.StatusBadRequest, errors.New("password is required for the first administrator"))
		return
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO users(username,password_hash,role,timedock_account,created_at,updated_at) VALUES(?,?,?,?,?,?)`, req.Username, encoded, role, timeDockAccount, now, now); err != nil {
		writeError(w, http.StatusConflict, errors.New("username already exists"))
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "success", "username": req.Username, "role": role, "timedock_account": timeDockAccount, "password_set": encoded != nil})
}

func (s *server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	now := time.Now()
	appCfg := s.appConfig.get()
	if s.loginLimiter == nil {
		s.loginLimiter = newLoginLimiter()
	}
	clientIP := requestClientIP(r, appCfg.ServerTrustedReverseProxy)
	if allowed, retryAfter := s.loginLimiter.allow(clientIP, now, appCfg); !allowed {
		w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
		writeError(w, http.StatusTooManyRequests, errors.New("too many login attempts; try again later"))
		return
	}
	var req authRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	var id int64
	var username, role, status string
	var passwordHash sql.NullString
	err := s.userDB.QueryRowContext(r.Context(), "SELECT id,username,password_hash,role,status FROM users WHERE username=?", req.Username).Scan(&id, &username, &passwordHash, &role, &status)
	valid := err == nil && status == "active" && passwordHash.Valid && verifyPassword(req.Password, passwordHash.String)
	if err != nil || !passwordHash.Valid {
		// Keep unknown users and passwordless users close to the normal timing path.
		_ = verifyPassword(req.Password, dummyPasswordHash)
	}
	if !valid {
		writeError(w, http.StatusUnauthorized, errors.New("invalid username or password"))
		return
	}
	token, tokenHash, err := newSessionToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	sessionNow := time.Now()
	if _, err := s.userDB.ExecContext(r.Context(), `INSERT INTO sessions(token_hash,user_id,created_at,last_seen_at,idle_expires_at,absolute_expires_at) VALUES(?,?,?,?,?,?)`, tokenHash, id, sessionNow.Unix(), sessionNow.Unix(), sessionNow.Add(sessionIdleTTL).Unix(), sessionNow.Add(sessionAbsoluteTTL).Unix()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	setSessionCookie(w, r, token, sessionNow.Add(sessionAbsoluteTTL))
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "username": username, "role": role})
}

func remoteHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

func (s *server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		hash := sha256.Sum256([]byte(cookie.Value))
		_, _ = s.userDB.ExecContext(r.Context(), "UPDATE sessions SET revoked_at=? WHERE token_hash=?", time.Now().Unix(), hash[:])
	}
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

func (s *server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	who, _ := actorFromRequest(r)
	writeJSON(w, http.StatusOK, map[string]any{"username": who.Username, "role": who.Role, "timedock_account": who.TimeDockAccount})
}

func normalizeTimeDockAccount(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	count := 0
	for _, r := range value {
		count++
		if count > 64 {
			return "", errors.New("timedock_account must not exceed 64 Unicode characters")
		}
		// Restrict the value to a single safe path component. In particular,
		// whitespace, slash and dot are rejected rather than normalized.
		if unicode.Is(unicode.Han, r) || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		return "", errors.New("timedock_account may contain only Chinese characters, English letters, and numbers")
	}
	return value, nil
}

func (s *server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}
	who, _ := actorFromRequest(r)
	var req passwordRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var current string
	if err := s.userDB.QueryRowContext(r.Context(), "SELECT password_hash FROM users WHERE id=?", who.ID).Scan(&current); err != nil || !verifyPassword(req.CurrentPassword, current) {
		writeError(w, http.StatusUnauthorized, errors.New("current password is incorrect"))
		return
	}
	next, err := hashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	now := time.Now().Unix()
	tx, err := s.userDB.BeginTx(r.Context(), nil)
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "UPDATE users SET password_hash=?,updated_at=? WHERE id=?", next, now, who.ID)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "UPDATE sessions SET revoked_at=? WHERE user_id=?", now, who.ID)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

func decodeJSONBody(r *http.Request, dst any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dst)
}

func hashPassword(password string) (string, error) {
	if len(password) < 8 || len(password) > 1024 {
		return "", errors.New("password must be between 8 and 1024 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=19456,t=2,p=1$%s$%s", b64.EncodeToString(salt), b64.EncodeToString(hash)), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var dummyPasswordHash, _ = hashPassword("clamavweb-dummy-password")

func validAdminRegisterToken(provided, configured string) bool {
	if configured == "" || provided == "" {
		return false
	}
	want := sha256.Sum256([]byte(configured))
	got := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func newSessionToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", HttpOnly: true, Secure: cookieSecure(r), SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(sessionAbsoluteTTL / time.Second)})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Path: "/", HttpOnly: true, Secure: cookieSecure(r), SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func cookieSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	value := strings.TrimSpace(os.Getenv("SCANNER_COOKIE_SECURE"))
	return strings.EqualFold(value, "true") || value == "1"
}

func (s *server) runSessionJanitor(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			// Keep recently revoked rows briefly for operational diagnosis, but
			// expired credentials do not need to grow the database indefinitely.
			cutoff := now.Add(-24 * time.Hour).Unix()
			_, _ = s.userDB.ExecContext(ctx, `DELETE FROM sessions
WHERE absolute_expires_at<? OR idle_expires_at<? OR (revoked_at IS NOT NULL AND revoked_at<?)`, now.Unix(), now.Unix(), cutoff)
		}
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

// scanInteger is shared by the administration handlers to decode path IDs
// without accepting signs, whitespace, or alternate numeric spellings.
func scanInteger(value string) (int64, error) {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 0, errors.New("invalid numeric identifier")
	}
	return strconv.ParseInt(value, 10, 64)
}
