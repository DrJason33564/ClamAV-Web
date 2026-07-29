package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"
)

type clamavPowerResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (s *server) handleClamAVSleep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	s.clamavPowerMu.Lock()
	defer s.clamavPowerMu.Unlock()

	status, message, statusCode, err := s.sleepClamAV(r.Context())
	if err != nil {
		writeError(w, statusCode, err)
		return
	}
	writeJSON(w, statusCode, clamavPowerResponse{Status: status, Message: message})
}

func (s *server) handleClamAVWake(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	s.clamavPowerMu.Lock()
	defer s.clamavPowerMu.Unlock()

	status, message, statusCode, err := s.wakeClamAV(r.Context())
	if err != nil {
		writeError(w, statusCode, err)
		return
	}
	writeJSON(w, statusCode, clamavPowerResponse{Status: status, Message: message})
}

func (s *server) sleepClamAV(ctx context.Context) (string, string, int, error) {
	sleeping, err := directoryLockExists(s.cfg.SleepLockDir)
	if err != nil {
		return "failed", "", http.StatusInternalServerError, err
	}

	ping, _ := s.pingClamd(ctx)
	if sleeping && ping != "ready" {
		return "sleeping", "ClamAV is already sleeping.", http.StatusOK, nil
	}

	// Never stop clamd while scan_once.sh owns the cross-process scan lock.
	scanning, err := s.scanLockActive()
	if err != nil {
		return "failed", "", http.StatusInternalServerError, fmt.Errorf("check active scan lock: %w", err)
	}
	if scanning {
		return "failed", "", http.StatusConflict, errors.New("ClamAV cannot sleep while a scan is active")
	}

	if err := sendClamdShutdown(ctx, s.cfg.ClamdSocket, s.cfg.CommandTimout); err != nil {
		return "failed", "", powerErrorStatus(ctx, err), fmt.Errorf("put ClamAV to sleep: %w", err)
	}

	// mkdir is the atomic state transition. An existing lock is valid when a
	// stale sleep marker was found next to a still-running clamd instance.
	if err := createDirectoryLock(s.cfg.SleepLockDir); err != nil {
		return "failed", "", http.StatusInternalServerError, fmt.Errorf("create sleep lock: %w", err)
	}
	return "sleeping", "ClamAV entered sleep mode.", http.StatusOK, nil
}

func (s *server) wakeClamAV(requestCtx context.Context) (string, string, int, error) {
	ping, _ := s.pingClamd(requestCtx)
	alreadyAwake := ping == "ready"

	// Do not bind a potentially long ClamAV database load to the HTTP client's
	// cancellation. The server serializes power transitions and completes the
	// requested wake even if the caller disconnects while waiting.
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.WakeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.cfg.StartupScript, "--wake")
	// Direct file descriptors prevent the awakened background /init process
	// from keeping os/exec capture pipes open after startup.sh itself returns.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "failed", "", http.StatusGatewayTimeout, errors.New("ClamAV wake timed out")
		}
		return "failed", "", http.StatusInternalServerError, fmt.Errorf("wake ClamAV: %w", err)
	}

	// startup.sh owns both the final PONG check and the sleep-lock transition.
	// A zero exit status is therefore the complete wake result.
	if alreadyAwake {
		return "awake", "ClamAV is already awake.", http.StatusOK, nil
	}
	return "awake", "ClamAV woke successfully.", http.StatusOK, nil
}

func sendClamdShutdown(ctx context.Context, socketPath string, timeout time.Duration) error {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	return sendClamdShutdownCommand(ctx, conn, timeout)
}

func sendClamdShutdownCommand(ctx context.Context, conn net.Conn, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	if requestDeadline, ok := ctx.Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	if _, err := io.WriteString(conn, "SHUTDOWN\n"); err != nil {
		return err
	}

	// clamd's successful SHUTDOWN command has no response. EOF with zero bytes
	// therefore corresponds to the successful exit status of a socket client.
	var response [1]byte
	n, err := conn.Read(response[:])
	if n != 0 {
		return fmt.Errorf("clamd returned an unexpected SHUTDOWN response")
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("clamd did not close the SHUTDOWN connection")
}

func directoryLockExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return false, fmt.Errorf("lock path is not a directory: %s", path)
		}
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func createDirectoryLock(path string) error {
	err := os.Mkdir(path, 0o755)
	if errors.Is(err, os.ErrExist) {
		exists, checkErr := directoryLockExists(path)
		if checkErr != nil {
			return checkErr
		}
		if exists {
			return nil
		}
	}
	return err
}

func powerErrorStatus(ctx context.Context, err error) int {
	if ctx.Err() == context.DeadlineExceeded {
		return http.StatusGatewayTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return http.StatusGatewayTimeout
	}
	return http.StatusInternalServerError
}
