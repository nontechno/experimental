// jsoncconv: convert JSONC (JSON with comments & trailing commas) to strict JSON.
//
// Usage:
//
//	jsoncconv [input.jsonc]           # reads file, writes to stdout
//	jsoncconv [input.jsonc] [out.json] # reads file, writes to file
//	cat file.jsonc | jsoncconv        # reads stdin, writes to stdout
//
// Handles:
//   - // line comments
//   - /* block comments */
//   - Trailing commas in objects and arrays
//   - All of the above inside string literals are left untouched
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

func main() {
	var (
		in  io.Reader = os.Stdin
		out io.Writer = os.Stdout
	)

	args := os.Args[1:]

	if len(args) >= 1 {
		f, err := os.Open(args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error opening input: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	}

	if len(args) >= 2 {
		f, err := os.Create(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error creating output: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		out = f
	}

	src, err := io.ReadAll(in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading input: %v\n", err)
		os.Exit(1)
	}

	result, err := convertJSONC(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if _, err := out.Write(result); err != nil {
		fmt.Fprintf(os.Stderr, "error writing output: %v\n", err)
		os.Exit(1)
	}
}

// convertJSONC strips comments and trailing commas from JSONC, returning
// valid JSON. It uses a single-pass state machine — no regex, no allocations
// per character beyond the output buffer.
func convertJSONC(src []byte) ([]byte, error) {
	type state int
	const (
		stNormal           state = iota
		stString                 // inside "..."
		stStringEsc              // inside string, after backslash
		stLineComment            // after //
		stBlockComment           // after /*
		stBlockCommentStar       // after /* ... *
	)

	out := make([]byte, 0, len(src))
	st := stNormal

	for i := 0; i < len(src); i++ {
		c := src[i]

		switch st {
		case stString:
			out = append(out, c)
			switch c {
			case '\\':
				st = stStringEsc
			case '"':
				st = stNormal
			}

		case stStringEsc:
			out = append(out, c)
			st = stString

		case stLineComment:
			// consume until newline; emit the newline so line numbers stay intact
			if c == '\n' {
				out = append(out, c)
				st = stNormal
			}

		case stBlockComment:
			if c == '*' {
				st = stBlockCommentStar
			}
			// preserve newlines so line numbers stay intact
			if c == '\n' {
				out = append(out, c)
			}

		case stBlockCommentStar:
			if c == '/' {
				st = stNormal
			} else {
				if c == '\n' {
					out = append(out, c)
				}
				if c != '*' {
					st = stBlockComment
				}
				// stay in stBlockCommentStar if c == '*'
			}

		case stNormal:
			switch {
			case c == '"':
				out = append(out, c)
				st = stString

			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				i++ // skip second '/'
				st = stLineComment

			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				i++ // skip '*'
				st = stBlockComment

			default:
				out = append(out, c)
			}
		}
	}

	if st == stString || st == stStringEsc {
		return nil, fmt.Errorf("unterminated string literal")
	}
	if st == stBlockComment || st == stBlockCommentStar {
		return nil, fmt.Errorf("unterminated block comment")
	}

	return stripTrailingCommas(out), nil
}

// stripTrailingCommas removes trailing commas before } or ] in a single pass.
// It skips over string literals so commas inside strings are never touched.
//
// Strategy: walk forward, tracking the last comma position. When we hit a
// closing bracket/brace (outside a string), blank out the last comma if there
// is nothing but whitespace between it and the closing token.
func stripTrailingCommas(src []byte) []byte {
	out := bytes.Clone(src) // work in-place on a copy

	type state int
	const (
		stNormal state = iota
		stString
		stStringEsc
	)

	st := stNormal
	lastComma := -1 // index of the most recent unmatched comma

	for i := 0; i < len(out); i++ {
		c := out[i]

		switch st {
		case stString:
			switch c {
			case '\\':
				st = stStringEsc
			case '"':
				st = stNormal
			}
		case stStringEsc:
			st = stString

		case stNormal:
			switch c {
			case '"':
				lastComma = -1 // commas before a string key/value are valid
				st = stString

			case ',':
				lastComma = i

			case '}', ']':
				if lastComma >= 0 && onlyWhitespaceBetween(out, lastComma+1, i) {
					out[lastComma] = ' '
				}
				lastComma = -1

			case ' ', '\t', '\r', '\n':
				// whitespace: don't update lastComma

			default:
				// any non-whitespace, non-special token means the comma
				// is followed by a real value — it's not trailing
				lastComma = -1
			}
		}
	}

	return out
}

// onlyWhitespaceBetween reports whether out[start:end] contains only whitespace.
func onlyWhitespaceBetween(b []byte, start, end int) bool {
	for i := start; i < end; i++ {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
		default:
			return false
		}
	}
	return true
}
