package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

// asyncWriter decouples logging from stderr. A write to a terminal that is
// paused (Ctrl-S, a stopped pager) or to a pipe nobody drains blocks, and a
// logger that blocks freezes whatever was logging: an NFS request, the
// reconnect loop, the very timeout path meant to recover from a hang. Lines
// are queued instead and written by one goroutine; when the queue is full
// they are dropped and counted, never waited for.
type asyncWriter struct {
	out     io.Writer
	lines   chan []byte
	dropped atomic.Int64
	idle    chan struct{} // signalled whenever the queue drains

	mu      sync.Mutex
	pending int
}

func newAsyncWriter(out io.Writer, depth int) *asyncWriter {
	w := &asyncWriter{out: out, lines: make(chan []byte, depth), idle: make(chan struct{}, 1)}
	go w.run()
	return w
}

// Write queues p; it never blocks. Each call is written out whole, so lines
// from concurrent goroutines do not interleave.
func (w *asyncWriter) Write(p []byte) (int, error) {
	line := append([]byte(nil), p...)
	w.mu.Lock()
	w.pending++
	w.mu.Unlock()
	select {
	case w.lines <- line:
	default:
		w.dropped.Add(1)
		w.done()
	}
	return len(p), nil
}

func (w *asyncWriter) done() {
	w.mu.Lock()
	w.pending--
	empty := w.pending == 0
	w.mu.Unlock()
	if empty {
		select {
		case w.idle <- struct{}{}:
		default:
		}
	}
}

func (w *asyncWriter) run() {
	for line := range w.lines {
		if n := w.dropped.Swap(0); n > 0 {
			fmt.Fprintf(w.out, "[warn]  %d log lines dropped: stderr is not keeping up\n", n)
		}
		w.out.Write(line)
		w.done()
	}
}

// Flush waits up to d for queued lines to be written, so the last messages
// before exit are not lost, without letting a blocked stderr hold up exit.
func (w *asyncWriter) Flush(d time.Duration) {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for {
		w.mu.Lock()
		empty := w.pending == 0
		w.mu.Unlock()
		if empty {
			return
		}
		select {
		case <-w.idle:
		case <-deadline.C:
			return
		}
	}
}

// stderr is the process-wide log sink. go-nfs logs through the standard log
// package, which main points here as well.
var stderr = newAsyncWriter(os.Stderr, 4096)

// Logger is a tiny leveled logger writing to stderr through the shared
// non-blocking sink.
type Logger struct {
	out    io.Writer
	level  Level
	prefix string
}

func NewLogger(level Level) *Logger {
	return &Logger{out: stderr, level: level}
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
	// One Write per line, so the line reaches the sink whole.
	if l.prefix != "" {
		io.WriteString(l.out, fmt.Sprintf("%s [%s] %s\n", tag, l.prefix, msg))
		return
	}
	io.WriteString(l.out, fmt.Sprintf("%s %s\n", tag, msg))
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
