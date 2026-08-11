package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	go_ora "github.com/sijms/go-ora/v2"
)

// Target is a resolved Oracle listener endpoint.
type Target struct {
	Host    string
	Port    int
	Service string // service name (mutually exclusive with SID)
	SID     string
	TCPS    bool
}

// String renders the target the way a human would write it.
func (t Target) String() string {
	host := t.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if t.SID != "" {
		return fmt.Sprintf("%s:%d:%s", host, t.Port, t.SID)
	}
	return fmt.Sprintf("%s:%d/%s", host, t.Port, t.Service)
}

var (
	jdbcPrefix = regexp.MustCompile(`(?i)^jdbc:oracle:(thin|oci|oci8):`)
	descField  = func(name string) *regexp.Regexp {
		return regexp.MustCompile(`(?i)\(\s*` + name + `\s*=\s*([^)\s]+)\s*\)`)
	}
	hostRe     = descField("HOST")
	portRe     = descField("PORT")
	serviceRe  = descField("SERVICE_NAME")
	sidRe      = descField("SID")
	protocolRe = descField("PROTOCOL")
)

// ParseJDBCURL turns an Oracle JDBC thin URL into a Target. Recognised forms:
//
//	jdbc:oracle:thin:@//host:port/service
//	jdbc:oracle:thin:@host:port/service
//	jdbc:oracle:thin:@host:port:SID
//	jdbc:oracle:thin:@host/service
//	jdbc:oracle:thin:@(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=h)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=s)))
//
// TNS aliases (jdbc:oracle:thin:@myalias) cannot be resolved without a
// tnsnames.ora and are rejected with a clear message.
func ParseJDBCURL(raw string) (Target, error) {
	var t Target
	s := strings.TrimSpace(raw)

	loc := jdbcPrefix.FindStringIndex(s)
	if loc == nil {
		return t, fmt.Errorf("not an Oracle JDBC URL (expected a jdbc:oracle:thin:@... prefix): %q", raw)
	}
	s = s[loc[1]:]

	at := strings.Index(s, "@")
	if at < 0 {
		return t, fmt.Errorf("malformed JDBC URL, missing '@': %q", raw)
	}
	// Anything between the driver prefix and '@' is user/password; the config
	// file is the authoritative source for credentials, so it is ignored.
	s = strings.TrimSpace(s[at+1:])
	if s == "" {
		return t, fmt.Errorf("malformed JDBC URL, nothing after '@': %q", raw)
	}

	if strings.HasPrefix(s, "(") {
		return parseDescriptor(s, raw)
	}
	return parseHostForm(s, raw)
}

// parseDescriptor extracts an endpoint from a TNS DESCRIPTOR. Only the first
// ADDRESS is used; failover / load-balanced address lists are not expanded.
func parseDescriptor(desc, raw string) (Target, error) {
	var t Target

	if m := hostRe.FindStringSubmatch(desc); m != nil {
		t.Host = m[1]
	} else {
		return t, fmt.Errorf("TNS descriptor has no HOST: %q", raw)
	}
	if m := portRe.FindStringSubmatch(desc); m != nil {
		p, err := strconv.Atoi(m[1])
		if err != nil {
			return t, fmt.Errorf("TNS descriptor has an invalid PORT %q", m[1])
		}
		t.Port = p
	} else {
		t.Port = 1521
	}
	if m := serviceRe.FindStringSubmatch(desc); m != nil {
		t.Service = m[1]
	} else if m := sidRe.FindStringSubmatch(desc); m != nil {
		t.SID = m[1]
	} else {
		return t, fmt.Errorf("TNS descriptor has neither SERVICE_NAME nor SID: %q", raw)
	}
	if m := protocolRe.FindStringSubmatch(desc); m != nil && strings.EqualFold(m[1], "TCPS") {
		t.TCPS = true
	}
	return t, nil
}

// parseHostForm handles the //host:port/service and host:port:SID shapes.
func parseHostForm(s, raw string) (Target, error) {
	var t Target
	s = strings.TrimPrefix(s, "//")

	// A bare word with no separators is a TNS alias, which needs tnsnames.ora.
	if !strings.ContainsAny(s, ":/") {
		return t, fmt.Errorf("%q looks like a TNS alias; use an explicit "+
			"jdbc:oracle:thin:@//host:port/service URL instead", raw)
	}

	// Split off the service name if the URL uses the '/service' form.
	hostPort := s
	if i := strings.LastIndex(s, "/"); i >= 0 {
		hostPort = s[:i]
		t.Service = s[i+1:]
		if t.Service == "" {
			return t, fmt.Errorf("malformed JDBC URL, empty service name: %q", raw)
		}
	}

	// IPv6 literals are bracketed: [::1]:1521
	if strings.HasPrefix(hostPort, "[") {
		end := strings.Index(hostPort, "]")
		if end < 0 {
			return t, fmt.Errorf("malformed IPv6 host in %q", raw)
		}
		t.Host = hostPort[1:end]
		rest := strings.TrimPrefix(hostPort[end+1:], ":")
		return finishHostForm(t, rest, raw)
	}

	parts := strings.Split(hostPort, ":")
	t.Host = parts[0]
	if t.Host == "" {
		return t, fmt.Errorf("malformed JDBC URL, empty host: %q", raw)
	}
	switch len(parts) {
	case 1:
		t.Port = 1521
	case 2:
		return finishHostForm(t, parts[1], raw)
	case 3:
		// host:port:SID
		if t.Service != "" {
			return t, fmt.Errorf("malformed JDBC URL, both SID and service name given: %q", raw)
		}
		t.SID = parts[2]
		return finishHostForm(t, parts[1], raw)
	default:
		return t, fmt.Errorf("malformed JDBC URL: %q", raw)
	}

	if t.Service == "" && t.SID == "" {
		return t, fmt.Errorf("malformed JDBC URL, no service name or SID: %q", raw)
	}
	return t, nil
}

func finishHostForm(t Target, portStr, raw string) (Target, error) {
	if portStr == "" {
		t.Port = 1521
	} else {
		p, err := strconv.Atoi(portStr)
		if err != nil {
			return t, fmt.Errorf("malformed JDBC URL, invalid port %q in %q", portStr, raw)
		}
		t.Port = p
	}
	if t.Service == "" && t.SID == "" {
		return t, fmt.Errorf("malformed JDBC URL, no service name or SID: %q", raw)
	}
	return t, nil
}

// BuildDSN produces the go-ora connection string for the configured database.
func BuildDSN(cfg *DatabaseConfig) (Target, string, error) {
	t, err := ParseJDBCURL(cfg.URL)
	if err != nil {
		return t, "", err
	}

	opts := make(map[string]string, len(cfg.Options)+2)
	for k, v := range cfg.Options {
		opts[k] = v
	}
	if t.SID != "" {
		opts["SID"] = t.SID
	}
	if t.TCPS {
		opts["SSL"] = "TRUE"
		if _, ok := opts["SSL VERIFY"]; !ok {
			opts["SSL VERIFY"] = "TRUE"
		}
	}

	return t, go_ora.BuildUrl(t.Host, t.Port, t.Service, cfg.Username, cfg.Password, opts), nil
}

// RedactDSN hides the password so a DSN can safely be logged.
func RedactDSN(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	scheme := strings.Index(dsn, "://")
	if at < 0 || scheme < 0 || at < scheme {
		return dsn
	}
	userinfo := dsn[scheme+3 : at]
	if i := strings.Index(userinfo, ":"); i >= 0 {
		userinfo = userinfo[:i] + ":****"
	}
	return dsn[:scheme+3] + userinfo + dsn[at:]
}
