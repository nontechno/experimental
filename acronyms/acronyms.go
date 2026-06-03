package main

import (
	"bufio"
	"fmt"
	"log"
	"maps"
	"os"
	"slices"
	"strings"
	"unicode"
)

const (
	garbage = " \t\r\n"
)

type (
	Entry struct {
		OriginalKey string
		Values      []string
	}

	Holder struct {
		data map[string]Entry
	}
)

func (h *Holder) add(key, val string) {
	normalized := strings.ToLower(key)
	already, found := h.data[normalized]
	if !found {
		already = Entry{
			OriginalKey: key,
			Values:      []string{val},
		}
	} else {
		already.Values = append(already.Values, val)
	}
	h.data[normalized] = already
}

func (h *Holder) print() {
	sortedKeys := slices.Sorted(maps.Keys(h.data))
	maxKeyLen := 0
	for _, key := range sortedKeys {
		if len(key) > maxKeyLen {
			maxKeyLen = len(key)
		}
	}

	format := fmt.Sprintf("| %%-%vs | %%s |\n", maxKeyLen)

	for _, key := range sortedKeys {
		entry := h.data[key]
		right := strings.Join(entry.Values, "; ")
		fmt.Printf(format, entry.OriginalKey, right)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: acronyms <file>")
		return
	}

	// 1. Open the file
	file, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	// 2. Ensure the file is closed when the function finishes
	defer file.Close()

	// 3. Create a scanner for the file
	scanner := bufio.NewScanner(file)

	store := Holder{make(map[string]Entry)}
	discarded := []string{}

	// 4. Scan through the file line by line
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.Trim(line, garbage)
		if len(line) == 0 {
			// discarded = append(discarded, line)
			continue
		}

		if !isAlphanumeric([]rune(line)[0]) {
			discarded = append(discarded, line)
			continue
		}

		if left, right, found := split(line); found {
			store.add(left, right)
			//fmt.Printf("[%s]\t\t[%s]\n", left, right) // Print the current line
		} else {
			discarded = append(discarded, line)
		}
	}

	// 5. Check for errors that occurred during scanning
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}

	store.print()

	if len(discarded) > 0 {
		fmt.Printf("\ndiscarded %v entries: [%v]\n", len(discarded), strings.Join(discarded, "; "))
	}
}

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
	left := strings.Trim(what[:cut], garbage)
	right := strings.Trim(what[cut+1:], garbage)

	if len(left) == 0 || len(right) == 0 {
		return "", "", false
	}

	if open, close := strings.Index(left, "("), strings.Index(left, ")"); open > 0 && close > 0 && open < close {
		insert := strings.Trim(left[open+1:close], garbage)
		left = strings.Trim(left[:open], garbage)

		if len(insert) > 0 {
			right = right + "; " + insert
		}
	}

	return left, right, true
}

func isAlphanumeric(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
