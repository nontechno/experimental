package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	go_ora "github.com/sijms/go-ora/v2"
)

const defaultOraclePort = 1521

const jdbcThinPrefix = "jdbc:oracle:thin:"

// Target is a parsed Oracle connection target.
type Target struct {
	Host       string
	Port       int
	Service    string
	SID        string
	Descriptor string            // full TNS descriptor, if the URL used one
	Options    map[string]string // extra go-ora URL options
}

// BuildDSN turns the target into a go-ora connection string.
func (t Target) BuildDSN(user, password string) string {
	opts := make(map[string]string, len(t.Options)+1)
	for k, v := range t.Options {
		opts[k] = v
	}
	if t.SID != "" {
		opts["SID"] = t.SID
	}
	if len(opts) == 0 {
		opts = nil
	}

	if t.Descriptor != "" {
		return go_ora.BuildJDBC(user, password, t.Descriptor, opts)
	}
	return go_ora.BuildUrl(t.Host, t.Port, t.Service, user, password, opts)
}

// Redacted returns a DSN safe for logging: the password is replaced.
func (t Target) Redacted(user string) string {
	return t.BuildDSN(user, "REDACTED")
}

// ParseJDBCURL parses a `jdbc:oracle:thin:` URL.
//
// Supported shapes:
//
//	jdbc:oracle:thin:@//host:port/service
//	jdbc:oracle:thin:@//host/service
//	jdbc:oracle:thin:@host:port/service
//	jdbc:oracle:thin:@host:port:SID
//	jdbc:oracle:thin:@tcps://host:port/service
//	jdbc:oracle:thin:@(DESCRIPTION=(ADDRESS=...)(CONNECT_DATA=...))
//	jdbc:oracle:thin:user/pass@//host:port/service  (credentials are ignored;
//	                                                 the config file wins)
func ParseJDBCURL(raw string) (Target, error) {
	var t Target
	t.Port = defaultOraclePort
	t.Options = map[string]string{}

	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(s), jdbcThinPrefix) {
		if strings.HasPrefix(strings.ToLower(s), "jdbc:oracle:") {
			return t, fmt.Errorf("only the JDBC thin driver URL form is supported, got %q", raw)
		}
		return t, fmt.Errorf("not an Oracle JDBC URL: %q", raw)
	}
	s = s[len(jdbcThinPrefix):]

	// Drop any user/password embedded ahead of '@'.
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	} else {
		return t, fmt.Errorf("malformed JDBC URL (missing '@'): %q", raw)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return t, fmt.Errorf("malformed JDBC URL (empty connection target): %q", raw)
	}

	// Full TNS descriptor form.
	if strings.HasPrefix(s, "(") {
		t.Descriptor = s
		return t, nil
	}

	// A bare TNS alias (resolved via tnsnames.ora) cannot be handled here.
	if !strings.Contains(s, "/") && !strings.Contains(s, ":") {
		return t, fmt.Errorf(
			"JDBC URL %q looks like a TNS alias; use an EZConnect URL or a full descriptor instead", raw)
	}

	// Optional protocol prefix (tcp:// or tcps://).
	if i := strings.Index(s, "://"); i > 0 {
		scheme := strings.ToLower(s[:i])
		switch scheme {
		case "tcp":
		case "tcps":
			t.Options["SSL"] = "TRUE"
		default:
			return t, fmt.Errorf("unsupported protocol %q in JDBC URL", scheme)
		}
		s = s[i+3:]
	}
	s = strings.TrimPrefix(s, "//")

	// Trailing query parameters become go-ora options.
	if i := strings.Index(s, "?"); i >= 0 {
		q, err := url.ParseQuery(s[i+1:])
		if err != nil {
			return t, fmt.Errorf("parsing JDBC URL parameters: %w", err)
		}
		for k, v := range q {
			if len(v) > 0 && v[0] != "" {
				t.Options[k] = v[0]
			}
		}
		s = s[:i]
	}

	// Service name after the first '/'.
	hostPart := s
	if i := strings.Index(s, "/"); i >= 0 {
		t.Service = strings.Trim(s[i+1:], "/")
		hostPart = s[:i]
	}

	host, tail, err := splitHost(hostPart)
	if err != nil {
		return t, fmt.Errorf("parsing JDBC URL %q: %w", raw, err)
	}
	t.Host = host

	// tail is "", "port", or "port:SID".
	if tail != "" {
		parts := strings.Split(tail, ":")
		if len(parts) > 2 {
			return t, fmt.Errorf("parsing JDBC URL %q: too many ':'-separated fields", raw)
		}
		if parts[0] != "" {
			p, err := strconv.Atoi(parts[0])
			if err != nil || p <= 0 || p > 65535 {
				return t, fmt.Errorf("parsing JDBC URL %q: invalid port %q", raw, parts[0])
			}
			t.Port = p
		}
		if len(parts) == 2 {
			if t.Service != "" {
				return t, fmt.Errorf("parsing JDBC URL %q: both a SID and a service name given", raw)
			}
			t.SID = parts[1]
		}
	}

	if t.Host == "" {
		return t, fmt.Errorf("parsing JDBC URL %q: missing host", raw)
	}
	if t.Service == "" && t.SID == "" {
		return t, fmt.Errorf("parsing JDBC URL %q: missing service name or SID", raw)
	}

	return t, nil
}

// splitHost separates the host from whatever follows the first ':',
// honouring bracketed IPv6 literals.
func splitHost(s string) (host, tail string, err error) {
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", "", fmt.Errorf("unterminated IPv6 literal")
		}
		host = s[1:end]
		tail = strings.TrimPrefix(s[end+1:], ":")
		return host, tail, nil
	}
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i], s[i+1:], nil
	}
	return s, "", nil
}
