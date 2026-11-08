package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// clamavSleepTimerState separates synchronous activity tracking from timer
// ownership. Only the scheduler goroutine touches time.Timer; callers merely
// advance the generation and send a coalesced wake-up notification.
type clamavSleepTimerState struct {
	mu         sync.Mutex
	generation uint64
	changed    chan struct{}
}

func newClamAVSleepTimerState() *clamavSleepTimerState {
	return &clamavSleepTimerState{changed: make(chan struct{}, 1)}
}

func (state *clamavSleepTimerState) advance() {
	state.mu.Lock()
	state.generation++
	state.mu.Unlock()
	select {
	case state.changed <- struct{}{}:
	default:
	}
}

func (state *clamavSleepTimerState) currentGeneration() uint64 {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.generation
}

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
	s.notifyClamAVSleepTimerChanged("manual_sleep")
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
	s.notifyClamAVSleepTimerChanged("manual_wake")
	writeJSON(w, statusCode, clamavPowerResponse{Status: status, Message: message})
}

func (s *server) notifyClamAVSleepTimerChanged(reason string) {
	if s.clamavSleepTimer == nil {
		return
	}
	s.clamavSleepTimer.advance()
	s.debug("clamav_power", "ClamAV sleep timer reset requested", "reason", reason)
}

func stopAndDrainTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (s *server) scanPendingOrActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeBatchID != "" || len(s.queuedBatchIDs) != 0
}

func (s *server) runClamAVSleepTimer(ctx context.Context) {
	s.runClamAVSleepTimerWithUnit(ctx, time.Second)
}

// runClamAVSleepTimerWithUnit keeps production intervals in seconds while
// allowing deterministic, fast unit tests without weakening config validation.
func (s *server) runClamAVSleepTimerWithUnit(ctx context.Context, unit time.Duration) {
	s.runClamAVSleepTimerWithAction(ctx, unit, s.sleepClamAV)
}

func (s *server) runClamAVSleepTimerWithAction(ctx context.Context, unit time.Duration, sleep func(context.Context) (string, string, int, error)) {
	if s.clamavSleepTimer == nil {
		return
	}

	var timer *time.Timer
	var timerC <-chan time.Time
	var armedGeneration uint64
	var armedInterval int
	arm := func() {
		stopAndDrainTimer(timer)
		timer = nil
		timerC = nil

		interval := s.appConfig.get().ClamAVSleepTimer
		if interval == 0 {
			s.debug("clamav_power", "ClamAV sleep timer disabled")
			return
		}
		if s.isSleeping() {
			return
		}

		armedGeneration = s.clamavSleepTimer.currentGeneration()
		armedInterval = interval
		timer = time.NewTimer(time.Duration(interval) * unit)
		timerC = timer.C
		s.debug("clamav_power", "ClamAV sleep timer started", "interval_seconds", interval)
	}

	arm()
	defer func() { stopAndDrainTimer(timer) }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.clamavSleepTimer.changed:
			arm()
		case <-timerC:
			timer = nil
			timerC = nil

			s.clamavPowerMu.Lock()
			currentInterval := s.appConfig.get().ClamAVSleepTimer
			if armedGeneration != s.clamavSleepTimer.currentGeneration() || currentInterval == 0 || currentInterval != armedInterval {
				s.clamavPowerMu.Unlock()
				arm()
				continue
			}
			if s.scanPendingOrActive() {
				s.clamavPowerMu.Unlock()
				s.debug("clamav_power", "automatic ClamAV sleep deferred for manual scan")
				arm()
				continue
			}

			status, _, statusCode, err := sleep(ctx)
			s.clamavPowerMu.Unlock()
			if err == nil && status == "sleeping" {
				s.info("clamav_power", "ClamAV sleep timer expired; engine is sleeping")
				continue
			}
			if ctx.Err() != nil {
				return
			}
			s.warn("clamav_power", "automatic ClamAV sleep failed; timer will retry", "status_code", statusCode, "error", err)
			arm()
		}
	}
}

func (s *server) sleepClamAV(ctx context.Context) (string, string, int, error) {
	s.debug("clamav_power", "ClamAV sleep requested")
	if s.scanPendingOrActive() {
		return "failed", "", http.StatusConflict, errors.New("ClamAV cannot sleep while scans are queued or active")
	}
	if s.isSleeping() {
		return "sleeping", "ClamAV is already sleeping.", http.StatusOK, nil
	}

	if err := sendClamdShutdown(ctx, s.cfg.ClamdSocket, s.cfg.CommandTimout); err != nil {
		s.error("clamav_power", "send ClamAV shutdown failed", "error", err)
		return "failed", "", powerErrorStatus(ctx, err), fmt.Errorf("put ClamAV to sleep: %w", err)
	}
	s.invalidateClamdStatusCache()

	s.mu.Lock()
	s.engineSleeping = true
	s.mu.Unlock()

	s.info("clamav_power", "ClamAV entered sleep mode")
	return "sleeping", "ClamAV entered sleep mode.", http.StatusOK, nil
}

func (s *server) wakeClamAV(requestCtx context.Context) (string, string, int, error) {
	s.debug("clamav_power", "ClamAV wake requested")
	ping, _ := s.pingClamd(requestCtx)
	alreadyAwake := ping == "ready"

	// Do not bind a potentially long ClamAV database load to the HTTP client's
	// cancellation. The server serializes power transitions and completes the
	// requested wake even if the caller disconnects while waiting.
	ctx, cancel := context.WithTimeout(s.scanContext(), s.cfg.WakeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.cfg.StartupScript, "--wake")
	// Direct file descriptors prevent the awakened background /init process
	// from keeping os/exec capture pipes open after startup.sh itself returns.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			s.error("clamav_power", "ClamAV wake timed out", "timeout", s.cfg.WakeTimeout)
			return "failed", "", http.StatusGatewayTimeout, errors.New("ClamAV wake timed out")
		}
		s.error("clamav_power", "ClamAV wake script failed", "error", err)
		return "failed", "", http.StatusInternalServerError, fmt.Errorf("wake ClamAV: %w", err)
	}
	s.invalidateClamdStatusCache()
	s.mu.Lock()
	s.engineSleeping = false
	s.mu.Unlock()

	// startup.sh owns the final PONG check; Go owns runtime sleep state.
	// A zero exit status is therefore the complete wake result.
	if alreadyAwake {
		s.info("clamav_power", "ClamAV wake completed; daemon was already awake")
		return "awake", "ClamAV is already awake.", http.StatusOK, nil
	}
	s.info("clamav_power", "ClamAV woke successfully")
	return "awake", "ClamAV woke successfully.", http.StatusOK, nil
}

func (s *server) isSleeping() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.engineSleeping }

func sendClamdShutdown(ctx context.Context, socketPath string, timeout time.Duration) error {
	reply, err := (ClamdClient{socketPath, timeout}).command(ctx, "SHUTDOWN")
	if err == nil && reply != "" {
		return fmt.Errorf("unexpected SHUTDOWN response: %.256q", reply)
	}
	return err
}
func sendClamdShutdownCommand(ctx context.Context, conn net.Conn, timeout time.Duration) error {
	if err := setClamdSocketDeadline(ctx, conn, timeout); err != nil {
		return err
	}
	reply, err := clamdCommandOnConn(conn, "SHUTDOWN")
	if err != nil {
		return err
	}
	if reply != "" {
		return errors.New("clamd returned an unexpected SHUTDOWN response")
	}
	return nil
}

func setClamdSocketDeadline(ctx context.Context, conn net.Conn, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	if requestDeadline, ok := ctx.Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	return conn.SetDeadline(deadline)
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
