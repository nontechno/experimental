package main

import (
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

	exclusionsFileName := configGet("exclusions")
	if len(exclusionsFileName) > 0 {
		if raw, err := os.ReadFile(exclusionsFileName); err == nil {
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
