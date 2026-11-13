// Package logx is the log channel. It writes to stderr (and optionally a file),
// never to stdout: stdout belongs to the stats lines.
package logx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelOff
)

func ParseLevel(s string) Level {
	switch s {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelOff
	}
}

type Logger struct {
	mu    sync.Mutex
	level Level
	out   io.Writer
	file  *os.File
}

// New opens the optional log file. Failing to open it is a hard error: a
// --log-file that silently does nothing is worse than no log file at all.
func New(level string, path string, out io.Writer) (*Logger, error) {
	if out == nil {
		out = os.Stderr
	}
	l := &Logger{level: ParseLevel(level), out: out}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("log-file dir: %w", err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("log-file: %w", err)
		}
		l.file = f
	}
	return l, nil
}

func (l *Logger) Enabled(v Level) bool { return l.level != LevelOff && v >= l.level }

func (l *Logger) write(v Level, tag, format string, args ...any) {
	if !l.Enabled(v) {
		return
	}
	line := fmt.Sprintf("%s %s %s\n",
		time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), tag, fmt.Sprintf(format, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.out, line)
	if l.file != nil {
		_, _ = io.WriteString(l.file, line)
	}
}

func (l *Logger) Debug(f string, a ...any) { l.write(LevelDebug, "debug", f, a...) }
func (l *Logger) Info(f string, a ...any)  { l.write(LevelInfo, "info", f, a...) }
func (l *Logger) Warn(f string, a ...any)  { l.write(LevelWarn, "warn", f, a...) }
func (l *Logger) Error(f string, a ...any) { l.write(LevelError, "error", f, a...) }

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}
