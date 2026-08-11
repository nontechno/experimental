package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// Object is a parsed configuration tree. Values are either string, []any or Object.
type Object map[string]any

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

// ParseConfigFile reads and parses a HOCON-flavoured configuration file:
//
//	database {
//	    url                = "jdbc:oracle:thin:@//localhost:1521/xepdb1"
//	    username           = "scott"
//	    password           = ${ORACLE_PASSWORD}   # env substitution
//	    authenticationType = "PASSWORD"
//	}
//
// Supported: nested blocks, dotted keys (a.b.c = v), '=' or ':' separators,
// quoted and bare values, arrays, # // and /* */ comments, ${VAR} and ${?VAR}
// environment substitution.
func ParseConfigFile(path string) (Object, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseConfig(string(src), path)
}

// ParseConfig parses configuration text. name is used in error messages.
func ParseConfig(src, name string) (Object, error) {
	p := &parser{src: []rune(src), file: name, line: 1}
	obj, err := p.parseBody(true)
	if err != nil {
		return nil, err
	}
	if err := expandTree(obj); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return obj, nil
}

type parser struct {
	src  []rune
	pos  int
	line int
	file string
}

func (p *parser) errf(format string, args ...any) error {
	return fmt.Errorf("%s:%d: %s", p.file, p.line, fmt.Sprintf(format, args...))
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) peek() rune {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

func (p *parser) next() rune {
	r := p.src[p.pos]
	p.pos++
	if r == '\n' {
		p.line++
	}
	return r
}

// skipSpace consumes whitespace and comments.
func (p *parser) skipSpace() error {
	for !p.eof() {
		r := p.peek()
		switch {
		case unicode.IsSpace(r):
			p.next()
		case r == '#':
			p.skipLine()
		case r == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/':
			p.skipLine()
		case r == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '*':
			p.next()
			p.next()
			closed := false
			for !p.eof() {
				if p.peek() == '*' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/' {
					p.next()
					p.next()
					closed = true
					break
				}
				p.next()
			}
			if !closed {
				return p.errf("unterminated block comment")
			}
		default:
			return nil
		}
	}
	return nil
}

func (p *parser) skipLine() {
	for !p.eof() && p.peek() != '\n' {
		p.next()
	}
}

// parseBody parses key/value pairs until EOF (top level) or a closing brace.
func (p *parser) parseBody(top bool) (Object, error) {
	obj := Object{}
	for {
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		if p.eof() {
			if top {
				return obj, nil
			}
			return nil, p.errf("unexpected end of file, expected '}'")
		}
		if p.peek() == '}' {
			if top {
				return nil, p.errf("unexpected '}'")
			}
			p.next()
			return obj, nil
		}
		if p.peek() == ',' || p.peek() == ';' {
			p.next()
			continue
		}

		key, err := p.parseKey()
		if err != nil {
			return nil, err
		}
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		if p.eof() {
			return nil, p.errf("unexpected end of file after key %q", key)
		}

		var val any
		switch p.peek() {
		case '{':
			p.next()
			val, err = p.parseBody(false)
		case '=', ':':
			p.next()
			val, err = p.parseValue()
		default:
			return nil, p.errf("expected '=', ':' or '{' after key %q, got %q", key, string(p.peek()))
		}
		if err != nil {
			return nil, err
		}
		if err := setPath(obj, key, val); err != nil {
			return nil, p.errf("%v", err)
		}
	}
}

// parseKey reads a quoted or bare (possibly dotted) key.
func (p *parser) parseKey() (string, error) {
	if p.peek() == '"' {
		return p.parseQuoted()
	}
	// Unquoted keys may contain spaces ("TRACE FILE"), but never a newline, so
	// that the common `database\n{` block style still parses.
	start := p.pos
	for !p.eof() {
		r := p.peek()
		if r == '\n' || r == '=' || r == ':' || r == '{' || r == '}' || r == ',' {
			break
		}
		p.next()
	}
	key := strings.TrimSpace(string(p.src[start:p.pos]))
	if key == "" {
		return "", p.errf("expected a key, got %q", string(p.peek()))
	}
	return key, nil
}

func (p *parser) parseValue() (any, error) {
	if err := p.skipSpace(); err != nil {
		return nil, err
	}
	if p.eof() {
		return nil, p.errf("unexpected end of file, expected a value")
	}
	switch p.peek() {
	case '{':
		p.next()
		return p.parseBody(false)
	case '[':
		return p.parseArray()
	case '"':
		return p.parseQuoted()
	default:
		return p.parseBare()
	}
}

