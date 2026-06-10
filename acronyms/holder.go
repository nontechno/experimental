package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
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
		alreadyKnown := false
		for _, existing := range already.Values {
			if strings.EqualFold(existing, val) {
				alreadyKnown = true
			}
		}

		if !alreadyKnown {
			if !isAlphanumericOrSpace(val) {
				// fmt.Fprintf(os.Stderr, "[%s] is not alphanumeric\n", val)
			}

			already.Values = append(already.Values, val)
		}
	}
	h.data[normalized] = already
}

func (h *Holder) print() {
	sortedKeysAll := slices.Sorted(maps.Keys(h.data))

	exclusions := getExclusionList()
	sortedKeys := []string{}
	for _, key := range sortedKeysAll {
		if !exclusions[key] {
			sortedKeys = append(sortedKeys, key)
		}
	}

	maxKeyLen := 0
	for _, key := range sortedKeys {
		if len(key) > maxKeyLen {
			maxKeyLen = len(key)
		}
	}

	format := fmt.Sprintf("| %%-%vs | %%s |\n", maxKeyLen)
	prefix := configGet("prefix")
	suffix := configGet("suffix")

	fmt.Printf("%s", prefix)
	for _, key := range sortedKeys {
		entry := h.data[key]
		right := strings.Join(entry.Values, "; ")
		fmt.Printf(format, entry.OriginalKey, right)
	}
	fmt.Printf("%s", suffix)
}
