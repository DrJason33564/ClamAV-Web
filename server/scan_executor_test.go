package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"clamav-scanner/internal/applog"
	"golang.org/x/sys/unix"
)

// Exercise real Unix ancillary data, not just a mocked ScanFile method.
func fakeClamd(t *testing.T, handler func(string, *os.File) string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "clamd-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "clamd.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(2 * time.Second))
				// Read the command byte by byte so the descriptor's dummy byte and
				// ancillary message remain available for ReadMsgUnix.
				var command []byte
				for {
					var b [1]byte
					if _, err := io.ReadFull(conn, b[:]); err != nil {
						return
					}
					command = append(command, b[0])
					if b[0] == 0 {
						break
					}
				}
				cmd := strings.TrimSuffix(string(command), "\x00")
				var file *os.File
				if cmd == "zFILDES" {
					data := make([]byte, 1)
					control := make([]byte, unix.CmsgSpace(4))
					_, n, _, _, err := conn.ReadMsgUnix(data, control)
					if err != nil {
						t.Error(err)
						return
					}
					messages, err := unix.ParseSocketControlMessage(control[:n])
					if err != nil {
						t.Error(err)
						return
					}
					for _, message := range messages {
						fds, err := unix.ParseUnixRights(&message)
						if err != nil {
							t.Error(err)
							return
						}
						for _, fd := range fds {
							if file == nil {
								file = os.NewFile(uintptr(fd), "received")
							} else {
								unix.Close(fd)
							}
						}
					}
					if file == nil {
						t.Error("missing SCM_RIGHTS fd")
						return
					}
					defer file.Close()
				}
				reply := handler(cmd, file)
				if reply != "" {
					_, _ = io.WriteString(conn, reply)
				}
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done; wg.Wait() })
	return path
}

func newScanTestServer(t *testing.T) *server {
	s := newDatabaseTestServer(t)
	s.cfg.BrowseRoots = []string{filepath.Join(t.TempDir(), "scan")}
	if err := os.MkdirAll(s.cfg.BrowseRoots[0], 0755); err != nil {
		t.Fatal(err)
	}
	s.cfg.QuarantineDir = t.TempDir()
	s.cfg.CommandTimout = time.Second
	s.cfg.ScanTimeout = time.Second
	s.history.logDir = s.cfg.LogDir
	s.scanCtx = t.Context()
	logger, err := applog.New(applog.Config{Path: filepath.Join(s.cfg.LogDir, "clamavweb.log"), MaxSize: 1 << 20, MaxFiles: 2, Level: "warn"})
	if err != nil {
		t.Fatal(err)
	}
	s.logger = logger
	t.Cleanup(func() { logger.Close() })
	t.Cleanup(func() { s.scanWG.Wait() })
	return s
}

func TestSocketScanFailuresAndPartialFindings(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		wantFound   bool
		wantError   string
	}{
		{"clean", "fd[1]: OK\x00", false, ""},
		{"found", "fd[1]: Eicar-Test FOUND\x00", true, ""},
		{"empty", "", false, "empty_response"},
		{"truncated", "fd[1]: OK", false, "truncated_response"},
		{"daemon error", "fd[1]: denied ERROR\x00", false, "daemon_error"},
		{"malformed", "nonsense\x00", false, "protocol"},
		{"conflict", "fd[1]: OK\x00fd[1]: Eicar-Test FOUND\x00", true, "protocol"},
		{"found then disconnect", "fd[1]: Eicar-Test FOUND\x00broken", true, "truncated_response"},
		{"oversized", strings.Repeat("x", maxClamdReply+1), false, "protocol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := fakeClamd(t, func(cmd string, file *os.File) string {
				if cmd != "zFILDES" {
					t.Errorf("command %q", cmd)
				}
				data, _ := io.ReadAll(file)
				if string(data) != "payload" {
					t.Errorf("fd content %q", data)
				}
				return tc.reply
			})
			file, err := os.CreateTemp(t.TempDir(), "input")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			file.WriteString("payload")
			file.Seek(0, 0)
			signature, err := (ClamdClient{path, time.Second}).ScanFile(t.Context(), file, nil)
			if (signature != "") != tc.wantFound {
				t.Fatalf("signature %q", signature)
			}
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("wanted %s, got %v", tc.wantError, err)
			}
		})
	}
}

func TestSocketTimeoutAndCancellation(t *testing.T) {
	path := fakeClamd(t, func(string, *os.File) string { time.Sleep(80 * time.Millisecond); return "fd: OK\x00" })
	f, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = (ClamdClient{path, 10 * time.Millisecond}).ScanFile(t.Context(), f, nil)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout, got %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = (ClamdClient{path, time.Second}).ScanFile(ctx, f, nil); err == nil {
		t.Fatal("canceled scan succeeded")
	}
}

