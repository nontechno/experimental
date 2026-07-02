// Package mdtitle extracts the title from a Markdown document.
//
// Priority order:
//  1. YAML front matter `title:` field
//  2. First ATX heading (`# Title`)
//  3. Setext-style H1 heading (underline with `=`)
package main

import (
	"strings"
)

// Extract returns the title of the given Markdown document, or an empty
// string if no title can be found.
func ExtractTitle(md string) string {
	if t := frontMatterTitle(md); t != "" {
		return t
	}
	return headingTitle(md)
}

// frontMatterTitle parses a YAML front matter block (delimited by ---) and
// returns the value of the `title` key, or "" if not present.
func frontMatterTitle(md string) string {
	// Strip optional UTF-8 BOM.
	md = strings.TrimPrefix(md, "\xef\xbb\xbf")

	if !strings.HasPrefix(md, "---") {
		return ""
	}
	rest := md[3:]
	// The closing delimiter may be "---" or "...".
	end := -1
	for _, delim := range []string{"\n---", "\n..."} {
		if i := strings.Index(rest, delim); i != -1 && (end == -1 || i < end) {
			end = i
		}
	}
	if end == -1 {
		return ""
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == "title" {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// headingTitle scans for the first H1 heading using both ATX (`# Title`) and
// setext (underline with `=`) syntax.
func headingTitle(md string) string {
	lines := strings.Split(md, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// ATX heading.
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(trimmed[2:])
		}

		// Setext H1: the previous non-empty line is the title.
		if i > 0 && isSetextH1(trimmed) {
			if prev := strings.TrimSpace(lines[i-1]); prev != "" {
				return prev
			}
		}
	}
	return ""
}

// isSetextH1 reports whether line is a setext H1 underline (one or more `=`).
func isSetextH1(line string) bool {
	return len(line) > 0 && strings.Trim(line, "=") == ""
}
