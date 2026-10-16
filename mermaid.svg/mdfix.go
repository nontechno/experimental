package main

import (
	"regexp"
	"strings"
)

var (
	summaryEnd   = regexp.MustCompile(`(?i)</summary>\s*$`)
	detailsClose = regexp.MustCompile(`(?i)^\s*</details>`)
	fenceOpen    = regexp.MustCompile("^\\s*(`{3,}|~{3,})")
)

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

// FixDetailsBlankLines inserts the blank lines CommonMark needs around
// <details>/<summary> so Markdown inside them gets parsed. It is idempotent
// and leaves fenced code blocks untouched. CRLF input is normalized to LF.
func FixDetailsBlankLines(md string) string {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	lines := strings.Split(md, "\n")
	out := make([]string, 0, len(lines)+8)

	var fence string // marker of the currently open code fence, "" if none
	pending := false // a blank line is owed before the next non-blank line

	for _, line := range lines {
		if pending && !isBlank(line) {
			out = append(out, "")
		}
		pending = false

		// Inside a fenced block: copy verbatim, watch for the closing fence.
		if fence != "" {
			out = append(out, line)
			t := strings.TrimSpace(line)
			if len(t) >= len(fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			}
			continue
		}

		// Opening fence? (A backtick fence can't have backticks in its info
		// string, which excludes inline code like ```x``` on one line.)
		if m := fenceOpen.FindStringSubmatch(line); m != nil {
			rest := strings.TrimSpace(line)[len(m[1]):]
			if !(m[1][0] == '`' && strings.Contains(rest, "`")) {
				fence = m[1]
				out = append(out, line)
				continue
			}
		}

		if detailsClose.MatchString(line) {
			if len(out) > 0 && !isBlank(out[len(out)-1]) {
				out = append(out, "")
			}
			out = append(out, line)
			pending = true
			continue
		}

		out = append(out, line)
		if summaryEnd.MatchString(line) {
			pending = true
		}
	}
	return strings.Join(out, "\n")
}
