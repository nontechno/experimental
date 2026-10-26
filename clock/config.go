package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type config struct {
	OmitLeadingZero bool   `json:"omit_leading_zero"`
	HourFormat      int    `json:"hour_format"`
	ShowSeconds     bool   `json:"show_seconds"`
	ShowAMPM        bool   `json:"show_am_pm"`
	TimeZone        string `json:"time_zone"`
	ShowDate        bool   `json:"show_date"`
	DateFormat      string `json:"date_format"`
	ShowTimeZone    bool   `json:"show_time_zone"`
	ShowHelp        bool   `json:"show_help"`
	Scale           int    `json:"scale"`
	Alignment       string `json:"alignment"`
	DigitColor      string `json:"digit_color"`
	DateColor       string `json:"date_color"`
	HelpColor       string `json:"help_color"`
	Background      string `json:"background"`
}

func defaultConfig() config {
	return config{
		HourFormat: 24, ShowSeconds: true, ShowAMPM: true, TimeZone: "Local",
		ShowDate: true, DateFormat: "Monday, 02 January 2006",
		ShowTimeZone: true, ShowHelp: true, Alignment: "center",
		DigitColor: "orange", DateColor: "orange", HelpColor: "orange", Background: "black",
	}
}

// ensureConfig installs defaults without parsing or replacing an existing file.
func ensureConfig(path string) (created bool, err error) {
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("config path is not a regular file: %s", path)
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	data, err := json.MarshalIndent(defaultConfig(), "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, writeErr := file.Write(append(data, '\n'))
	return true, errors.Join(writeErr, file.Close())
}

// loadConfig creates the default file on first use. Partial files inherit defaults.
func loadConfig(path string) (config, error) {
	if _, err := ensureConfig(path); err != nil {
		return config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return config{}, err
	}
	return decodeConfig(data)
}

func decodeConfig(data []byte) (config, error) {
	cfg := defaultConfig()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return cfg, fmt.Errorf("expected a single JSON object")
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return cfg, fmt.Errorf("expected a JSON object, got null")
	}
	return cfg, nil
}

func (cfg config) options() (options, error) {
	if cfg.HourFormat != 12 && cfg.HourFormat != 24 {
		return options{}, fmt.Errorf("hour_format must be 12 or 24")
	}
	if cfg.Scale < 0 || cfg.Scale > 4 {
		return options{}, fmt.Errorf("scale must be 0 (automatic) or 1 through 4")
	}
	if _, _, ok := parseAlignment(cfg.Alignment); !ok {
		return options{}, fmt.Errorf("alignment must be left, center, right, top, middle, bottom, or <top|middle|bottom>-<left|center|right>")
	}
	for _, r := range cfg.DateFormat {
		if r < 32 || r == 127 {
			return options{}, fmt.Errorf("date_format must not contain control characters")
		}
	}
	location := time.Local
	if cfg.TimeZone != "" && cfg.TimeZone != "Local" {
		var err error
		location, err = time.LoadLocation(cfg.TimeZone)
		if err != nil {
			return options{}, err
		}
	}
	opts := options{
		omitLeadingZero: cfg.OmitLeadingZero,
		showAMPM:        cfg.ShowAMPM,
		twelve:          cfg.HourFormat == 12, seconds: cfg.ShowSeconds, location: location,
		showDate: cfg.ShowDate, dateFormat: cfg.DateFormat, showZone: cfg.ShowTimeZone,
		showHelp: cfg.ShowHelp, scale: cfg.Scale, alignment: cfg.Alignment,
	}
	for _, field := range []struct {
		name, value string
		target      *string
		background  bool
	}{
		{"digit_color", cfg.DigitColor, &opts.digitColor, false},
		{"date_color", cfg.DateColor, &opts.dateColor, false},
		{"help_color", cfg.HelpColor, &opts.helpColor, false},
		{"background", cfg.Background, &opts.background, true},
	} {
		code, ok := colorCode(field.value, field.background)
		if !ok {
			return options{}, fmt.Errorf("%s: unknown color %q", field.name, field.value)
		}
		*field.target = code
	}
	return opts, nil
}

func colorCode(name string, background bool) (string, bool) {
	if name == "orange" {
		if background {
			return "\x1b[48;2;255;165;0m", true
		}
		return "\x1b[38;2;255;165;0m", true
	}
	if name == "default" {
		return "", true
	}
	names := []string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"}
	bright := strings.HasPrefix(name, "bright_")
	name = strings.TrimPrefix(name, "bright_")
	for i, candidate := range names {
		if name == candidate {
			code := 30 + i
			if bright {
				code += 60
			}
			if background {
				code += 10
			}
			return fmt.Sprintf("\x1b[%dm", code), true
		}
	}
	return "", false
}
