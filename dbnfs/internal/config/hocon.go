package config

// A strict parser for the subset of HOCON that configuration files like
//
//	database {
//	    url = "jdbc:oracle:thin:@//localhost:1521/svc"
//	    username = "bbbb"
//	}
//
// actually use. Anything outside the subset is an error with a line number,
// never a silently different value.
//
// Supported:
//   - objects: `key { ... }`, `key = { ... }`, `key : { ... }`, a root wrapped in braces
//   - dotted keys: `a.b.c = 1` (quoted keys are not split)
//   - separators: newlines and commas
//   - comments: `# ...` and `// ...`
//   - quoted strings with JSON escapes, triple-quoted raw strings `"""..."""`
//   - unquoted scalars: numbers, booleans, simple words
//   - arrays of scalars: `[ "a", "b" ]`
//   - environment substitution as a whole value: `${VAR}` (required), `${?VAR}` (optional)
//   - repeated keys: objects merge, the last scalar wins
//
// Not supported (reported as errors): include, +=, value concatenation,
// substitutions of config paths, nested arrays and arrays of objects.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type tokKind int

const (
	tEOF tokKind = iota
	tNewline
	tComma
	tLBrace
	tRBrace
	tLBrack
	tRBrack
	tSep // '=' or ':'
	tString
	tUnquoted
	tSubst
)

func (k tokKind) String() string {
	return [...]string{"end of file", "newline", "','", "'{'", "'}'", "'['", "']'", "'=' or ':'",
		"quoted string", "value", "substitution"}[k]
}

type token struct {
	kind     tokKind
	text     string
	optional bool // for ${?VAR}
	line     int
}

// scalar is a leaf value.
type scalar struct {
	text   string
	quoted bool
	line   int
}

type hArray []scalar
type hObject map[string]any // values: scalar, hArray, hObject

type lexer struct {
	src  string
	pos  int
	line int
}

func (l *lexer) errf(format string, a ...any) error {
	return fmt.Errorf("line %d: %s", l.line, fmt.Sprintf(format, a...))
}

// Characters that may not appear in unquoted text (HOCON spec, plus '/' handled for comments).
const forbiddenUnquoted = "$\"{}[]:=,+#`^?!@*&\\"

func (l *lexer) next() (token, error) {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			l.pos++
		case c == '#' || strings.HasPrefix(l.src[l.pos:], "//"):
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		default:
			goto scan
		}
	}
	return token{kind: tEOF, line: l.line}, nil

scan:
	c := l.src[l.pos]
	line := l.line
	simple := map[byte]tokKind{'\n': tNewline, ',': tComma, '{': tLBrace, '}': tRBrace, '[': tLBrack, ']': tRBrack, '=': tSep, ':': tSep}
	if k, ok := simple[c]; ok {
		l.pos++
		if c == '\n' {
			l.line++
		}
		return token{kind: k, text: string(c), line: line}, nil
	}
	switch {
	case strings.HasPrefix(l.src[l.pos:], `"""`):
		return l.rawString()
	case c == '"':
		return l.quotedString()
	case strings.HasPrefix(l.src[l.pos:], "${"):
		end := strings.IndexByte(l.src[l.pos:], '}')
		if end < 0 {
			return token{}, l.errf("unterminated substitution")
		}
		name := l.src[l.pos+2 : l.pos+end]
		l.pos += end + 1
		tok := token{kind: tSubst, line: line}
		if strings.HasPrefix(name, "?") {
			tok.optional = true
			name = name[1:]
		}
		if name == "" || strings.ContainsAny(name, " \t\n\"$") {
			return token{}, l.errf("invalid substitution ${%s}", name)
		}
		tok.text = name
		return tok, nil
	case c == '+' && strings.HasPrefix(l.src[l.pos:], "+="):
		return token{}, l.errf("'+=' is not supported")
	}

	start := l.pos
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || strings.IndexByte(forbiddenUnquoted, c) >= 0 ||
			strings.HasPrefix(l.src[l.pos:], "//") {
			break
		}
		l.pos++
	}
	if l.pos == start {
		return token{}, l.errf("unexpected character %q (quote the value)", c)
	}
	text := l.src[start:l.pos]
	if !utf8.ValidString(text) {
		return token{}, l.errf("invalid UTF-8")
	}
	return token{kind: tUnquoted, text: text, line: line}, nil
}