func (p *parser) parseArray() (any, error) {
	p.next() // consume '['
	var items []any
	for {
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		if p.eof() {
			return nil, p.errf("unexpected end of file, expected ']'")
		}
		if p.peek() == ']' {
			p.next()
			return items, nil
		}
		if p.peek() == ',' {
			p.next()
			continue
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
}

// parseQuoted reads a "..." or """...""" string.
func (p *parser) parseQuoted() (string, error) {
	// Triple-quoted (raw) string.
	if strings.HasPrefix(string(p.src[p.pos:min(p.pos+3, len(p.src))]), `"""`) {
		p.next()
		p.next()
		p.next()
		start := p.pos
		for {
			if p.eof() {
				return "", p.errf("unterminated triple-quoted string")
			}
			if p.peek() == '"' && strings.HasPrefix(string(p.src[p.pos:min(p.pos+3, len(p.src))]), `"""`) {
				s := string(p.src[start:p.pos])
				p.next()
				p.next()
				p.next()
				return s, nil
			}
			p.next()
		}
	}

	p.next() // consume opening quote
	var b strings.Builder
	for {
		if p.eof() {
			return "", p.errf("unterminated string")
		}
		r := p.next()
		switch r {
		case '"':
			return b.String(), nil
		case '\n':
			return "", p.errf("newline inside string literal")
		case '\\':
			if p.eof() {
				return "", p.errf("unterminated escape sequence")
			}
			e := p.next()
			switch e {
			case 'n':
				b.WriteRune('\n')
			case 't':
				b.WriteRune('\t')
			case 'r':
				b.WriteRune('\r')
			case 'b':
				b.WriteRune('\b')
			case 'f':
				b.WriteRune('\f')
			case '\\', '"', '/', '$':
				b.WriteRune(e)
			case 'u':
				if p.pos+4 > len(p.src) {
					return "", p.errf("truncated \\u escape")
				}
				hex := string(p.src[p.pos : p.pos+4])
				n, err := strconv.ParseUint(hex, 16, 32)
				if err != nil {
					return "", p.errf("invalid \\u escape %q", hex)
				}
				for i := 0; i < 4; i++ {
					p.next()
				}
				b.WriteRune(rune(n))
			default:
				return "", p.errf("unknown escape sequence \\%c", e)
			}
		default:
			b.WriteRune(r)
		}
	}
}

// parseBare reads an unquoted value terminated by newline, comma, or '}'.
func (p *parser) parseBare() (string, error) {
	start := p.pos
	for !p.eof() {
		r := p.peek()
		// A ${...} reference is part of the value even though it contains '}'.
		if r == '$' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '{' {
			p.next()
			p.next()
			for !p.eof() && p.peek() != '}' && p.peek() != '\n' {
				p.next()
			}
			if p.eof() || p.peek() == '\n' {
				return "", p.errf("unterminated ${...} reference")
			}
			p.next()
			continue
		}
		if r == '\n' || r == ',' || r == '}' || r == ']' || r == '#' {
			break
		}
		if r == '/' && p.pos+1 < len(p.src) && (p.src[p.pos+1] == '/' || p.src[p.pos+1] == '*') {
			break
		}
		p.next()
	}
	v := strings.TrimSpace(string(p.src[start:p.pos]))
	if v == "" {
		return "", p.errf("empty value")
	}
	return v, nil
}

// setPath assigns val at a dotted path, merging objects rather than replacing.
func setPath(obj Object, path string, val any) error {
	parts := splitKey(path)
	cur := obj
	for i, part := range parts[:len(parts)-1] {
		existing, ok := cur[part]
		if !ok {
			child := Object{}
			cur[part] = child
			cur = child
			continue
		}
		child, ok := existing.(Object)
		if !ok {
			return fmt.Errorf("key %q is not an object", strings.Join(parts[:i+1], "."))
		}
		cur = child
	}
	last := parts[len(parts)-1]
	if newObj, ok := val.(Object); ok {
		if oldObj, ok := cur[last].(Object); ok {
			mergeObject(oldObj, newObj)
			return nil
		}
	}
	cur[last] = val
	return nil
}

func mergeObject(dst, src Object) {
	for k, v := range src {
		if sv, ok := v.(Object); ok {
			if dv, ok := dst[k].(Object); ok {
				mergeObject(dv, sv)
				continue
			}
		}
		dst[k] = v
	}
}

// splitKey splits a dotted key, honouring quoted segments ("my.key".sub).
func splitKey(key string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range key {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == '.' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	parts = append(parts, cur.String())
	return parts
}

// ---------------------------------------------------------------------------
// Environment substitution
// ---------------------------------------------------------------------------

// expandTree rewrites ${VAR} / ${?VAR} references in every string in the tree.
func expandTree(v any) error {
	switch t := v.(type) {
	case Object:
		for k, child := range t {
			if s, ok := child.(string); ok {
				expanded, err := expandEnv(s)
				if err != nil {
					return fmt.Errorf("key %q: %w", k, err)
				}
				t[k] = expanded
				continue
			}
			if err := expandTree(child); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range t {
			if s, ok := child.(string); ok {
				expanded, err := expandEnv(s)
				if err != nil {
					return err
				}
				t[i] = expanded
				continue
			}
			if err := expandTree(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func expandEnv(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '$' && i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated ${...} reference")
			}
			name := s[i+2 : i+end]
			optional := strings.HasPrefix(name, "?")
			name = strings.TrimPrefix(name, "?")
			val, ok := os.LookupEnv(name)
			if !ok && !optional {
				return "", fmt.Errorf("environment variable %q referenced but not set (use ${?%s} to allow it to be empty)", name, name)
			}
			b.WriteString(val)
			i += end + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), nil
}

// ---------------------------------------------------------------------------
// Typed lookups (case-insensitive on each path segment)
// ---------------------------------------------------------------------------

func (o Object) lookup(path string) (any, bool) {
	cur := any(o)
	for _, part := range splitKey(path) {
		obj, ok := cur.(Object)
		if !ok {
			return nil, false
		}
		v, ok := obj[part]
		if !ok {
			// Fall back to a case-insensitive match.
			for k, cand := range obj {
				if strings.EqualFold(k, part) {
					v, ok = cand, true
					break
				}
			}
		}
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// GetString returns the string at path.
func (o Object) GetString(path string) (string, bool) {
	v, ok := o.lookup(path)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// GetObject returns the sub-object at path.
func (o Object) GetObject(path string) (Object, bool) {
	v, ok := o.lookup(path)
	if !ok {
		return nil, false
	}
	obj, ok := v.(Object)
	return obj, ok
}

// StringMap flattens an object of scalars into a map[string]string.
func (o Object) StringMap() map[string]string {
	m := make(map[string]string, len(o))
	for k, v := range o {
		if s, ok := v.(string); ok {
			m[k] = s
		}
	}
	return m
}

// ---------------------------------------------------------------------------
// database { ... } block
// ---------------------------------------------------------------------------

// DatabaseConfig is the typed view of the database block.
type DatabaseConfig struct {
	URL                string
	Username           string
	Password           string
	AuthenticationType string
	Schema             string            // optional: schema to introspect
	Options            map[string]string // optional: extra go-ora URL options
}

// LoadDatabaseConfig parses path and extracts the database block.
func LoadDatabaseConfig(path string) (*DatabaseConfig, error) {
	root, err := ParseConfigFile(path)
	if err != nil {
		return nil, err
	}
	db, ok := root.GetObject("database")
	if !ok {
		return nil, fmt.Errorf("%s: missing 'database { ... }' block", path)
	}

	cfg := &DatabaseConfig{Options: map[string]string{}}
	cfg.URL, _ = db.GetString("url")
	cfg.Username, _ = db.GetString("username")
	cfg.Password, _ = db.GetString("password")
	cfg.AuthenticationType, _ = db.GetString("authenticationType")
	cfg.Schema, _ = db.GetString("schema")
	if opts, ok := db.GetObject("options"); ok {
		cfg.Options = opts.StringMap()
	}

	if cfg.URL == "" {
		return nil, fmt.Errorf("%s: database.url is required", path)
	}
	if cfg.AuthenticationType == "" {
		cfg.AuthenticationType = "PASSWORD"
	}
	switch strings.ToUpper(cfg.AuthenticationType) {
	case "PASSWORD":
		if cfg.Username == "" {
			return nil, fmt.Errorf("%s: database.username is required for authenticationType=PASSWORD", path)
		}
	case "WALLET", "TCPS":
		// Credentials come from the wallet; user/password may be empty.
		if _, ok := cfg.Options["WALLET"]; !ok {
			return nil, fmt.Errorf("%s: authenticationType=%s requires database.options.WALLET (path to the wallet directory)",
				path, cfg.AuthenticationType)
		}
	default:
		return nil, fmt.Errorf("%s: unsupported authenticationType %q (supported: PASSWORD, WALLET)", path, cfg.AuthenticationType)
	}
	return cfg, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
