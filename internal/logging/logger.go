package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type Level int

const (
	ErrorLevel Level = iota
	WarnLevel
	InfoLevel
	DebugLevel
)

type Logger struct {
	mu    sync.Mutex
	level Level
	out   io.Writer
	close io.Closer
}

func ParseLevel(v string) Level {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "error":
		return ErrorLevel
	case "warn":
		return WarnLevel
	case "debug":
		return DebugLevel
	default:
		return InfoLevel
	}
}

func New(level string) *Logger {
	return &Logger{
		level: ParseLevel(level),
		out:   os.Stderr,
	}
}

func NewWithOutput(level string, out io.Writer) *Logger {
	if out == nil {
		out = os.Stderr
	}
	return &Logger{
		level: ParseLevel(level),
		out:   out,
	}
}

func NewWithFile(level string, path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Logger{
		level: ParseLevel(level),
		out:   f,
		close: f,
	}, nil
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.close == nil {
		return nil
	}
	err := l.close.Close()
	l.close = nil
	return err
}

func (l *Logger) log(level Level, tag string, msg string, meta any) {
	if level > l.level {
		return
	}

	line := fmt.Sprintf("%s [%s] %s", time.Now().Format(time.RFC3339), tag, msg)
	if meta != nil {
		if b, err := json.Marshal(meta); err == nil {
			line += " " + string(b)
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	target := l.out
	if target == nil {
		target = os.Stderr
	}
	_, _ = fmt.Fprintln(target, line)
}

func (l *Logger) Error(msg string, meta any) { l.log(ErrorLevel, "error", msg, meta) }
func (l *Logger) Warn(msg string, meta any)  { l.log(WarnLevel, "warn", msg, meta) }
func (l *Logger) Info(msg string, meta any)  { l.log(InfoLevel, "info", msg, meta) }
func (l *Logger) Debug(msg string, meta any) { l.log(DebugLevel, "debug", msg, meta) }