func (l *lexer) rawString() (token, error) {
	line := l.line
	body := l.src[l.pos+3:]
	end := strings.Index(body, `"""`)
	if end < 0 {
		return token{}, l.errf(`unterminated """ string`)
	}
	// A run of more than three quotes ends with the last three.
	for end+3 < len(body) && body[end+3] == '"' {
		end++
	}
	text := body[:end]
	l.line += strings.Count(text, "\n")
	l.pos += 3 + end + 3
	return token{kind: tString, text: text, line: line}, nil
}

func (l *lexer) quotedString() (token, error) {
	line := l.line
	var b strings.Builder
	i := l.pos + 1
	for {
		if i >= len(l.src) || l.src[i] == '\n' {
			return token{}, l.errf("unterminated string")
		}
		c := l.src[i]
		if c == '"' {
			l.pos = i + 1
			return token{kind: tString, text: b.String(), line: line}, nil
		}
		if c != '\\' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(l.src) {
			return token{}, l.errf("unterminated escape")
		}
		esc := l.src[i+1]
		i += 2
		switch esc {
		case '"', '\\', '/':
			b.WriteByte(esc)
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			r, n, err := decodeUnicodeEscape(l.src[i:])
			if err != nil {
				return token{}, l.errf("%v", err)
			}
			b.WriteRune(r)
			i += n
		default:
			return token{}, l.errf("invalid escape \\%c", esc)
		}
	}
}

// decodeUnicodeEscape decodes XXXX (after "\u"), including surrogate pairs.
func decodeUnicodeEscape(s string) (rune, int, error) {
	hex4 := func(s string) (rune, bool) {
		if len(s) < 4 {
			return 0, false
		}
		v, err := strconv.ParseUint(s[:4], 16, 16)
		return rune(v), err == nil
	}
	r, ok := hex4(s)
	if !ok {
		return 0, 0, fmt.Errorf("invalid \\u escape")
	}
	if !utf16.IsSurrogate(r) {
		return r, 4, nil
	}
	if len(s) >= 10 && s[4] == '\\' && s[5] == 'u' {
		if r2, ok := hex4(s[6:]); ok {
			if d := utf16.DecodeRune(r, r2); d != utf8.RuneError {
				return d, 10, nil
			}
		}
	}
	return 0, 0, fmt.Errorf("invalid surrogate pair in \\u escape")
}

type parser struct {
	lex    *lexer
	peeked *token
}

func (p *parser) peek() (token, error) {
	if p.peeked == nil {
		t, err := p.lex.next()
		if err != nil {
			return t, err
		}
		p.peeked = &t
	}
	return *p.peeked, nil
}

func (p *parser) take() (token, error) {
	t, err := p.peek()
	p.peeked = nil
	return t, err
}

func errAt(t token, format string, a ...any) error {
	return fmt.Errorf("line %d: %s", t.line, fmt.Sprintf(format, a...))
}

// parseHOCON parses text into an object tree.
func parseHOCON(text string) (hObject, error) {
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("config is not valid UTF-8")
	}
	text = strings.TrimPrefix(text, "\uFEFF")
	p := &parser{lex: &lexer{src: text, line: 1}}

	// Optional braces around the root.
	if err := p.skipSeparators(); err != nil {
		return nil, err
	}
	t, err := p.peek()
	if err != nil {
		return nil, err
	}
	root := hObject{}
	if t.kind == tLBrace {
		if _, err := p.take(); err != nil {
			return nil, err
		}
		if err := p.parseBody(root, true); err != nil {
			return nil, err
		}
		if err := p.skipSeparators(); err != nil {
			return nil, err
		}
		if t, err := p.take(); err != nil {
			return nil, err
		} else if t.kind != tEOF {
			return nil, errAt(t, "unexpected %s after root object", t.kind)
		}
		return root, nil
	}
	return root, p.parseBody(root, false)
}

func (p *parser) skipSeparators() error {
	for {
		t, err := p.peek()
		if err != nil {
			return err
		}
		if t.kind != tNewline && t.kind != tComma {
			return nil
		}
		p.peeked = nil
	}
}

func (p *parser) skipNewlines() error {
	for {
		t, err := p.peek()
		if err != nil {
			return err
		}
		if t.kind != tNewline {
			return nil
		}
		p.peeked = nil
	}
}

