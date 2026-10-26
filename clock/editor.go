package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func editConfig(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := ensureConfig(path); err != nil {
		return err
	}
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		editor = "vi"
	}
	// Editor variables may contain flags and shell quoting. Pass the filename
	// separately so spaces and shell characters in paths remain literal.
	cmd := exec.Command("sh", "-c", "exec "+editor+" \"$1\"", "clock-editor", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %q: %w", editor, err)
	}
	return nil
}
