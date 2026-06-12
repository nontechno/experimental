package main

import (
	"fmt"
	"os"
	"strings"
	"unicode"
)

const (
	garbage = " \t\r\n"
)

func split(what string) (string, string, bool) {
	cut := 0
	if tab := strings.Index(what, "\t"); tab > 0 {
		cut = tab
	}
	if space := strings.Index(what, "  "); space > 0 {
		if cut == 0 || space < cut {
			cut = space
		}
	}
	if colon := strings.Index(what, ": "); colon > 0 {
		if cut == 0 || colon < cut {
			cut = colon
		}
	}

	if cut == 0 {
		return "", "", false
	}
	left := trim(what[:cut])
	right := trim(what[cut+1:])

	if len(left) == 0 || len(right) == 0 {
		return "", "", false
	}

	if open, close := strings.Index(left, "("), strings.Index(left, ")"); open > 0 && close > 0 && open < close {
		insert := trim(left[open+1 : close])
		left = trim(left[:open])

		if len(insert) > 0 {
			right = right + "; " + insert
		}
	}

	return left, right, true
}

func isAlphanumeric(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func trim(original string) string {
	return strings.Trim(original, garbage)
}

func getExclusionList() map[string]bool {
	exclusionList := make(map[string]bool)

	exclusionsFileName := trim(configGet("exclusions"))
	if len(exclusionsFileName) > 0 {
		if raw, err := os.ReadFile(exclusionsFileName); err == nil {
			trace("opened file [%s]", exclusionsFileName)
			lines := strings.Split(string(raw), "\n")
			for _, line := range lines {
				line = strings.ToLower(trim(line))
				if len(line) > 0 {
					exclusionList[line] = true
				}
			}
		}
	}

	return exclusionList
}

func getHiglightList() []string {
	highlightsList := []string{}

	highlightsFileName := trim(configGet("highlights"))
	if len(highlightsFileName) > 0 {
		if raw, err := os.ReadFile(highlightsFileName); err == nil {
			trace("opened file [%s]", highlightsFileName)
			lines := strings.Split(string(raw), "\n")
			for _, line := range lines {
				line = strings.ToLower(trim(line))
				if len(line) > 0 {
					highlightsList = append(highlightsList, line)
				}
			}
		} else {
			failure("error: failed to open file [%s]: %v", highlightsFileName, err)
		}
	}

	return highlightsList
}

func isAlphanumericUnicode(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			return false
		}
	}
	return true
}

func isAlphanumericOrSpace(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != ' ' && r != '-' && r != '\'' {
			return false
		}
	}
	return true
}

func isAlphanumericOrDot(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '.' {
			return false
		}
	}
	return true
}

func isTraceEnabled() bool {
	if txt := configGet("trace"); len(txt) > 0 {
		return txt == "true" || txt == "enable" || txt == "yes"
	}
	return false
}

func trace(format string, args ...interface{}) {
	if isTraceEnabled() {
		fmt.Fprintf(os.Stderr, "trace: "+format+"\n", args...)
	}
}
