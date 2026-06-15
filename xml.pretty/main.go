package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
)

// nsScope tracks prefix→URI declarations as a stack of frames (one per element).
// Empty-string prefix = default namespace (xmlns="...").
type nsScope struct {
	frames []map[string]string
}

func (s *nsScope) push() {
	s.frames = append(s.frames, nil)
}

func (s *nsScope) pop() {
	s.frames = s.frames[:len(s.frames)-1]
}

func (s *nsScope) declare(prefix, uri string) {
	top := &s.frames[len(s.frames)-1]
	if *top == nil {
		*top = make(map[string]string)
	}
	(*top)[prefix] = uri
}

// lookupURI returns the prefix (possibly "") that maps to uri, searching
// from innermost to outermost frame. Returns ("", false) if not found.
func (s *nsScope) lookupURI(uri string) (string, bool) {
	for i := len(s.frames) - 1; i >= 0; i-- {
		for p, u := range s.frames[i] {
			if u == uri {
				return p, true
			}
		}
	}
	return "", false
}

// declaredInParent returns true when prefix→uri appears in any frame
// *before* the current (top) frame.
func (s *nsScope) declaredInParent(prefix, uri string) bool {
	for i := len(s.frames) - 2; i >= 0; i-- {
		if v, ok := s.frames[i][prefix]; ok {
			return v == uri
		}
	}
	return false
}

// processStart rewrites a StartElement so the encoder emits no redundant xmlns.
// Must be called after scope.push() for this element's frame.
func processStart(t xml.StartElement, scope *nsScope) xml.StartElement {
	// 1. Harvest explicit xmlns / xmlns:pfx attrs, register in current frame.
	var nsAttrs []xml.Attr
	var otherAttrs []xml.Attr
	for _, a := range t.Attr {
		if a.Name.Local == "xmlns" && a.Name.Space == "" {
			scope.declare("", a.Value)
			nsAttrs = append(nsAttrs, a)
		} else if a.Name.Space == "xmlns" {
			scope.declare(a.Name.Local, a.Value)
			nsAttrs = append(nsAttrs, a)
		} else {
			otherAttrs = append(otherAttrs, a)
		}
	}

	// 2. Handle implicit element namespace (from Name.Space, no Attr present).
	if t.Name.Space != "" {
		if _, ok := scope.lookupURI(t.Name.Space); ok {
			// Already in scope — just suppress.
			t.Name.Space = ""
		} else {
			// New namespace — emit xmlns="..." once, register, suppress Space.
			scope.declare("", t.Name.Space)
			nsAttrs = append(nsAttrs, xml.Attr{
				Name:  xml.Name{Local: "xmlns"},
				Value: t.Name.Space,
			})
			t.Name.Space = ""
		}
	}

	// 3. Suppress Space on non-xmlns attributes whose namespace is in scope.
	for i, a := range otherAttrs {
		if a.Name.Space == "" {
			continue
		}
		if _, ok := scope.lookupURI(a.Name.Space); ok {
			otherAttrs[i].Name.Space = ""
		}
	}

	// 4. Drop xmlns attrs already declared identically in a parent frame.
	var keptNS []xml.Attr
	for _, a := range nsAttrs {
		prefix := ""
		if a.Name.Space == "xmlns" {
			prefix = a.Name.Local
		}
		if !scope.declaredInParent(prefix, a.Value) {
			keptNS = append(keptNS, a)
		}
	}

	t.Attr = append(keptNS, otherAttrs...)
	return t
}

// xmlDecl extracts the raw <?xml ...?> declaration from input, if present.
func xmlDecl(input []byte) string {
	trimmed := bytes.TrimSpace(input)
	if !bytes.HasPrefix(trimmed, []byte("<?xml")) {
		return ""
	}
	end := bytes.Index(trimmed, []byte("?>"))
	if end < 0 {
		return ""
	}
	return string(trimmed[:end+2])
}

func prettyXML(input []byte) ([]byte, error) {
	decoder := xml.NewDecoder(bytes.NewReader(input))
	decoder.Strict = false

	var buf bytes.Buffer

	if decl := xmlDecl(input); decl != "" {
		buf.WriteString(decl)
		buf.WriteByte('\n')
	}

	encoder := xml.NewEncoder(&buf)
	encoder.Indent("", "  ")

	var scope nsScope
	// depth tracks nesting so we can pop synchronously (defer in a loop
	// defers until function return, which is wrong).
	depth := 0

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse error: %w", err)
		}

		switch t := token.(type) {
		case xml.StartElement:
			scope.push()
			depth++
			token = processStart(t, &scope)

		case xml.EndElement:
			// Clear Space so encoder doesn't inject xmlns on closing tags.
			t.Name.Space = ""
			token = t
			// Encode first, then pop — so the encoder still sees correct depth.
			if err := encoder.EncodeToken(token); err != nil {
				return nil, fmt.Errorf("encode error: %w", err)
			}
			depth--
			scope.pop()
			continue // already encoded, skip the encode below
		}

		if err := encoder.EncodeToken(token); err != nil {
			if cd, ok := token.(xml.CharData); ok && strings.TrimSpace(string(cd)) == "" {
				continue
			}
			return nil, fmt.Errorf("encode error: %w", err)
		}
	}

	_ = depth

	if err := encoder.Flush(); err != nil {
		return nil, fmt.Errorf("flush error: %w", err)
	}

	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func main() {
	var input []byte
	var err error

	switch len(os.Args) {
	case 1:
		input, err = io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading stdin: %v\n", err)
			os.Exit(1)
		}
	case 2:
		path := os.Args[1]
		if path == "-" {
			input, err = io.ReadAll(os.Stdin)
		} else {
			input, err = os.ReadFile(path)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading file: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "usage: xmlfmt [file|-]\n")
		os.Exit(2)
	}

	pretty, err := prettyXML(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	os.Stdout.Write(pretty)
}
