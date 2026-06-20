Here's a standalone HTML → Confluence storage format converter, no markdown/goldmark involved at all. Single file, single package, builds clean.Layout's clean — one `go.mod`, package `htmlconf` at root, `package main` only in `cmd/htmlconf/`. No cycle possible since `cmd/htmlconf` is a subdirectory importing the root module by name, not the reverse.```
htmlconf/
├── go.mod              module htmlconf
├── htmlconf.go         package htmlconf — Convert(htmlString) (string, error)
└── cmd/htmlconf/
    └── main.go          package main — CLI wrapper
```

Build and run:
```bash
cd htmlconf
go mod tidy
go run ./cmd/htmlconf page.html
# or: cat page.html | go run ./cmd/htmlconf
```

Same conversion logic as before (code blocks → macro, img → ac:image, blockquote → info panel, everything else passthrough), just stripped down to pure HTML in, storage format out — no goldmark, no markdown step. No Go toolchain in this sandbox so again: build it locally before trusting it.
