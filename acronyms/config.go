package main

import (
	"os"
	"strings"
)

var (
	configFileName = "./config"
	config         = map[string][]string{}
)

func loadConfig() {
	raw, err := os.ReadFile(configFileName)
	if err != nil {
		return
	}
	// trace("opened file [%s]", configFileName)

	// reset config
	config = map[string][]string{}

	lines := strings.Split(string(raw), "\n")
	section := ""
	collector := []string{}

	flush := func() {
		if len(section) > 0 {
			config[section] = collector
		}
		// empty the collector
		collector = []string{}
	}

	for _, line := range lines {
		if head, newSection := isSectionHead(line); head {
			flush()
			section = newSection
			continue
		}

		if len(section) == 0 {
			continue
		} else {
			line = strings.TrimPrefix(line, "\t")
			line = strings.TrimSuffix(line, "\r")
			collector = append(collector, line)
		}
	}
	flush()
}

func isSectionHead(original string) (bool, string) {
	parts := strings.Split(trim(original), ":")
	if len(parts) > 1 {
		section := parts[0]
		if len(section) > 0 && len(section) < 32 && isAlphanumericOrDot(section) {
			return true, strings.ToLower(section)
		}
	}
	return false, ""
}

func configGet(key string) string {
	if value, found := config[strings.ToLower(trim(key))]; found && len(value) > 0 {
		if len(value) == 1 {
			return trim(value[0])
		}
		return strings.Join(value, "\n")
	}
	return ""
}
