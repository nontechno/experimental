package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Target is a parsed Oracle connect target.
type Target struct {
	Host    string
	Port    int
	Service string // set for service-name URLs
	SID     string // set for host:port:SID URLs
}

func (t Target) String() string {
	hp := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	if t.SID != "" {
		return hp + ":" + t.SID
	}
	return "//" + hp + "/" + t.Service
}

const jdbcPrefix = "jdbc:oracle:thin:@"

// ParseJDBCURL parses the EZConnect forms of an Oracle JDBC thin URL:
//
//	jdbc:oracle:thin:@//host:port/service
//	jdbc:oracle:thin:@//host/service          (port 1521)
//	jdbc:oracle:thin:@host:port/service
//	jdbc:oracle:thin:@host:port:SID
//	jdbc:oracle:thin:@//[::1]:1521/service    (IPv6)
//
// TNS descriptors "(DESCRIPTION=...)", TNS aliases, wallets and URL
// parameters are rejected rather than half-supported.
func ParseJDBCURL(u string) (Target, error) {
	var t Target
	if len(u) < len(jdbcPrefix) || !strings.EqualFold(u[:len(jdbcPrefix)], jdbcPrefix) {
		return t, fmt.Errorf("must start with %q", jdbcPrefix)
	}
	rest := strings.TrimSpace(u[len(jdbcPrefix):])
	switch {
	case rest == "":
		return t, errors.New("missing host")
	case strings.HasPrefix(rest, "("):
		return t, errors.New("TNS descriptors are not supported; use //host:port/service")
	case strings.ContainsAny(rest, "?& \t"):
		return t, errors.New("URL parameters are not supported")
	}

	slashes := strings.HasPrefix(rest, "//")
	rest = strings.TrimPrefix(rest, "//")

	// Split off the service name (after the first '/' that follows the host part).
	hostPort, service, hasService := cutAfterHost(rest, '/')
	if hasService {
		if service == "" || strings.Contains(service, "/") {
			return t, errors.New("invalid service name")
		}
		t.Service = service
	} else {
		if slashes {
			return t, errors.New("missing /service after host")
		}
		// host:port:SID
		hp, sid, ok := cutLastColon(hostPort)
		if !ok || sid == "" {
			return t, errors.New("expected //host:port/service or host:port:SID")
		}
		t.SID = sid
		hostPort = hp
	}

	host, port, err := splitHostPortDefault(hostPort, 1521)
	if err != nil {
		return t, err
	}
	t.Host, t.Port = host, port
	return t, nil
}

// cutAfterHost splits s at the first sep that is not inside [IPv6] brackets.
func cutAfterHost(s string, sep byte) (before, after string, found bool) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case sep:
			if depth == 0 {
				return s[:i], s[i+1:], true
			}
		}
	}
	return s, "", false
}

func cutLastColon(s string) (before, after string, ok bool) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 || strings.LastIndexByte(s, ']') > i {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}

func splitHostPortDefault(hp string, def int) (string, int, error) {
	if hp == "" {
		return "", 0, errors.New("missing host")
	}
	host, portStr := hp, ""
	if strings.HasPrefix(hp, "[") {
		end := strings.IndexByte(hp, ']')
		if end < 0 {
			return "", 0, errors.New("unterminated IPv6 address")
		}
		host = hp[1:end]
		tail := hp[end+1:]
		if tail != "" {
			if !strings.HasPrefix(tail, ":") {
				return "", 0, fmt.Errorf("invalid host %q", hp)
			}
			portStr = tail[1:]
		}
	} else if i := strings.IndexByte(hp, ':'); i >= 0 {
		host, portStr = hp[:i], hp[i+1:]
	}
	if host == "" || strings.ContainsAny(host, ":/[]@") && net.ParseIP(host) == nil {
		return "", 0, fmt.Errorf("invalid host %q", host)
	}
	port := def
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil || p < 1 || p > 65535 {
			return "", 0, fmt.Errorf("invalid port %q", portStr)
		}
		port = p
	}
	return host, port, nil
}
