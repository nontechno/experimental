// Package config loads otel.sink's configuration from a YAML file,
// layered on top of built-in defaults.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Verbosity levels for the console sink.
const (
	VerbosityNone     = "none"     // print nothing per batch
	VerbosityBasic    = "basic"    // one line per received batch
	VerbosityNormal   = "normal"   // one line per span / metric / log record
	VerbosityDetailed = "detailed" // include resource and record attributes
)

// TLS configures server-side TLS for a receiver.
type TLS struct {
	Enabled bool `yaml:"enabled"`
	// CertFile and KeyFile are the PEM-encoded server certificate and key.
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// ClientCAFile, when set, turns on mutual TLS: clients must present a
	// certificate signed by this CA.
	ClientCAFile string `yaml:"client_ca_file"`
}

// Endpoint is one listening socket.
type Endpoint struct {
	Enabled  bool   `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"`
	TLS      TLS    `yaml:"tls"`
}

// Config is the whole configuration tree.
type Config struct {
	OTLP struct {
		GRPC Endpoint `yaml:"grpc"`
		HTTP Endpoint `yaml:"http"`
		// MaxRequestSizeMiB caps a single export request on both transports.
		MaxRequestSizeMiB int `yaml:"max_request_size_mib"`
	} `yaml:"otlp"`

	API struct {
		Enabled  bool   `yaml:"enabled"`
		Endpoint string `yaml:"endpoint"`
	} `yaml:"api"`

	Console struct {
		Verbosity string `yaml:"verbosity"`
	} `yaml:"console"`

	File struct {
		Enabled bool   `yaml:"enabled"`
		Dir     string `yaml:"dir"`
		// Format is jsonl (one object per line, append-safe) or json
		// (a single array, closed on shutdown).
		Format string `yaml:"format"`
	} `yaml:"file"`

	// UDS streams every record over a Unix domain socket as newline
	// delimited JSON.
	UDS struct {
		Enabled bool   `yaml:"enabled"`
		Path    string `yaml:"path"`
		// Mode is listen (otel.sink creates the socket) or dial (it
		// connects to a socket someone else created).
		Mode string `yaml:"mode"`
		// Envelope wraps each record as {"signal":..., "record":...} so one
		// socket can carry all three signals unambiguously.
		Envelope bool `yaml:"envelope"`
		// Buffer is how many batches to queue before dropping. The socket
		// never blocks the export path.
		Buffer int `yaml:"buffer"`
	} `yaml:"uds"`

	// Forward re-exports every batch to another OTLP endpoint, which makes
	// otel.sink a proxy: capture locally and pass everything on.
	Forward struct {
		Enabled bool `yaml:"enabled"`
		// Endpoint is host:port for grpc, or a base URL for http.
		Endpoint string `yaml:"endpoint"`
		Protocol string `yaml:"protocol"`
		// Mode is raw (send the batch untouched) or filtered (send what the
		// filter chain kept, with attribute edits applied).
		Mode        string            `yaml:"mode"`
		Insecure    bool              `yaml:"insecure"`
		CAFile      string            `yaml:"ca_file"`
		CertFile    string            `yaml:"cert_file"`
		KeyFile     string            `yaml:"key_file"`
		Headers     map[string]string `yaml:"headers"`
		Compression string            `yaml:"compression"`
		Timeout     string            `yaml:"timeout"`
		Retries     int               `yaml:"retries"`
		// FailOpen keeps local capture working when the upstream is down.
		// Turn it off to make otel.sink behave as a strict proxy, reporting
		// the failure to the sending SDK.
		FailOpen bool `yaml:"fail_open"`
	} `yaml:"forward"`

	// Filters run in order, ahead of every output. See internal/filter.
	Filters []FilterSpec `yaml:"filters"`

	Store struct {
		MaxSpans   int `yaml:"max_spans"`
		MaxMetrics int `yaml:"max_metrics"`
		MaxLogs    int `yaml:"max_logs"`
	} `yaml:"store"`
}

// FilterSpec selects a registered filter and passes it options.
type FilterSpec struct {
	Name    string            `yaml:"name"`
	Options map[string]string `yaml:"options"`
}

