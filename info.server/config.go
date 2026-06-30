package main

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const configName = "config.json"

var config = map[string]string{}

func loadConfig() {
	dir, _ := os.Getwd()
	locations := []string{dir}

	// Get the path for the executable that started the current process
	if ex, err := os.Executable(); err == nil {
		exPath := filepath.Dir(ex)
		locations = append(locations, exPath)
	}

	var raw []byte
	var err error
	for _, location := range locations {
		fullPath := path.Join(location, configName)
		if runtime.GOOS == "windows" {
			fullPath = strings.Replace(fullPath, "/", "\\", -1)
		}

		if raw, err = os.ReadFile(fullPath); err == nil {
			break
		}
	}
	if raw == nil {
		panic("config file not found")
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		panic(err)
	}
}

func getConfig(key, fallback string) string {
	if value, found := config[key]; found {
		return value
	}
	return fallback
}
