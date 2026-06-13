package main

 import _ "embed"

// MermaidJS itself (~3MB minified) is downloaded from jsDelivr on first run 
// and cached at /tmp/mermaid.min.js. Subsequent runs use the cache.
// 
// Alternatively, embed it at build time for fully offline/reproducible builds:
//
// curl -o mermaid.min.js https://cdn.jsdelivr.net/npm/mermaid@11.4.1/dist/mermaid.min.js

//         mermaid.min.js
//go:embed embed/mermaid@11.15.0
var mermaidJSSource string

// pass mermaidJSSource directly to mermaidcdp.Config{JSSource: mermaidJSSource}