func TestExecutorAggregatesAndIndexesWithoutWatcher(t *testing.T) {
	for _, tc := range []struct {
		name   string
		files  map[string]string
		want   string
		errors int
	}{
		{"single error", map[string]string{"a": "error"}, "error", 1},
		{"partial error", map[string]string{"a": "clean", "b": "error"}, "error", 1},
		{"found wins", map[string]string{"a": "found", "b": "error", "c": "clean"}, "found", 1},
		{"all errors", map[string]string{"a": "error", "b": "error"}, "error", 2},
		{"clean", map[string]string{"a": "clean", "sub/b": "clean"}, "clean", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScanTestServer(t)
			s.cfg.ClamdSocket = fakeClamd(t, func(_ string, f *os.File) string {
				data, _ := io.ReadAll(f)
				switch string(data) {
				case "error":
					return "fd: denied ERROR\x00"
				case "found":
					return "fd: Eicar FOUND\x00"
				default:
					return "fd: OK\x00"
				}
			})
			for name, body := range tc.files {
				path := filepath.Join(s.cfg.BrowseRoots[0], name)
				os.MkdirAll(filepath.Dir(path), 0755)
				if err := os.WriteFile(path, []byte(body), 0640); err != nil {
					t.Fatal(err)
				}
			}
			job := s.history.newScanJob("manual", testAliceUserID, s.cfg.BrowseRoots[0], "warn", false)
			if err := s.history.saveScanJob(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			summary := s.executeScan(t.Context(), job)
			if err := s.history.finishScanJob(t.Context(), job, summary); err != nil {
				t.Fatal(err)
			}
			if job.Result != tc.want || summary.ScanErrors != tc.errors {
				t.Fatalf("job=%+v summary=%+v", job, summary)
			}
			var result string
			if err := s.historyDB.QueryRow("SELECT result FROM history_jobs WHERE job_id=?", job.JobID).Scan(&result); err != nil || result != tc.want {
				t.Fatalf("immediate index %q: %v", result, err)
			}
			log, _ := os.ReadFile(job.LogFile)
			if !strings.Contains(string(log), "Scan completed:") {
				t.Fatalf("scan records filtered by LOG_LEVEL: %s", log)
			}
			if tc.errors > 0 {
				diagnostic, _ := os.ReadFile(filepath.Join(s.cfg.LogDir, "clamavweb.log"))
				if !strings.Contains(string(diagnostic), "error_kind=daemon_error") {
					t.Fatalf("missing diagnostic: %s", diagnostic)
				}
			}
		})
	}
}

