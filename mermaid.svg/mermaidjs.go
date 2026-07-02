package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// Pin the version you want. Check https://cdn.jsdelivr.net/npm/mermaid/
	mermaidVersion = "11.15.0" // "11.4.1"
	mermaidCDN     = "https://cdn.jsdelivr.net/npm/mermaid@" + mermaidVersion + "/dist/mermaid.min.js"

	// Local cache path so we only download once per machine
	cacheFile = "/tmp/mermaid.min.js"
)

// downloadMermaidJS returns the MermaidJS source, using a local cache at
// /tmp/mermaid.min.js to avoid re-downloading on every run.
//
// Alternative: embed the file at build time with:
//
//	//go:embed mermaid.min.js
//	var mermaidJSSource string
//
// Then pass mermaidJSSource directly to mermaidcdp.Config{JSSource: ...}.
func downloadMermaidJS(ctx context.Context) (string, error) {
	// Return cached copy if available
	if data, err := os.ReadFile(cacheFile); err == nil {
		return string(data), nil
	}

	// Make sure the cache directory exists
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o755); err != nil {
		return "", fmt.Errorf("creating cache dir: %w", err)
	}

	fmt.Fprintf(os.Stderr, "downloading mermaid.js v%s from CDN (cached to %s)...\n",
		mermaidVersion, cacheFile)

	httpCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, mermaidCDN, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", mermaidCDN, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP %d from CDN", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	// Write to cache
	if err := os.WriteFile(cacheFile, data, 0o644); err != nil {
		// Non-fatal: just don't cache
		fmt.Fprintf(os.Stderr, "warning: could not cache mermaid.js: %v\n", err)
	}

	return string(data), nil
}
