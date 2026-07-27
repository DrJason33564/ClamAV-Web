package main

import (
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"time"
)

func (s *server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || !s.validAccount(username, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="ClamAV Scanner", charset="UTF-8"`)
			writeError(w, http.StatusUnauthorized, errors.New("authentication required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) validAccount(username, password string) bool {
	for _, account := range s.cfg.Accounts {
		userOK := subtle.ConstantTimeCompare([]byte(username), []byte(account.Username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(password), []byte(account.Password)) == 1
		if userOK && passOK {
			return true
		}
	}
	return false
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