// Default returns a usable configuration: OTLP on the standard ports,
// query API on 8080, one line printed per record, 10k records kept in memory.
func Default() *Config {
	c := &Config{}
	c.OTLP.GRPC.Enabled = true
	c.OTLP.GRPC.Endpoint = "0.0.0.0:4317"
	c.OTLP.HTTP.Enabled = true
	c.OTLP.HTTP.Endpoint = "0.0.0.0:4318"
	c.OTLP.MaxRequestSizeMiB = 16
	c.API.Enabled = true
	c.API.Endpoint = "0.0.0.0:8080"
	c.Console.Verbosity = VerbosityNormal
	c.File.Enabled = false
	c.File.Dir = "./data"
	c.File.Format = "jsonl"
	c.UDS.Enabled = false
	c.UDS.Path = "/tmp/otel.sink.sock"
	c.UDS.Mode = "listen"
	c.UDS.Envelope = true
	c.UDS.Buffer = 1024
	c.Forward.Enabled = false
	c.Forward.Protocol = "grpc"
	c.Forward.Mode = "raw"
	c.Forward.Insecure = true
	c.Forward.Compression = "none"
	c.Forward.Timeout = "10s"
	c.Forward.Retries = 1
	c.Forward.FailOpen = true
	c.Store.MaxSpans = 10000
	c.Store.MaxMetrics = 10000
	c.Store.MaxLogs = 10000
	return c
}

// Load reads path on top of Default(). An empty path returns the defaults.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, cfg.Validate()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, cfg.Validate()
}

// Validate checks for settings that would fail at runtime.
func (c *Config) Validate() error {
	switch c.Console.Verbosity {
	case VerbosityNone, VerbosityBasic, VerbosityNormal, VerbosityDetailed:
	default:
		return fmt.Errorf("console.verbosity %q: want none, basic, normal or detailed", c.Console.Verbosity)
	}
	if !c.OTLP.GRPC.Enabled && !c.OTLP.HTTP.Enabled {
		return fmt.Errorf("both OTLP receivers are disabled: nothing would be captured")
	}
	if c.OTLP.MaxRequestSizeMiB <= 0 {
		return fmt.Errorf("otlp.max_request_size_mib must be greater than 0")
	}
	for name, ep := range map[string]Endpoint{"otlp.grpc": c.OTLP.GRPC, "otlp.http": c.OTLP.HTTP} {
		if !ep.Enabled {
			continue
		}
		if ep.Endpoint == "" {
			return fmt.Errorf("%s.endpoint is empty", name)
		}
		if ep.TLS.Enabled && (ep.TLS.CertFile == "" || ep.TLS.KeyFile == "") {
			return fmt.Errorf("%s.tls needs both cert_file and key_file", name)
		}
	}
	if c.UDS.Enabled {
		if c.UDS.Path == "" {
			return fmt.Errorf("uds.path is empty")
		}
		if c.UDS.Mode != "listen" && c.UDS.Mode != "dial" {
			return fmt.Errorf("uds.mode %q: want listen or dial", c.UDS.Mode)
		}
	}
	if c.Forward.Enabled {
		if c.Forward.Endpoint == "" {
			return fmt.Errorf("forward.endpoint is empty")
		}
		switch c.Forward.Protocol {
		case "grpc", "http", "http/protobuf":
		default:
			return fmt.Errorf("forward.protocol %q: want grpc or http", c.Forward.Protocol)
		}
		switch c.Forward.Mode {
		case "raw", "filtered":
		default:
			return fmt.Errorf("forward.mode %q: want raw or filtered", c.Forward.Mode)
		}
		if _, err := c.ForwardTimeout(); err != nil {
			return err
		}
	}
	for i, f := range c.Filters {
		if f.Name == "" {
			return fmt.Errorf("filters[%d]: name is empty", i)
		}
	}
	if c.API.Enabled && c.API.Endpoint == "" {
		return fmt.Errorf("api.endpoint is empty")
	}
	if c.File.Enabled {
		if c.File.Dir == "" {
			return fmt.Errorf("file.dir is empty")
		}
		if c.File.Format != "jsonl" && c.File.Format != "json" {
			return fmt.Errorf("file.format %q: want jsonl or json", c.File.Format)
		}
	}
	return nil
}

// ForwardTimeout parses the configured per-attempt timeout.
func (c *Config) ForwardTimeout() (time.Duration, error) {
	if strings.TrimSpace(c.Forward.Timeout) == "" {
		return 10 * time.Second, nil
	}
	d, err := time.ParseDuration(c.Forward.Timeout)
	if err != nil {
		return 0, fmt.Errorf("forward.timeout %q: %w", c.Forward.Timeout, err)
	}
	return d, nil
}

// MaxRequestBytes is the request cap in bytes.
func (c *Config) MaxRequestBytes() int {
	return c.OTLP.MaxRequestSizeMiB * 1024 * 1024
}
