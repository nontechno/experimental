package main

import (
	"context"
	"fmt"
	"strings"

	"go.abhg.dev/goldmark/mermaid"
	"go.abhg.dev/goldmark/mermaid/mermaidcdp"
)

type themedCompiler struct {
	inner     *mermaidcdp.Compiler
	initJS    string
	isMermaid bool
}

func (c *themedCompiler) Compile(ctx context.Context, req *mermaid.CompileRequest) (*mermaid.CompileResponse, error) {
	src := req.Source
	if !strings.HasPrefix(strings.TrimSpace(src), "%%{init") {
		src = fmt.Sprintf("%%%%{init: %s}%%%%\n%s", c.initJS, src)
	}
	resp, err := c.inner.Compile(ctx, &mermaid.CompileRequest{Source: src})
	if err != nil {
		return nil, err
	}

	colorMap := map[string]string{

		/* "primaryColor":			*/ "#123456": "var(--mermaid-primaryColor)",
		/* "primaryBorderColor":	*/ "#234567": "var(--mermaid-primaryBorderColor)",
		/* "primaryTextColor":		*/ "#345678": "var(--mermaid-primaryTextColor)",
		/* "lineColor":				*/ "#456789": "var(--mermaid-lineColor)",
		/* "fontSize":				*/ "14px": "var(--mermaid-fontSize)",
		/* "fontFamily":			*/ "JetBrains Mono, monospace": "var(--mermaid-fontFamily)",
		/* "clusterBkg": 			*/ "#56789a": "var(--th-bg)",
		/* "clusterBorder":			*/ "#6789ab": "var(--border)",
		/* "edgeLabelBackground":	*/ "#789abc": "var(--bg)",
		/* "titleColor":			*/ "#89abcd": "var(--muted)",

		"#ffffff": "var(--mermaid-node-fill)",
		"#333333": "var(--mermaid-node-border)",
		"#111111": "var(--mermaid-text)",
		"#555555": "var(--mermaid-line)",
	}

	if c.isMermaid {
		fontName := "Arial, sans-serif"
		colorMap = map[string]string{

			/* "primaryColor":			*/ "#123456": "#f8f8f8",
			/* "primaryBorderColor":	*/ "#234567": "#cccccc",
			/* "primaryTextColor":		*/ "#345678": "#0066cc",
			/* "lineColor":				*/ "#456789": "#0066cc",
			/* "fontSize":				*/ "14px": "12px",
			"16px":                      "12px",
			/* "fontFamily":			*/ "JetBrains Mono, monospace": fontName,
			"JetBrains Mono,monospace":                        fontName,
			/* "clusterBkg": 			*/ "#56789a": "#f5f5f5",
			/* "clusterBorder":			*/ "#6789ab": "#cccccc",
			/* "edgeLabelBackground":	*/ "#789abc": "#ffffff",
			/* "titleColor":			*/ "#89abcd": "#555555",
		}
	}

	resp.SVG = rewriteSVGColors(resp.SVG, colorMap)
	return resp, nil
}

func (c *themedCompiler) Close() error {
	return c.inner.Close()
}

// newCompiler builds a mermaidcdp.Compiler backed by a headless Chromium
// browser. It loads MermaidJS from a CDN once and reuses the browser tab
// for all subsequent renders — much cheaper than spawning mmdc per diagram.
//
// The returned compiler MUST be closed when no longer needed.
func newCompiler(ctx context.Context, isMermaid bool) (mermaid.Compiler, error) {
	// Download mermaid.min.js source at startup.
	// In production, you'd embed this with //go:embed or cache it on disk.
	jsSource, err := downloadMermaidJS(ctx)
	if err != nil {
		return nil, fmt.Errorf("downloading mermaid.js: %w", err)
	}

	compiler, err := mermaidcdp.New(getConfig(jsSource))
	if err != nil {
		return nil, fmt.Errorf("starting CDP compiler: %w\n\nMake sure Chrome/Chromium is installed.", err)
	}

	compiler2 := themedCompiler{
		inner:     compiler,
		initJS:    mermaidInit,
		isMermaid: isMermaid,
	}
	return &compiler2, nil
}

/*

`{
        "theme": "base",
        "themeVariables": {
            "primaryColor":       "#ffffff",
            "primaryBorderColor": "#333333",
            "primaryTextColor":   "#111111",
            "lineColor":          "#555555",
            "fontSize":           "14px",
            "fontFamily":         "JetBrains Mono, monospace"
        }
}`
*/

// colorMap maps the hardcoded hex Mermaid emits → CSS variable reference
func rewriteSVGColors(svg string, colorMap map[string]string) string {
	for hex, cssVar := range colorMap {
		svg = strings.ReplaceAll(svg, hex, cssVar)
	}
	return svg
}
