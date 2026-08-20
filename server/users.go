package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

func (s *server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	if _, err := requireAdminActor(r); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	if r.Method == http.MethodPost {
		s.handleAuthRegister(w, r)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	rows, err := s.userDB.QueryContext(r.Context(), "SELECT username,role,status,timedock_account,password_hash,created_at,updated_at FROM users ORDER BY username")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	users := []userRecord{}
	for rows.Next() {
		var item userRecord
		var password sql.NullString
		if err := rows.Scan(&item.Username, &item.Role, &item.Status, &item.TimeDockAccount, &password, &item.CreatedAt, &item.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		item.PasswordSet = password.Valid && password.String != ""
		users = append(users, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "users": users})
}

func (s *server) handleAdminUser(w http.ResponseWriter, r *http.Request) {
	who, err := requireAdminActor(r)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	username := strings.TrimPrefix(r.URL.Path, "/api/admin/users/")
	if !usernamePattern.MatchString(username) {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		s.patchAdminUser(w, r, who, username)
	case http.MethodDelete:
		if username == who.Username {
			writeError(w, http.StatusBadRequest, errors.New("use the account deletion endpoint to delete your own account"))
			return
		}
		if err := s.cleanDeleteUser(r.Context(), username); err != nil {
			s.warn("users", "administrator user deletion failed", "actor", who.Username, "user", username, "error", err)
			writeError(w, userDeletionStatus(err), err)
			return
		}
		s.info("users", "administrator deleted user", "actor", who.Username, "user", username)
		writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
	default:
		methodNotAllowed(w)
	}
}

func (s *server) patchAdminUser(w http.ResponseWriter, r *http.Request, who actor, username string) {
	var req userPatchRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var id int64
	var role, status, timeDockAccount string
	timeDockAccountChanged := false
	var password sql.NullString
	if err := s.userDB.QueryRowContext(r.Context(), "SELECT id,role,status,timedock_account,password_hash FROM users WHERE username=?", username).Scan(&id, &role, &status, &timeDockAccount, &password); err != nil {
		writeError(w, http.StatusNotFound, errors.New("user not found"))
		return
	}
	if req.Role != nil {
		next := strings.TrimSpace(*req.Role)
		if next != "user" && next != "admin" {
			writeError(w, http.StatusBadRequest, errors.New("role must be user or admin"))
			return
		}
		if role == "admin" && next != "admin" {
			if last, _ := s.isLastActiveAdmin(r.Context(), username); last {
				writeError(w, http.StatusConflict, errors.New("the last active administrator cannot be demoted"))
				return
			}
		}
		role = next
	}
	if req.Status != nil {
		next := strings.TrimSpace(*req.Status)
		if next != "active" && next != "disabled" {
			writeError(w, http.StatusBadRequest, errors.New("status must be active or disabled"))
			return
		}
		if next == "disabled" && role == "admin" {
			if last, _ := s.isLastActiveAdmin(r.Context(), username); last {
				writeError(w, http.StatusConflict, errors.New("the last active administrator cannot be disabled"))
				return
			}
		}
		status = next
	}
	if req.TimeDockAccount != nil {
		normalized, err := normalizeTimeDockAccount(*req.TimeDockAccount)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		timeDockAccountChanged = normalized != timeDockAccount
		timeDockAccount = normalized
	}
	if req.Password != nil {
		if *req.Password == "" {
			password = sql.NullString{}
		} else {
			hash, err := hashPassword(*req.Password)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			password = sql.NullString{String: hash, Valid: true}
		}
	}
	now := time.Now().Unix()
	if _, err := s.userDB.ExecContext(r.Context(), "UPDATE users SET role=?,status=?,timedock_account=?,password_hash=?,updated_at=? WHERE id=?", role, status, timeDockAccount, password, now, id); err != nil {
		s.error("users", "update user failed", "actor", who.Username, "user", username, "error", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if status == "disabled" || req.Password != nil {
		_, _ = s.userDB.ExecContext(r.Context(), "UPDATE sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL", now, id)
	}
	if status == "disabled" {
		// Disabled accounts must not retain unattended scheduled execution.
		if err := s.disableCronRulesForUser(username); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if s.cfg.IsTimeDock && timeDockAccountChanged && status != "disabled" {
		// Existing cron targets may belong to the previous TimeDock account.
		// Disable them until the owner reviews and explicitly re-enables them.
		if err := s.disableCronRulesForUser(username); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	s.info("users", "user updated", "actor", who.Username, "user", username, "role", role, "status", status, "timedock_account_changed", timeDockAccountChanged, "password_changed", req.Password != nil)
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "username": username, "role": role, "timedock_account": timeDockAccount})
}

func (s *server) handleAuthAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	who, _ := actorFromRequest(r)
	var req accountDeleteRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var hash string
	if err := s.userDB.QueryRowContext(r.Context(), "SELECT password_hash FROM users WHERE id=?", who.ID).Scan(&hash); err != nil || !verifyPassword(req.Password, hash) {
		writeError(w, http.StatusUnauthorized, errors.New("password is incorrect"))
		return
	}
	if err := s.cleanDeleteUser(r.Context(), who.Username); err != nil {
		s.warn("users", "self account deletion failed", "user", who.Username, "error", err)
		writeError(w, userDeletionStatus(err), err)
		return
	}
	s.info("users", "user deleted own account", "user", who.Username)
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

func (s *server) isLastActiveAdmin(ctx context.Context, username string) (bool, error) {
	var count int
	err := s.userDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role='admin' AND status='active' AND username<>?", username).Scan(&count)
	return count == 0, err
}

func userDeletionStatus(err error) int {
	if errors.Is(err, errUserBusy) || errors.Is(err, errLastAdmin) {
		return http.StatusConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}
