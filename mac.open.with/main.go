package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Config maps type aliases to application paths.
// The JSON structure supports both a single app path (string)
// and a list of arguments to pass before the filename ([]string).
//
// Simple form:
//
//	{ "go": "/Applications/GoLand.app" }
//
// Advanced form (extra args inserted before the filename):
//
//	{ "go": { "app": "/Applications/GoLand.app", "args": ["--wait"] } }
type appEntry struct {
	App  string   // path to .app bundle or binary
	Args []string // optional extra args prepended before filename
}

func (e *appEntry) UnmarshalJSON(data []byte) error {
	// Try plain string first.
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		e.App = s
		return nil
	}
	// Try object form.
	var obj struct {
		App  string   `json:"app"`
		Args []string `json:"args"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	e.App = obj.App
	e.Args = obj.Args
	return nil
}

type config map[string]appEntry

func configPath() string {
	// 1. $OPENWITH_CONFIG env var
	if p := os.Getenv("OPENWITH_CONFIG"); p != "" {
		return p
	}
	// 2. ~/.config/openwith/apps.json
	home, err := os.UserHomeDir()
	if err != nil {
		return "apps.json"
	}
	return filepath.Join(home, ".config", "openwith", "apps.json")
}

func loadConfig(path string) (config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open config %q: %w", path, err)
	}
	defer f.Close()

	var cfg config
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("malformed config %q: %w", path, err)
	}
	return cfg, nil
}

func openFile(entry appEntry, filename string) error {
	// Resolve absolute path so the app receives a clean path.
	abs, err := filepath.Abs(filename)
	if err != nil {
		return fmt.Errorf("cannot resolve path %q: %w", filename, err)
	}
	if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("file not found: %s", abs)
	}

	var cmd *exec.Cmd

	// If the app path ends in .app it's a macOS bundle → use `open -a`.
	if filepath.Ext(entry.App) == ".app" {
		// open -a /Applications/GoLand.app [--args <extra>...] <file>
		args := []string{"-a", entry.App}
		if len(entry.Args) > 0 {
			args = append(args, "--args")
			args = append(args, entry.Args...)
		}
		args = append(args, abs)
		cmd = exec.Command("open", args...)
	} else {
		// Plain binary: binary [extra args...] <file>
		args := append(append([]string{}, entry.Args...), abs)
		cmd = exec.Command(entry.App, args...)
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: %s <type> <filename>", filepath.Base(os.Args[0]))
	}
	typeName := os.Args[1]
	filename := os.Args[2]

	cfg, err := loadConfig(configPath())
	if err != nil {
		return err
	}

	entry, ok := cfg[typeName]
	if !ok {
		// Collect known types for a helpful error.
		known := make([]string, 0, len(cfg))
		for k := range cfg {
			known = append(known, k)
		}
		return fmt.Errorf("unknown type %q (known: %v)", typeName, known)
	}

	return openFile(entry, filename)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
