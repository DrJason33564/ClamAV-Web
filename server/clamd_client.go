package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const maxClamdReply = 64 * 1024

// ClamdClient owns transport only; callers associate FILDES replies with their
// already-open file rather than trusting a daemon-supplied filesystem path.
type ClamdClient struct {
	Socket  string
	Timeout time.Duration
}

type clamdError struct {
	Stage, Kind string
	Err         error
}

func (e *clamdError) Error() string { return fmt.Sprintf("clamd %s (%s): %v", e.Stage, e.Kind, e.Err) }
func (e *clamdError) Unwrap() error { return e.Err }
func clamdFailure(stage, kind string, err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		kind = "timeout"
	}
	return &clamdError{stage, kind, err}
}

func (c ClamdClient) connect(ctx context.Context) (net.Conn, func(), error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	conn, err := (&net.Dialer{Timeout: min(timeout, 3*time.Second)}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, nil, clamdFailure("connect", "connection", err)
	}
	if err := setClamdSocketDeadline(ctx, conn, timeout); err != nil {
		conn.Close()
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	return conn, func() { stop(); conn.Close() }, nil
}

// A framed record must terminate with NUL. EOF is only successful between
// records; an empty connection or a truncated record is never a clean scan.
func readClamdRecord(reader *bufio.Reader) (string, error) {
	var record []byte
	for {
		part, err := reader.ReadSlice(0)
		if len(record)+len(part) > maxClamdReply {
			return "", clamdFailure("response", "protocol", errors.New("response exceeds limit"))
		}
		record = append(record, part...)
		if err == nil {
			return string(record[:len(record)-1]), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err == io.EOF && len(record) == 0 {
			return "", io.EOF
		}
		return "", clamdFailure("response", "truncated_response", err)
	}
}

func (c ClamdClient) command(ctx context.Context, command string) (string, error) {
	conn, closeConn, err := c.connect(ctx)
	if err != nil {
		return "", err
	}
	defer closeConn()
	return clamdCommandOnConn(conn, command)
}

func clamdCommandOnConn(conn net.Conn, command string) (string, error) {
	if _, err := io.WriteString(conn, "z"+command+"\x00"); err != nil {
		return "", clamdFailure("send", "write", err)
	}
	r := bufio.NewReader(conn)
	reply, err := readClamdRecord(r)
	if command == "SHUTDOWN" && err == io.EOF {
		return "", nil
	}
	if err == io.EOF {
		return "", clamdFailure("response", "empty_response", err)
	}
	if err != nil {
		return "", err
	}
	if tail, err := readClamdRecord(r); err != io.EOF {
		return "", clamdFailure("response", "protocol", fmt.Errorf("unexpected trailing reply %q: %v", tail, err))
	}
	return reply, nil
}

func (c ClamdClient) ScanFile(ctx context.Context, file *os.File, submitted func()) (string, error) {
	conn, closeConn, err := c.connect(ctx)
	if err != nil {
		return "", err
	}
	defer closeConn()
	u := conn.(*net.UnixConn)
	if _, err := io.WriteString(u, "zFILDES\x00"); err != nil {
		return "", clamdFailure("command", "write", err)
	}
	oob := unix.UnixRights(int(file.Fd()))
	n, oobn, err := u.WriteMsgUnix([]byte{0}, oob, nil)
	if err != nil || n != 1 || oobn != len(oob) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return "", clamdFailure("descriptor", "write", err)
	}
	if submitted != nil {
		submitted()
	}
	r := bufio.NewReader(u)
	var signature string
	var scanErr error
	seen, clean := false, false
	for {
		record, err := readClamdRecord(r)
		if err == io.EOF {
			if !seen {
				scanErr = errors.Join(scanErr, clamdFailure("response", "empty_response", err))
			}
			return signature, scanErr
		}
		if err != nil {
			return signature, errors.Join(scanErr, err)
		}
		if seen {
			scanErr = errors.Join(scanErr, clamdFailure("response", "protocol", errors.New("multiple results for one file")))
		}
		seen = true
		_, value, ok := strings.Cut(record, ": ")
		if !ok {
			scanErr = errors.Join(scanErr, clamdFailure("response", "protocol", fmt.Errorf("unexpected reply %.256q", record)))
			continue
		}
		switch {
		case value == "OK":
			clean = true
		case strings.HasSuffix(value, " FOUND") && strings.TrimSuffix(value, " FOUND") != "":
			signature = strings.TrimSuffix(value, " FOUND")
		case strings.HasSuffix(value, " ERROR"):
			scanErr = errors.Join(scanErr, clamdFailure("scan", "daemon_error", errors.New(value)))
		default:
			scanErr = errors.Join(scanErr, clamdFailure("response", "protocol", fmt.Errorf("unexpected reply %.256q", record)))
		}
		if clean && signature != "" {
			scanErr = errors.Join(scanErr, clamdFailure("response", "protocol", errors.New("conflicting scan results")))
		}
	}
}
