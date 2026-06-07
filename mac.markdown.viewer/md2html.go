package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
)

const defaultTitle = ""

type Md2HtmlConfig struct {
	Page        *bool   `json:"page,omitempty"`
	Toc         *bool   `json:"toc,omitempty"`
	Xhtml       *bool   `json:"xhtml,omitempty"`
	Smartypants *bool   `json:"smartypants,omitempty"`
	Latexdashes *bool   `json:"latexdashes,omitempty"`
	Fractions   *bool   `json:"fractions,omitempty"`
	Attributes  *bool   `json:"attributes,omitempty"`
	HeadingIDs  *bool   `json:"headingIDs,omitempty"`
	Repeat      *int    `json:"repeat,omitempty"`
	Css         *string `json:"css,omitempty"`
}

const md2HtmlConfigFilename = ".md2html.json"

func loadMd2HtmlConfig() Md2HtmlConfig {
	var cfg Md2HtmlConfig
	home, err := os.UserHomeDir()
	if err != nil {
		return cfg
	}
	data, err := os.ReadFile(filepath.Join(home, md2HtmlConfigFilename))
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		// fmt.Fprintf(os.Stderr, "warning: could not parse ~/%s: %v\n", md2HtmlConfigFilename, err)
	}
	return cfg
}

func Md2Html(input []byte) ([]byte, error) {
	var page, toc, xhtml, smartypants, latexdashes, fractions, attributes, headingIDs bool
	var css string
	var repeat int

	page = false
	toc = false
	xhtml = true
	smartypants = true
	latexdashes = true
	fractions = true
	css = ""
	repeat = 1
	attributes = false
	headingIDs = false

	cfg := loadMd2HtmlConfig()
	if cfg.Page != nil {
		page = *cfg.Page
	}
	if cfg.Toc != nil {
		toc = *cfg.Toc
	}
	if cfg.Xhtml != nil {
		xhtml = *cfg.Xhtml
	}
	if cfg.Smartypants != nil {
		smartypants = *cfg.Smartypants
	}
	if cfg.Latexdashes != nil {
		latexdashes = *cfg.Latexdashes
	}
	if cfg.Fractions != nil {
		fractions = *cfg.Fractions
	}
	if cfg.Css != nil {
		css = *cfg.Css
	}
	if cfg.Repeat != nil {
		repeat = *cfg.Repeat
	}
	if cfg.Attributes != nil {
		attributes = *cfg.Attributes
	}
	if cfg.HeadingIDs != nil {
		headingIDs = *cfg.HeadingIDs
	}

	// enforce implied options
	if css != "" {
		page = true
	}

	// set up options
	var extensions = parser.NoIntraEmphasis |
		parser.Tables |
		parser.FencedCode |
		parser.Autolink |
		parser.Strikethrough |
		parser.SpaceHeadings

	if attributes {
		extensions |= parser.Attributes
	}

	if headingIDs {
		extensions |= parser.HeadingIDs
	}

	var renderer markdown.Renderer
	// render the data into HTML
	var htmlFlags html.Flags
	if xhtml {
		htmlFlags |= html.UseXHTML
	}
	if smartypants {
		htmlFlags |= html.Smartypants
	}
	if fractions {
		htmlFlags |= html.SmartypantsFractions
	}
	if latexdashes {
		htmlFlags |= html.SmartypantsLatexDashes
	}
	title := ""
	if page {
		htmlFlags |= html.CompletePage
		title = getTitle(input)
	}
	if toc {
		htmlFlags |= html.TOC
	}
	params := html.RendererOptions{
		Flags: htmlFlags,
		Title: title,
		CSS:   css,
	}
	renderer = html.NewRenderer(params)

	// parse and render
	var output []byte
	for i := 0; i < repeat; i++ {
		parser := parser.NewWithExtensions(extensions)
		output = markdown.ToHTML(input, parser, renderer)
	}

	return output, nil
}

// try to guess the title from the input buffer
// just check if it starts with an <h1> element and use that
func getTitle(input []byte) string {
	i := 0

	// skip blank lines
	for i < len(input) && (input[i] == '\n' || input[i] == '\r') {
		i++
	}
	if i >= len(input) {
		return defaultTitle
	}
	if input[i] == '\r' && i+1 < len(input) && input[i+1] == '\n' {
		i++
	}

	// find the first line
	start := i
	for i < len(input) && input[i] != '\n' && input[i] != '\r' {
		i++
	}
	line1 := input[start:i]
	if input[i] == '\r' && i+1 < len(input) && input[i+1] == '\n' {
		i++
	}
	i++

	// check for a prefix header
	if len(line1) >= 3 && line1[0] == '#' && (line1[1] == ' ' || line1[1] == '\t') {
		return strings.TrimSpace(string(line1[2:]))
	}

	// check for an underlined header
	if i >= len(input) || input[i] != '=' {
		return defaultTitle
	}
	for i < len(input) && input[i] == '=' {
		i++
	}
	for i < len(input) && (input[i] == ' ' || input[i] == '\t') {
		i++
	}
	if i >= len(input) || (input[i] != '\n' && input[i] != '\r') {
		return defaultTitle
	}

	return strings.TrimSpace(string(line1))
}

func Md2HtmlFiles(mdFileName, htmlFileName string) error {
	var input []byte
	var err error
	if input, err = os.ReadFile(mdFileName); err != nil {
		return fmt.Errorf("Error reading from", mdFileName, ":", err)
	}

	output, err := Md2Html(input)

	// output the result
	var out *os.File
	if out, err = os.Create(htmlFileName); err != nil {
		return fmt.Errorf("Error creating %s: %v", htmlFileName, err)
	}
	defer out.Close()

	if _, err = out.Write(output); err != nil {
		return fmt.Errorf("Error writing output:", err)
	}
	return nil
}
