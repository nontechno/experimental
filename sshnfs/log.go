package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	nfs "github.com/willscott/go-nfs"
)

type Level int

const (
	LevelPanic Level = iota
	LevelFatal
	LevelError
	LevelWarn
	LevelInfo
	LevelDebug
	LevelTrace
)

func parseLogLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "panic":
		return LevelPanic, nil
	case "fatal":
		return LevelFatal, nil
	case "error":
		return LevelError, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "info", "":
		return LevelInfo, nil
	case "debug":
		return LevelDebug, nil
	case "trace":
		return LevelTrace, nil
	}
	return LevelInfo, fmt.Errorf("unknown log level %q", s)
}

func (l Level) nfsLevel() nfs.LogLevel {
	switch l {
	case LevelPanic:
		return nfs.PanicLevel
	case LevelFatal:
		return nfs.FatalLevel
	case LevelError:
		return nfs.ErrorLevel
	case LevelWarn:
		return nfs.WarnLevel
	case LevelInfo:
		return nfs.InfoLevel
	case LevelDebug:
		return nfs.DebugLevel
	default:
		return nfs.TraceLevel
	}
}

// Logger is a tiny leveled logger writing to stderr. Derived loggers share the
// parent's mutex so lines from concurrent mounts do not interleave.
type Logger struct {
	mu     *sync.Mutex
	out    io.Writer
	level  Level
	prefix string
}

func NewLogger(level Level) *Logger {
	return &Logger{mu: &sync.Mutex{}, out: os.Stderr, level: level}
}

// With returns a logger that tags every line with a mount's name.
func (l *Logger) With(prefix string) *Logger {
	c := *l
	c.prefix = prefix
	return &c
}

func (l *Logger) logf(lvl Level, tag, format string, args ...interface{}) {
	if lvl > l.level {
		return
	}
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.prefix != "" {
		fmt.Fprintf(l.out, "%s [%s] %s\n", tag, l.prefix, msg)
		return
	}
	fmt.Fprintf(l.out, "%s %s\n", tag, msg)
}

func (l *Logger) Errorf(format string, args ...interface{}) {
	l.logf(LevelError, "[error]", format, args...)
}

func (l *Logger) Warnf(format string, args ...interface{}) {
	l.logf(LevelWarn, "[warn] ", format, args...)
}

func (l *Logger) Infof(format string, args ...interface{}) {
	l.logf(LevelInfo, "[info] ", format, args...)
}

func (l *Logger) Debugf(format string, args ...interface{}) {
	l.logf(LevelDebug, "[debug]", format, args...)
}
