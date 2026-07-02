// Command mermaid-render converts Markdown containing Mermaid diagrams to HTML
// with server-side SVG rendering via a headless Chromium browser (CDP).
//
// Usage:
//
//	go run . -in input.md -out output.html
//	go run . -in input.md           # writes to stdout
//	echo "graph TD; A-->B" | go run . -mermaid  # render raw mermaid from stdin
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"go.abhg.dev/goldmark/mermaid"
	"go.abhg.dev/goldmark/mermaid/mermaidcdp"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("error: %v", err)
	}
}

func run() error {
	var (
		inFile     = flag.String("in", "", "Input Markdown file (default: stdin)")
		outFile    = flag.String("out", "", "Output HTML file (default: stdout)")
		rawMermaid = flag.Bool("mermaid", false, "Treat input as raw Mermaid source, output SVG directly")
		timeout    = flag.Duration("timeout", 30*time.Second, "Rendering timeout")
	)
	flag.Parse()

	// Read input
	var input []byte
	var err error
	if *inFile != "" {
		input, err = os.ReadFile(*inFile)
		if err != nil {
			return fmt.Errorf("reading input file: %w", err)
		}
	} else {
		input, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
	}

	// Set up context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Build the CDP compiler (reusable across many renders)
	compiler, err := newCompiler(ctx)
	if err != nil {
		return err
	}
	defer compiler.Close()

	var output []byte
	if *rawMermaid {
		output, err = renderMermaidToSVG(ctx, compiler, string(input))
	} else {
		output, err = renderMarkdownToHTML(ctx, compiler, input)
	}
	if err != nil {
		return err
	}

	// Write output
	if *outFile != "" {
		if err := os.WriteFile(*outFile, output, 0o644); err != nil {
			return fmt.Errorf("writing output file: %w", err)
		}
		log.Printf("written to %s", *outFile)
	} else {
		_, err = os.Stdout.Write(output)
	}
	return err
}

// newCompiler builds a mermaidcdp.Compiler backed by a headless Chromium
// browser. It loads MermaidJS from a CDN once and reuses the browser tab
// for all subsequent renders — much cheaper than spawning mmdc per diagram.
//
// The returned compiler MUST be closed when no longer needed.
func newCompiler(ctx context.Context) (*mermaidcdp.Compiler, error) {
	// Download mermaid.min.js source at startup.
	// In production you'd embed this with //go:embed or cache it on disk.
	jsSource, err := downloadMermaidJS(ctx)
	if err != nil {
		return nil, fmt.Errorf("downloading mermaid.js: %w", err)
	}

	compiler, err := mermaidcdp.New(&mermaidcdp.Config{
		JSSource: jsSource,
	})
	if err != nil {
		return nil, fmt.Errorf("starting CDP compiler: %w\n\nMake sure Chrome/Chromium is installed.", err)
	}
	return compiler, nil
}

// renderMermaidToSVG renders a single raw Mermaid diagram to an SVG string.
// Use this when you only need the SVG bytes, not a full HTML page.
func renderMermaidToSVG(ctx context.Context, compiler *mermaidcdp.Compiler, src string) ([]byte, error) {
	resp, err := compiler.Compile(ctx, &mermaid.CompileRequest{
		Source: src,
	})
	if err != nil {
		return nil, fmt.Errorf("compiling mermaid diagram: %w", err)
	}
	return []byte(resp.SVG), nil
}

// renderMarkdownToHTML converts full Markdown (with optional Mermaid fenced
// blocks) to a standalone HTML page. Each ```mermaid block is replaced with
// an inline <svg> element — no JavaScript required in the output.
func renderMarkdownToHTML(ctx context.Context, compiler *mermaidcdp.Compiler, src []byte) ([]byte, error) {
	md := goldmark.New(
		goldmark.WithExtensions(
			// GFM tables, strikethrough, autolinks, task lists
			extension.GFM,
			// Server-side mermaid: emits <svg> directly into the HTML
			&mermaid.Extender{
				RenderMode: mermaid.RenderModeServer,
				Compiler:   compiler,
			},
		),
	)

	var body bytes.Buffer
	if err := md.Convert(src, &body); err != nil {
		return nil, fmt.Errorf("converting markdown: %w", err)
	}

	// Wrap in a minimal but complete HTML page
	title := ExtractTitle(string(src))
	page := htmlPage(title, body.String())
	return []byte(page), nil
}

// htmlPage wraps rendered HTML body content in a minimal standalone HTML page.
func htmlPage(title, body string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>` + title + `</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 860px; margin: 2rem auto; padding: 0 1rem; line-height: 1.6; }
    table { border-collapse: collapse; width: 100%; margin: 1rem 0; }
    th, td { border: 1px solid #ccc; padding: .4rem .8rem; text-align: left; }
    th { background: #f5f5f5; }
    pre { background: #f8f8f8; padding: 1rem; overflow-x: auto; border-radius: 4px; }
    code { font-family: "JetBrains Mono", "Fira Code", monospace; font-size: .9em; }
    svg { max-width: 100%; height: auto; display: block; margin: 1rem 0; }
  </style>
</head>
<body>
` + body + `
</body>
</html>`
}
