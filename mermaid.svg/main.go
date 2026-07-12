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
		inFile      = flag.String("in", "", "Input Markdown file (default: stdin)")
		outFile     = flag.String("out", "", "Output HTML file (default: stdout)")
		rawMermaid  = flag.Bool("mermaid", false, "Treat input as raw Mermaid source, output SVG directly")
		timeout     = flag.Duration("timeout", 30*time.Second, "Rendering timeout")
		themeName   = flag.String("theme", "default", "Theme: "+availableThemes())
		interactive = flag.Bool("interactive", false, "allow interactive theme switch")
	)
	flag.Parse()

	// Resolve theme.
	theme, err := ResolveTheme(*themeName)
	if err != nil {
		return fmt.Errorf("unknown theme %q — available: %s", *themeName, availableThemes())
	}

	// Read input
	var input []byte
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
	defer func() {
		if closer, found := compiler.(io.Closer); found && closer != nil {
			closer.Close()
		}
	}()

	var output []byte
	if *rawMermaid {
		output, err = renderMermaidToSVG(ctx, compiler, string(input), *interactive)
	} else {
		output, err = renderMarkdownToHTML(ctx, compiler, input, theme, *interactive)
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

func getConfig(jsSource string) *mermaidcdp.Config {
	return &mermaidcdp.Config{
		JSSource: jsSource,
		Theme:    "default", // "default", "dark", "forest", "neutral", "base"
	}
	/*
		return &mermaidcdp.Config{
			JSSource: jsSource,
			Theme:    "neutral", // "default", "dark", "forest", "neutral", "base"
		}
	*/
}

// renderMermaidToSVG renders a single raw Mermaid diagram to an SVG string.
// Use this when you only need the SVG bytes, not a full HTML page.
func renderMermaidToSVG(ctx context.Context, compiler mermaid.Compiler, src string, interactive bool) ([]byte, error) {
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
func renderMarkdownToHTML(ctx context.Context, compiler mermaid.Compiler, src []byte, styleBlock string, interactive bool) ([]byte, error) {
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
	page := htmlPage(title, body.String(), styleBlock, interactive)
	return []byte(page), nil
}

// htmlPage wraps rendered HTML body content in a minimal standalone HTML page.
func htmlPage(title, body, style string, interactive bool) string {

	link := ""
	if interactive {
		link = interactivePlug
	}

	style = `  <style>` + style + `</style>`

	return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>` + title + `</title>
` + link + `
` + style + `
</head>
<body>
` + body + `
</body>
</html>`
}
