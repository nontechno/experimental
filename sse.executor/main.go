package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Config represents the external JSON configuration file.
type Config struct {
	// SSE server base URL, e.g. "https://example.com/events"
	ServerURL string `json:"server_url"`

	// Topic to subscribe to; appended as a query param or path segment
	// depending on your server. Configure url_template to override.
	Topic string `json:"topic"`

	// Optional: full URL template overriding server_url+topic composition.
	// Use "{topic}" as placeholder, e.g. "https://host/sse?channel={topic}"
	URLTemplate string `json:"url_template,omitempty"`

	// Optional static HTTP headers sent with every request (auth tokens, etc.)
	Headers map[string]string `json:"headers,omitempty"`

	// Shell script (or any executable) to run when an event arrives.
	// The event data is passed as the first argument and also via the
	// SSE_EVENT_DATA / SSE_EVENT_ID / SSE_EVENT_TYPE env vars.
	Script string `json:"script"`

	// Optional extra arguments prepended before the event-data argument.
	ScriptArgs []string `json:"script_args,omitempty"`

	// Maximum time (seconds) to wait for the script to finish. Default 30.
	ScriptTimeoutSec int `json:"script_timeout_sec,omitempty"`

	// Filter: only trigger the script for these event types.
	// Empty list means "all event types".
	EventTypes []string `json:"event_types,omitempty"`

	Reconnect ReconnectConfig `json:"reconnect"`

	// Log level: "debug" | "info" | "warn" | "error". Default "info".
	LogLevel string `json:"log_level,omitempty"`
}

type ReconnectConfig struct {
	// Initial delay before the first reconnect attempt (seconds). Default 1.
	InitialDelaySec float64 `json:"initial_delay_sec,omitempty"`
	// Maximum delay cap (seconds). Default 60.
	MaxDelaySec float64 `json:"max_delay_sec,omitempty"`
	// Exponential back-off multiplier. Default 2.0.
	Multiplier float64 `json:"multiplier,omitempty"`
	// If the connection stays healthy for this many seconds, reset the back-off
	// counter. Default 30.
	ResetAfterSec float64 `json:"reset_after_sec,omitempty"`
	// Maximum number of reconnect attempts. 0 = unlimited.
	MaxAttempts int `json:"max_attempts,omitempty"`
}

// defaults fills zero-value fields with sensible defaults.
func (c *Config) defaults() {
	if c.ScriptTimeoutSec == 0 {
		c.ScriptTimeoutSec = 30
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	r := &c.Reconnect
	if r.InitialDelaySec == 0 {
		r.InitialDelaySec = 1
	}
	if r.MaxDelaySec == 0 {
		r.MaxDelaySec = 60
	}
	if r.Multiplier == 0 {
		r.Multiplier = 2.0
	}
	if r.ResetAfterSec == 0 {
		r.ResetAfterSec = 30
	}
}

// sseEvent holds a parsed SSE event.
type sseEvent struct {
	ID    string
	Type  string // defaults to "message" per spec
	Data  string
	Retry int // ms hint from server (unused beyond logging)
}

func main() {
	cfgPath := flag.String("config", "config.json", "path to JSON config file")
	flag.Parse()

	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read config %q: %v\n", *cfgPath, err)
		os.Exit(1)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "cannot parse config: %v\n", err)
		os.Exit(1)
	}
	cfg.defaults()

	logger := buildLogger(cfg.LogLevel)

	if cfg.ServerURL == "" && cfg.URLTemplate == "" {
		logger.Error("config must have server_url or url_template")
		os.Exit(1)
	}
	if cfg.Script == "" {
		logger.Error("config must have script")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	svc := &service{cfg: cfg, log: logger}
	svc.run(ctx)
	logger.Info("shutdown complete")
}

// service encapsulates the reconnect loop and event handling.
type service struct {
	cfg Config
	log *slog.Logger
}

func (s *service) run(ctx context.Context) {
	r := s.cfg.Reconnect
	delay := r.InitialDelaySec
	attempt := 0

	for {
		attempt++
		if r.MaxAttempts > 0 && attempt > r.MaxAttempts {
			s.log.Error("maximum reconnect attempts reached, giving up",
				"max_attempts", r.MaxAttempts)
			return
		}

		connectedAt := time.Now()
		s.log.Info("connecting to SSE stream",
			"url", s.sseURL(),
			"attempt", attempt)

		err := s.subscribe(ctx)

		select {
		case <-ctx.Done():
			s.log.Info("context cancelled, not reconnecting")
			return
		default:
		}

		if err != nil {
			s.log.Warn("SSE stream error", "err", err, "attempt", attempt)
		}

		// Reset back-off if the connection was healthy long enough.
		if time.Since(connectedAt).Seconds() >= r.ResetAfterSec {
			s.log.Debug("connection was stable, resetting back-off")
			delay = r.InitialDelaySec
			attempt = 0
		}

		s.log.Info("reconnecting after delay", "delay_sec", fmt.Sprintf("%.1f", delay))
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(delay * float64(time.Second))):
		}

		delay = math.Min(delay*r.Multiplier, r.MaxDelaySec)
	}
}