func TestFileLocksAreBestEffortAndReleasedOnSubmission(t *testing.T) {
	s := newScanTestServer(t)
	path := filepath.Join(s.cfg.BrowseRoots[0], "file")
	os.WriteFile(path, []byte("clean"), 0600)
	job := s.history.newScanJob("manual", testAliceUserID, path, "warn", true)
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := unix.Flock(int(held.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	s.cfg.ClamdSocket = fakeClamd(t, func(string, *os.File) string { return "fd: OK\x00" })
	summary := s.executeScan(t.Context(), job)
	if summary.ScanErrors != 0 || summary.Scanned != 1 {
		t.Fatalf("lock conflict blocked scan: %+v", summary)
	}
	data, _ := os.ReadFile(job.LogFile)
	if !strings.Contains(string(data), "File lock unavailable") {
		t.Fatal("missing lock warning")
	}
	unix.Flock(int(held.Fd()), unix.LOCK_UN)
	// Submission callback executes immediately after sendmsg, before response.
	file, err := openScanPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	_, err = (ClamdClient{s.cfg.ClamdSocket, time.Second}).ScanFile(t.Context(), file, func() {
		unix.Flock(int(file.Fd()), unix.LOCK_UN)
		if err := unix.Flock(int(held.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Error("lock still held after submission:", err)
		}
		unix.Flock(int(held.Fd()), unix.LOCK_UN)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLockFileAPIValidation(t *testing.T) {
	s := newScanTestServer(t)
	for _, value := range []string{"null", "1", "\"true\""} {
		r := httptest.NewRequest(http.MethodPost, "/api/scans", strings.NewReader(`{"LOCK_FILE":`+value+`}`))
		w := httptest.NewRecorder()
		s.startScan(w, r)
		if w.Code != 400 {
			t.Fatalf("%s: %d", value, w.Code)
		}
	}
}

func TestCachedSocketStatusCoalescesProbes(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	path := fakeClamd(t, func(command string, _ *os.File) string {
		mu.Lock()
		counts[command]++
		mu.Unlock()
		if command == "zPING" {
			return "PONG\x00"
		}
		return "ClamAV 1.5.4/28098/date\x00"
	})
	s := &server{cfg: config{ClamdSocket: path, CommandTimout: time.Second}}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if result := s.cachedClamdStatus(t.Context()); result.ping != "ready" {
				t.Errorf("probe %+v", result)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if counts["zPING"] != 1 || counts["zVERSION"] != 1 {
		t.Fatalf("probe counts %v", counts)
	}
}

func TestSafeScanOpenRejectsSymlinkParentsAndSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	os.Mkdir(real, 0755)
	os.WriteFile(filepath.Join(real, "file"), nil, 0600)
	os.Symlink(real, filepath.Join(dir, "link"))
	if f, err := openScanPath(filepath.Join(dir, "link", "file")); err == nil {
		f.Close()
		t.Fatal("opened symlink parent")
	}
	s := newScanTestServer(t)
	fifo := filepath.Join(s.cfg.BrowseRoots[0], "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	job := s.history.newScanJob("manual", testAliceUserID, fifo, "warn", false)
	if result := s.executeScan(t.Context(), job); result.ScanErrors != 1 {
		t.Fatalf("non-regular target accepted: %+v", result)
	}
}

// Keep common protocol parsers exercised on a fragmented NUL record.
func TestReadClamdRecordFragmentation(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 5000)+"\x00"), 16)
	record, err := readClamdRecord(r)
	if err != nil || len(record) != 5000 {
		t.Fatalf("record length=%d err=%v", len(record), err)
	}
}

func TestManualAndScheduledScansShareOneReservation(t *testing.T) {
	s := newScanTestServer(t)
	if _, err := s.userDB.Exec("INSERT INTO users(id,username,role,created_at,updated_at) VALUES(?,?,?,1,1)", testAliceUserID, "alice", "user"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(s.cfg.BrowseRoots[0], "input")
	if err := os.WriteFile(file, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	s.cfg.ClamdSocket = fakeClamd(t, func(command string, _ *os.File) string {
		if command != "zFILDES" {
			t.Errorf("unexpected command %s", command)
		}
		entered <- struct{}{}
		<-release
		return "fd: OK\x00"
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	who := actor{ID: testAliceUserID, Username: "alice", Role: "user"}
	r := httptest.NewRequest(http.MethodPost, "/api/scans", strings.NewReader(`{"targets":["`+file+`"],"LOCK_FILE":true}`))
	r = r.WithContext(context.WithValue(r.Context(), actorContextKey{}, who))
	w := httptest.NewRecorder()
	s.startScan(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("manual admission: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("manual scan did not start")
	}
	rule := cronRule{ID: "abc123def456gh78", Enabled: true, UserID: testAliceUserID, Target: file, Action: "warn"}
	s.cronRules = []cronRule{rule}
	s.submitCronRule(rule, time.Now())
	s.mu.RLock()
	queued := len(s.queuedBatchIDs)
	s.mu.RUnlock()
	if queued != 1 {
		t.Fatalf("scheduled job not queued: %d", queued)
	}
	if _, _, code, err := s.sleepClamAV(t.Context()); code != 409 || err == nil {
		t.Fatalf("sleep interrupted active scan: code=%d err=%v", code, err)
	}
	select {
	case <-entered:
		t.Fatal("scheduled scan overlapped manual scan")
	default:
	}
	once.Do(func() { close(release) })
	s.scanWG.Wait()
	var count int
	if err := s.historyDB.QueryRow("SELECT COUNT(*) FROM history_jobs WHERE result='clean'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("finished scans=%d: %v", count, err)
	}
}

func TestDirectoryLocksSiblingsBeforeSubmission(t *testing.T) {
	s := newScanTestServer(t)
	root := s.cfg.BrowseRoots[0]
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "child"), 0755); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "child", "c")
	if err := os.WriteFile(child, []byte("c"), 0600); err != nil {
		t.Fatal(err)
	}
	probe := func(path string, wantLocked bool) {
		f, err := os.Open(path)
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		deadline := time.Now().Add(time.Second)
		for !wantLocked && err != nil && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
			err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		}
		if wantLocked && err == nil {
			t.Error("sibling was not locked:", path)
		}
		if !wantLocked && err != nil {
			t.Error("file remained locked:", path, err)
		}
		if err == nil {
			unix.Flock(int(f.Fd()), unix.LOCK_UN)
		}
	}
	var first atomic.Bool
	first.Store(true)
	s.cfg.ClamdSocket = fakeClamd(t, func(_ string, file *os.File) string {
		data, _ := io.ReadAll(file)
		name := string(data)
		// Sending a's descriptor unlocks a but keeps sibling b locked. A child
		// directory is not pre-locked before the walk enters that directory.
		if first.CompareAndSwap(true, false) {
			probe(filepath.Join(root, name), false)
			other := "a"
			if name == "a" {
				other = "b"
			}
			probe(filepath.Join(root, other), true)
			probe(child, false)
		}
		if name == "c" {
			probe(filepath.Join(root, "a"), false)
			probe(filepath.Join(root, "b"), false)
		}
		return "fd: OK\x00"
	})
	job := s.history.newScanJob("manual", testAliceUserID, root, "warn", true)
	if result := s.executeScan(t.Context(), job); result.Scanned != 3 || result.ScanErrors != 0 {
		t.Fatalf("result %+v", result)
	}
}
