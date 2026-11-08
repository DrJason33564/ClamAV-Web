package applog

import (
	"fmt"
	"strings"
	"time"
)

// RecordLog keeps user-visible scan records independent of the diagnostic
// LOG_LEVEL, while sharing the application's concurrent, bounded file writer.
type RecordLog struct{ writer *rotatingWriter }

func NewRecordLog(path string, maxSize int64, maxFiles int) (*RecordLog, error) {
	w, err := newRotatingWriter(path, maxSize, maxFiles)
	if err != nil {
		return nil, err
	}
	return &RecordLog{w}, nil
}

func (l *RecordLog) Write(level, message string) {
	message = strings.NewReplacer("\r", "\\r", "\n", "\\n", "\x00", "\\0").Replace(message)
	_, _ = l.writer.Write([]byte(fmt.Sprintf("%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), level, message)))
}
func (l *RecordLog) Close() error { return l.writer.Close() }
