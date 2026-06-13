# mermaid-render

Converts Markdown (with Mermaid diagrams and GFM tables) to standalone HTML
with **server-side SVG rendering** — no JavaScript in the output.

Uses `mermaidcdp`: a headless Chromium browser driven via Chrome DevTools
Protocol. The browser starts once and is reused for all diagrams in a run,
making it much cheaper than shelling out to `mmdc` per diagram.

## Prerequisites

1. **Go 1.22+**
2. **Chrome or Chromium** installed and on `$PATH` (or at a standard location)
   ```
   # Debian/Ubuntu
   sudo apt install chromium-browser

   # macOS
   brew install --cask google-chrome
   ```
3. Dependencies (fetched automatically by `go mod tidy`):
   - `github.com/yuin/goldmark`
   - `go.abhg.dev/goldmark/mermaid` (includes `mermaidcdp`)
   - `github.com/chromedp/chromedp` (pulled in transitively)

## Setup

```bash
git clone <repo>
cd mermaid-render
go mod tidy
```

MermaidJS itself (~3MB minified) is downloaded from jsDelivr on first run and
cached at `/tmp/mermaid.min.js`. Subsequent runs use the cache.

**Alternatively**, embed it at build time for fully offline/reproducible builds:

```bash
# Download once
curl -o mermaid.min.js https://cdn.jsdelivr.net/npm/mermaid@11.4.1/dist/mermaid.min.js
```

Then in `main.go`, replace `downloadMermaidJS` with:

```go
//go:embed mermaid.min.js
var mermaidJSSource string
```

And pass `mermaidJSSource` directly to `mermaidcdp.Config{JSSource: mermaidJSSource}`.

## Usage

```bash
# Markdown file → HTML file
go run . -in example.md -out output.html

# Markdown file → stdout
go run . -in example.md

# stdin → stdout
cat example.md | go run .

# Raw Mermaid diagram → SVG (useful for embedding in other tools)
echo 'graph TD; A-->B; B-->C' | go run . -mermaid

# Custom timeout (default: 30s)
go run . -in example.md -out output.html -timeout 60s
```

## How it works

```
Markdown input
     │
     ▼
goldmark parser
     │  (Mermaid fenced blocks detected by goldmark-mermaid Transformer)
     ▼
mermaid.ServerRenderer
     │  calls compiler.Compile() for each diagram
     ▼
mermaidcdp.Compiler
     │  sends diagram source to headless Chrome via CDP
     │  Chrome executes mermaid.render() in JS
     ▼
<svg>...</svg>  ←─ inlined directly into the HTML output
     │
     ▼
HTML page (self-contained, no JS required)
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-in` | stdin | Input Markdown file |
| `-out` | stdout | Output HTML file |
| `-mermaid` | false | Treat input as raw Mermaid; output SVG |
| `-timeout` | 30s | Per-run rendering timeout |

## Architecture notes

- **One browser, many diagrams**: `mermaidcdp.Compiler` launches a single
  headless Chrome instance and reuses it. Spinning it up takes ~1–2s, but
  each subsequent diagram render takes ~50–200ms. This is far better than
  `mmdc` which starts a new Node.js process per diagram.

- **Compiler is goroutine-safe**: safe to call `compiler.Compile()` from
  multiple goroutines concurrently (e.g., in an HTTP server).

- **No Node.js required**: unlike `mmdc`, this only needs Chrome/Chromium.

## Embedding in an HTTP server

```go
func NewHandler() (http.Handler, error) {
    jsSource, _ := downloadMermaidJS(context.Background())
    compiler, err := mermaidcdp.New(&mermaidcdp.Config{JSSource: jsSource})
    if err != nil {
        return nil, err
    }

    md := goldmark.New(
        goldmark.WithExtensions(
            extension.GFM,
            &mermaid.Extender{
                RenderMode: mermaid.RenderModeServer,
                Compiler:   compiler,
            },
        ),
    )

    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        var buf bytes.Buffer
        md.Convert(body, &buf)
        w.Header().Set("Content-Type", "text/html")
        w.Write([]byte(htmlPage(buf.String())))
    }), nil
}
```
