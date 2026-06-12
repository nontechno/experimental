package main

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
)

// HighlightPattern compiles keywords into a single *regexp.Regexp ready for
// use with Highlight. Call once and reuse the result across many Highlight calls.
func HighlightPattern(keywords []string) *regexp.Regexp {
	if len(keywords) == 0 {
		return nil
	}

	sorted := make([]string, len(keywords))
	copy(sorted, keywords)
	slices.SortFunc(sorted, func(a, b string) int {
		return cmp.Compare(len(b), len(a))
	})

	escaped := make([]string, len(sorted))
	for i, kw := range sorted {
		parts := strings.Fields(kw)
		escapedParts := make([]string, len(parts))
		for j, p := range parts {
			escapedParts[j] = regexp.QuoteMeta(p)
		}
		escaped[i] = `\b` + strings.Join(escapedParts, `\s+`) + `\b`
	}

	return regexp.MustCompile(`(?i)(` + strings.Join(escaped, "|") + `)`)
}

// Highlight wraps each keyword match in original with prefix and suffix.
// Pass a pattern from HighlightPattern; if pattern is nil, original is returned unchanged.
func Highlight(original string, pattern *regexp.Regexp, prefix, suffix string) string {
	if pattern == nil {
		return original
	}
	return pattern.ReplaceAllString(original, prefix+"$1"+suffix)
}
