package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
)

// FilterFunc decides whether a resolved link URL should be downloaded.
// Return true to download, false to skip.
type FilterFunc func(href string) bool

var (
	dryrun = false
)

// Downloader holds shared config.
type Downloader struct {
	client *http.Client
	log    *slog.Logger
	filter FilterFunc
	outDir string
}

func NewDownloader(filter FilterFunc, outDir string) *Downloader {
	return &Downloader{
		client: &http.Client{},
		log:    slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})),
		filter: filter,
		outDir: outDir,
	}
}

// Run is the main entry point: fetch the page, find links, filter, download.
func (d *Downloader) Run(rawURL string) error {
	base, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse base URL: %w", err)
	}

	d.log.Info("fetching page", "url", rawURL)
	body, err := d.fetch(rawURL)
	if err != nil {
		return fmt.Errorf("fetch page: %w", err)
	}
	defer body.Close()

	d.log.Info("parsing links")
	hrefs, err := extractLinks(body)
	if err != nil {
		return fmt.Errorf("parse HTML: %w", err)
	}
	d.log.Info("links found", "count", len(hrefs))

	for _, href := range hrefs {
		resolved, err := resolveURL(base, href)
		if err != nil {
			d.log.Warn("skipping unresolvable href", "href", href, "err", err)
			continue
		}

		if !d.filter(resolved) {
			d.log.Debug("filtered out", "url", resolved)
			continue
		}

		d.log.Info("downloading", "url", resolved)
		if err := d.download(resolved); err != nil {
			// Log but keep going — one failure shouldn't stop the rest.
			d.log.Error("download failed", "url", resolved, "err", err)
		}
	}

	return nil
}

// fetch performs a GET and returns the response body. Caller must close it.
func (d *Downloader) fetch(rawURL string) (io.ReadCloser, error) {
	resp, err := d.client.Get(rawURL)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// download fetches rawURL and writes its binary content to outDir,
// using the last path segment as the filename.
func (d *Downloader) download(rawURL string) error {
	if dryrun {
		fmt.Fprintf(os.Stdout, "[%s]\n", rawURL)
		return nil
	}

	body, err := d.fetch(rawURL)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer body.Close()

	filename := urlFilename(rawURL)
	dest := filepath.Join(d.outDir, filename)

	if err := os.MkdirAll(d.outDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create file %q: %w", dest, err)
	}
	defer f.Close()

	n, err := io.Copy(f, body)
	if err != nil {
		return fmt.Errorf("write %q: %w", dest, err)
	}

	d.log.Info("saved", "file", dest, "bytes", n)
	return nil
}

// extractLinks walks the HTML token stream and collects every href attribute
// found on an <a> element.
func extractLinks(r io.Reader) ([]string, error) {
	var hrefs []string

	tokenizer := html.NewTokenizer(r)
	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			if err := tokenizer.Err(); err != io.EOF {
				return hrefs, err
			}
			return hrefs, nil

		case html.StartTagToken, html.SelfClosingTagToken:
			tok := tokenizer.Token()
			if tok.Data != "a" {
				continue
			}
			for _, attr := range tok.Attr {
				if attr.Key == "href" && strings.TrimSpace(attr.Val) != "" {
					hrefs = append(hrefs, attr.Val)
				}
			}
		}
	}
}

// resolveURL resolves href relative to base, returning the absolute URL string.
func resolveURL(base *url.URL, href string) (string, error) {
	ref, err := url.Parse(href)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

// urlFilename derives a safe filename from the last non-empty path segment
// of rawURL, falling back to "index" when nothing useful is found.
func urlFilename(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "download"
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return "index"
}

// ---------------------------------------------------------------------------
// Example usage — swap out the filter and URL for your own needs.
// ---------------------------------------------------------------------------

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: downloader <url>")
		os.Exit(1)
	}
	targetURL := os.Args[1]

	for _, entry := range os.Args[2:] {
		if strings.HasPrefix(entry, "-dry") || strings.HasPrefix(entry, "/dry") {
			dryrun = true
		}
	}

	originalHost := ""
	if u, err := url.Parse(targetURL); err != nil {
		fmt.Fprintln(os.Stderr, "failed to parse (%s): %v", targetURL, err)
		os.Exit(1)
	} else {
		originalHost = u.Host
	}

	pattern := "*" // "go*.25.*windows*amd64*.msi"
	if len(os.Args) > 2 {
		pattern = os.Args[2]
		fmt.Fprintf(os.Stdout, "pattern used [%s]\n", pattern)
	}

	// Example filter: only download PDF files.
	filter := func(href string) bool {
		u, err := url.Parse(href)
		if err != nil {
			slog.Debug("failed to parse (%s): %v", href, err)
			return false
		}

		if u.Host != originalHost {
			slog.Debug("cross host reference (%s) instead of (%s)", u.Host, originalHost)
			return false
		}

		fullPath := u.Path              // e.g.: /resources/images/vacation.jpg
		fileName := path.Base(fullPath) // e.g.: vacation.jpg
		extension := path.Ext(fullPath) // e.g.: .jpg

		if isMatched(pattern, fileName) {
			return true
		}

		_, _, _ = fullPath, fileName, extension
		return false
	}

	d := NewDownloader(filter, "downloads")
	if err := d.Run(targetURL); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func isMatched(pattern, candidate string) bool {
	matched, _ := filepath.Match(pattern, candidate)
	return matched
}
