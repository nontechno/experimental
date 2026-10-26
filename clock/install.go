package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxConfigBytes = 1 << 20

func installConfig(path, root string, warnings io.Writer) error {
	cfg := defaultConfig()
	if root = strings.TrimSpace(root); root != "" {
		downloaded, err := downloadConfig(root)
		if err != nil {
			fmt.Fprintf(warnings, "clock: config download failed: %v; using built-in defaults\n", err)
		} else {
			cfg = downloaded
		}
	}
	return replaceConfig(path, cfg)
}

func downloadConfig(root string) (config, error) {
	base, err := url.Parse(root)
	if err != nil {
		return config{}, err
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return config{}, fmt.Errorf("CONFIGURATION_ROOT must be an HTTP or HTTPS URL")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(base.JoinPath("clock.config.json").String())
	if err != nil {
		return config{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return config{}, fmt.Errorf("server returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxConfigBytes+1))
	if err != nil {
		return config{}, err
	}
	if len(data) > maxConfigBytes {
		return config{}, fmt.Errorf("download exceeds %d bytes", maxConfigBytes)
	}
	cfg, err := decodeConfig(data)
	if err != nil {
		return config{}, fmt.Errorf("invalid downloaded config: %w", err)
	}
	if _, err := cfg.options(); err != nil {
		return config{}, fmt.Errorf("invalid downloaded config: %w", err)
	}
	return cfg, nil
}

// Write in the destination directory, then rename so readers never see a
// partially written config and a failed write leaves the old file intact.
func replaceConfig(path string, cfg config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".clock-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(0644); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
