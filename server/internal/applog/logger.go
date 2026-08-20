// Package applog provides the service's structured logging, secret redaction,
// level filtering, and size-based file rotation.
package applog

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

const maskedSecret = "********"

// Config describes the immutable logging settings loaded at process startup.
type Config struct {
	Path     string
	MaxSize  int64
	MaxFiles int
	Level    string
}

// Logger is safe for concurrent use. Close must be called during shutdown.
type Logger struct {
	logger *slog.Logger
	writer *rotatingWriter
}

// New opens the active log file and constructs a structured text logger.
func New(cfg Config) (*Logger, error) {
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, errors.New("log file path is required")
	}
	if cfg.MaxSize <= 0 {
		return nil, errors.New("log file max size must be positive")
	}
	if cfg.MaxFiles <= 0 {
		return nil, errors.New("log file count must be positive")
	}
	configuredLevel, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	writer, err := newRotatingWriter(cfg.Path, cfg.MaxSize, cfg.MaxFiles)
	if err != nil {
		return nil, err
	}
	handler := slog.NewTextHandler(writer, &slog.HandlerOptions{
		Level:       configuredLevel,
		ReplaceAttr: redactAttr,
	})
	return &Logger{logger: slog.New(handler), writer: writer}, nil
}

func parseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q", value)
	}
}

func (l *Logger) Debug(msg string, args ...any) { l.logger.Debug(msg, args...) }
func (l *Logger) Info(msg string, args ...any)  { l.logger.Info(msg, args...) }
func (l *Logger) Warn(msg string, args ...any)  { l.logger.Warn(msg, args...) }
func (l *Logger) Error(msg string, args ...any) { l.logger.Error(msg, args...) }

// Close flushes the underlying file descriptor.
func (l *Logger) Close() error {
	if l == nil || l.writer == nil {
		return nil
	}
	return l.writer.Close()
}

type secretValue string

// Secret marks a structured field as sensitive. Redaction remains centralized
// in this package so callers cannot accidentally use inconsistent masking.
func Secret(key, value string) slog.Attr {
	return slog.Any(key, secretValue(value))
}

// MaskSecret preserves four characters at either end and replaces the middle
// with exactly eight asterisks. Short secrets are hidden completely.
func MaskSecret(value string) string {
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 8 {
		return maskedSecret
	}
	return string(runes[:4]) + maskedSecret + string(runes[len(runes)-4:])
}

func redactAttr(_ []string, attr slog.Attr) slog.Attr {
	if secret, ok := attr.Value.Any().(secretValue); ok {
		return slog.String(attr.Key, MaskSecret(string(secret)))
	}
	if !sensitiveKey(attr.Key) {
		return attr
	}
	switch attr.Value.Kind() {
	case slog.KindString:
		return slog.String(attr.Key, MaskSecret(attr.Value.String()))
	case slog.KindAny:
		return slog.String(attr.Key, MaskSecret(fmt.Sprint(attr.Value.Any())))
	default:
		return slog.String(attr.Key, maskedSecret)
	}
}

func sensitiveKey(key string) bool {
	key = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return '_'
	}, key)
	parts := strings.FieldsFunc(key, func(r rune) bool { return r == '_' })
	for _, part := range parts {
		switch part {
		case "password", "passwd", "token", "secret", "cookie", "authorization", "session":
			return true
		}
	}
	return strings.HasSuffix(key, "api_key") || strings.HasSuffix(key, "access_key")
}

type rotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxSize  int64
	maxFiles int
	file     *os.File
	size     int64
}

func newRotatingWriter(path string, maxSize int64, maxFiles int) (*rotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	w := &rotatingWriter{path: path, maxSize: maxSize, maxFiles: maxFiles}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return fmt.Errorf("log path is not a regular file: %s", w.path)
	}
	w.file = file
	w.size = info.Size()
	return nil
}

func (w *rotatingWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return w.fallback(data, errors.New("log file is closed"))
	}
	if w.size > 0 && w.size+int64(len(data)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return w.fallback(data, fmt.Errorf("rotate application log: %w", err))
		}
	}
	n, err := w.file.Write(data)
	w.size += int64(n)
	if err != nil {
		return w.fallback(data[n:], fmt.Errorf("write application log: %w", err))
	}
	return n, nil
}

func (w *rotatingWriter) fallback(data []byte, err error) (int, error) {
	_, _ = fmt.Fprintf(os.Stderr, "application logger failure: %v\n", err)
	_, _ = os.Stderr.Write(data)
	// Logging failures must not recursively re-enter the logger or stop request work.
	return len(data), nil
}

func (w *rotatingWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil
	if w.maxFiles == 1 {
		file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			_ = w.open()
			return err
		}
		w.file = file
		w.size = 0
		return nil
	}

	oldest := fmt.Sprintf("%s.%d", w.path, w.maxFiles-1)
	if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = w.open()
		return err
	}
	for index := w.maxFiles - 2; index >= 1; index-- {
		source := fmt.Sprintf("%s.%d", w.path, index)
		target := fmt.Sprintf("%s.%d", w.path, index+1)
		if err := os.Rename(source, target); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = w.open()
			return err
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = w.open()
		return err
	}
	return w.open()
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

var _ io.Writer = (*rotatingWriter)(nil)