func (p *parser) parseBody(obj hObject, braced bool) error {
	for {
		if err := p.skipSeparators(); err != nil {
			return err
		}
		t, err := p.take()
		if err != nil {
			return err
		}
		switch {
		case t.kind == tRBrace && braced:
			return nil
		case t.kind == tEOF && !braced:
			return nil
		case t.kind == tEOF:
			return errAt(t, "missing '}'")
		case t.kind != tString && t.kind != tUnquoted:
			return errAt(t, "expected a key, got %s", t.kind)
		}
		keyTok := t
		path := []string{t.text}
		if t.kind == tUnquoted {
			path = strings.Split(t.text, ".")
			for _, seg := range path {
				if seg == "" {
					return errAt(t, "invalid key %q", t.text)
				}
			}
		}

		t, err = p.take()
		if err != nil {
			return err
		}
		var value any
		var skip bool
		switch t.kind {
		case tLBrace:
			child := hObject{}
			if err := p.parseBody(child, true); err != nil {
				return err
			}
			value = child
		case tSep:
			if err := p.skipNewlines(); err != nil {
				return err
			}
			value, skip, err = p.parseValue()
			if err != nil {
				return err
			}
		default:
			if keyTok.kind == tUnquoted && keyTok.text == "include" {
				return errAt(keyTok, "include is not supported")
			}
			return errAt(t, "expected '=', ':' or '{' after key %q, got %s", keyTok.text, t.kind)
		}

		// A value must be followed by a separator or the end of the object.
		t, err = p.peek()
		if err != nil {
			return err
		}
		switch t.kind {
		case tNewline, tComma, tEOF:
		case tRBrace:
			if !braced {
				return errAt(t, "unexpected '}'")
			}
		default:
			return errAt(t, "unexpected %s after value of %q (value concatenation is not supported; quote the value)",
				t.kind, keyTok.text)
		}
		if !skip {
			setPath(obj, path, value)
		}
	}
}

// parseValue parses the value after a separator. skip reports an unset ${?VAR}.
func (p *parser) parseValue() (value any, skip bool, err error) {
	t, err := p.take()
	if err != nil {
		return nil, false, err
	}
	switch t.kind {
	case tLBrace:
		child := hObject{}
		return child, false, p.parseBody(child, true)
	case tLBrack:
		arr, err := p.parseArray()
		return arr, false, err
	case tString:
		return scalar{text: t.text, quoted: true, line: t.line}, false, nil
	case tUnquoted:
		return scalar{text: t.text, line: t.line}, false, nil
	case tSubst:
		v, ok := os.LookupEnv(t.text)
		if !ok {
			if t.optional {
				return nil, true, nil
			}
			return nil, false, errAt(t, "environment variable %s is not set (use ${?%s} if optional)", t.text, t.text)
		}
		return scalar{text: v, quoted: true, line: t.line}, false, nil
	default:
		return nil, false, errAt(t, "expected a value, got %s", t.kind)
	}
}

func (p *parser) parseArray() (hArray, error) {
	arr := hArray{}
	for {
		if err := p.skipSeparators(); err != nil {
			return nil, err
		}
		t, err := p.take()
		if err != nil {
			return nil, err
		}
		switch t.kind {
		case tRBrack:
			return arr, nil
		case tString:
			arr = append(arr, scalar{text: t.text, quoted: true, line: t.line})
		case tUnquoted:
			arr = append(arr, scalar{text: t.text, line: t.line})
		case tSubst:
			if v, ok := os.LookupEnv(t.text); ok {
				arr = append(arr, scalar{text: v, quoted: true, line: t.line})
			} else if !t.optional {
				return nil, errAt(t, "environment variable %s is not set", t.text)
			}
		case tEOF:
			return nil, errAt(t, "missing ']'")
		default:
			return nil, errAt(t, "arrays may only contain scalar values, got %s", t.kind)
		}
		n, err := p.peek()
		if err != nil {
			return nil, err
		}
		if n.kind != tComma && n.kind != tNewline && n.kind != tRBrack {
			return nil, errAt(n, "unexpected %s in array", n.kind)
		}
	}
}

// setPath assigns value at path, merging objects and overriding everything else.
func setPath(obj hObject, path []string, value any) {
	for _, seg := range path[:len(path)-1] {
		child, ok := obj[seg].(hObject)
		if !ok {
			child = hObject{}
			obj[seg] = child
		}
		obj = child
	}
	last := path[len(path)-1]
	if newObj, ok := value.(hObject); ok {
		if oldObj, ok := obj[last].(hObject); ok {
			for k, v := range newObj {
				setPath(oldObj, []string{k}, v)
			}
			return
		}
	}
	obj[last] = value
}

// lookup returns the value at a dotted path, or nil.
func (o hObject) lookup(path string) any {
	var cur any = o
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(hObject)
		if !ok {
			return nil
		}
		if cur, ok = m[seg]; !ok {
			return nil
		}
	}
	return cur
}