// subscribe opens one SSE connection and reads until error or context cancel.
func (s *service) subscribe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.sseURL(), nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	for k, v := range s.cfg.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{} // no timeout: SSE is a long-lived stream
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		s.log.Warn("unexpected Content-Type", "content_type", ct)
	}

	s.log.Info("SSE stream connected", "status", resp.StatusCode)
	return s.readStream(ctx, resp.Body)
}

// readStream parses the SSE wire format from r until EOF or error.
func (s *service) readStream(ctx context.Context, r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1 MiB line buffer

	var cur sseEvent
	cur.Type = "message" // spec default

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		line := scanner.Text()

		switch {
		case line == "":
			// Blank line = dispatch event
			if cur.Data != "" {
				s.dispatch(cur)
			}
			cur = sseEvent{Type: "message"}

		case strings.HasPrefix(line, ":"):
			// Comment / keep-alive — log at debug and continue
			s.log.Debug("SSE comment", "line", line)

		case strings.HasPrefix(line, "id:"):
			cur.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))

		case strings.HasPrefix(line, "event:"):
			cur.Type = strings.TrimSpace(strings.TrimPrefix(line, "event:"))

		case strings.HasPrefix(line, "retry:"):
			var ms int
			if _, err := fmt.Sscanf(strings.TrimPrefix(line, "retry:"), "%d", &ms); err == nil {
				cur.Retry = ms
				s.log.Debug("server requested retry interval", "ms", ms)
			}

		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimPrefix(line, "data:")
			if chunk != "" && chunk[0] == ' ' {
				chunk = chunk[1:] // strip single leading space per spec
			}
			if cur.Data == "" {
				cur.Data = chunk
			} else {
				cur.Data = cur.Data + "\n" + chunk
			}

		default:
			s.log.Debug("ignoring unknown SSE field", "line", line)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner: %w", err)
	}
	return io.EOF // clean EOF = reconnect
}

// dispatch decides whether to run the script for this event.
func (s *service) dispatch(ev sseEvent) {
	s.log.Info("event received",
		"type", ev.Type,
		"id", ev.ID,
		"data_len", len(ev.Data))
	s.log.Debug("event data", "data", ev.Data)

	if len(s.cfg.EventTypes) > 0 && !contains(s.cfg.EventTypes, ev.Type) {
		s.log.Debug("event type filtered out", "type", ev.Type)
		return
	}

	go s.runScript(ev) // run asynchronously so stream reading isn't blocked
}

// runScript executes the configured script with the event data.
func (s *service) runScript(ev sseEvent) {
	timeout := time.Duration(s.cfg.ScriptTimeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := append(append([]string{}, s.cfg.ScriptArgs...), ev.Data)
	cmd := exec.CommandContext(ctx, s.cfg.Script, args...)

	cmd.Env = append(os.Environ(),
		"SSE_EVENT_DATA="+ev.Data,
		"SSE_EVENT_TYPE="+ev.Type,
		"SSE_EVENT_ID="+ev.ID,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	log := s.log.With(
		"script", s.cfg.Script,
		"event_type", ev.Type,
		"event_id", ev.ID,
		"elapsed_ms", elapsed.Milliseconds(),
	)

	if stdout.Len() > 0 {
		log.Debug("script stdout", "output", stdout.String())
	}
	if stderr.Len() > 0 {
		log.Warn("script stderr", "output", stderr.String())
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			log.Error("script timed out", "timeout_sec", s.cfg.ScriptTimeoutSec)
		} else {
			log.Error("script failed", "err", err)
		}
		return
	}

	log.Info("script succeeded")
}

// sseURL builds the URL from config.
func (s *service) sseURL() string {
	if s.cfg.URLTemplate != "" {
		return strings.ReplaceAll(s.cfg.URLTemplate, "{topic}", s.cfg.Topic)
	}
	base := strings.TrimRight(s.cfg.ServerURL, "/")
	if s.cfg.Topic == "" {
		return base
	}
	return base + "/" + s.cfg.Topic
}

// buildLogger returns a structured slog.Logger at the requested level.
func buildLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(h)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
