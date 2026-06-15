package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ── Embedded Swift helper source ──────────────────────────────────────────────

// helperSource is compiled once by swiftc and cached in ~/.cache/mdview/.
// It reads HTML from stdin and displays it in a plain WKWebView window.
const helperSource = `
import Cocoa
import WebKit

class AppDelegate: NSObject, NSApplicationDelegate {
    var window: NSWindow!
    var webView: WKWebView!

    func applicationDidFinishLaunching(_ notification: Notification) {
        let args = CommandLine.arguments
        let title   = args.count > 1 ? args[1] : "mdview"
        let width   = args.count > 2 ? CGFloat(Double(args[2]) ?? 1024) : 1024
        let height  = args.count > 3 ? CGFloat(Double(args[3]) ?? 768)  : 768
        let baseURL = args.count > 4 ? URL(fileURLWithPath: args[4])    : nil

        let html = String(
            bytes: FileHandle.standardInput.readDataToEndOfFile(),
            encoding: .utf8
        ) ?? "<p>error: could not read stdin</p>"

        let rect = NSRect(x: 0, y: 0, width: width, height: height)

        let wvConfig = WKWebViewConfiguration()
        wvConfig.preferences.setValue(true, forKey: "developerExtrasEnabled")

        webView = WKWebView(frame: rect, configuration: wvConfig)
        webView.autoresizingMask = [.width, .height]

        window = NSWindow(
            contentRect: rect,
            styleMask:   [.titled, .closable, .resizable, .miniaturizable],
            backing:     .buffered,
            defer:       false
        )
        window.title = title
        window.contentView = webView
        window.isReleasedWhenClosed = false
        window.center()
        window.makeKeyAndOrderFront(nil)

        webView.loadHTMLString(html, baseURL: baseURL)
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        return true
    }
}

let delegate = AppDelegate()
NSApplication.shared.delegate = delegate
NSApplication.shared.setActivationPolicy(.regular)
NSApplication.shared.activate(ignoringOtherApps: true)
NSApplication.shared.run()
`

// ── Swift helper compilation / caching ───────────────────────────────────────

// helperBinary returns the path to a compiled mdview-helper binary,
// compiling it from helperSource if needed. The binary is keyed by a
// SHA-256 of the source so it is recompiled automatically when the
// embedded source changes.
func helperBinary() (string, error) {
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(helperSource)))[:16]
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("user cache dir: %w", err)
	}
	dir := filepath.Join(cacheDir, "mdview")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir cache: %w", err)
	}

	bin := filepath.Join(dir, "mdview-helper-"+sum)
	if _, err := os.Stat(bin); err == nil {
		return bin, nil // already compiled
	}

	log.Printf("compiling Swift helper -> %s", bin)
	fmt.Fprintln(os.Stderr, "mdview: compiling Swift helper (first run, takes ~5s)…")

	src := filepath.Join(dir, "mdview-helper.swift")
	if err := os.WriteFile(src, []byte(helperSource), 0644); err != nil {
		return "", fmt.Errorf("write swift src: %w", err)
	}

	cmd := exec.Command("swiftc", "-O", "-o", bin, src)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("swiftc failed: %w", err)
	}
	return bin, nil
}

// ── Config ────────────────────────────────────────────────────────────────────

const configFilename = ".htmlviewer.json"

type Config struct {
	Width   *int    `json:"width,omitempty"`
	Height  *int    `json:"height,omitempty"`
	CSSFile *string `json:"css_file,omitempty"`
	Logging *bool   `json:"logging,omitempty"`
}

func loadConfig() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}
	}
	data, err := os.ReadFile(filepath.Join(home, configFilename))
	if err != nil {
		return Config{}
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not parse ~/%s: %v\n", configFilename, err)
	}
	return cfg
}

// ── HTML helpers ──────────────────────────────────────────────────────────────

func extractTitle(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return filepath.Base(path)
	}
	re := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	m := re.FindSubmatch(data)
	if m == nil {
		return filepath.Base(path)
	}
	t := regexp.MustCompile(`<[^>]+>`).ReplaceAllString(string(m[1]), "")
	t = strings.Join(strings.Fields(t), " ")
	if t == "" {
		return filepath.Base(path)
	}
	return t
}

func buildCSSInit(cssPath string) string {
	data, err := os.ReadFile(cssPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot read css file %q: %v\n", cssPath, err)
		return ""
	}
	css := strings.ReplaceAll(string(data), `\`, `\\`)
	css = strings.ReplaceAll(css, "`", "\\`")
	return `<style data-injected="mdview">` + string(data) + `</style>`
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	cfg := loadConfig()

	if cfg.Logging != nil && *cfg.Logging {
		logFileName := fmt.Sprintf("application-%v.log", os.Getpid())
		f, err := os.OpenFile(logFileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal(err)
		}
		defer func() {
			log.Printf("done")
			f.Close()
		}()
		log.SetOutput(f)
		log.Printf("start: %v", os.Args[1:])
	} else {
		log.SetOutput(io.Discard)
	}

	defaultWidth := 1024
	defaultHeight := 768
	defaultCSSFile := ""

	if cfg.Width != nil {
		defaultWidth = *cfg.Width
	}
	if cfg.Height != nil {
		defaultHeight = *cfg.Height
	}
	if cfg.CSSFile != nil {
		defaultCSSFile = *cfg.CSSFile
	}

	width := flag.Int("width", defaultWidth, "initial window width")
	height := flag.Int("height", defaultHeight, "initial window height")
	cssFile := flag.String("css", defaultCSSFile, "CSS file to inject into every page")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mdview [options] <file.md|file.html>")
		flag.PrintDefaults()
		os.Exit(1)
	}

	abs, err := filepath.Abs(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad path:", err)
		os.Exit(1)
	}

	// Determine title
	title := filepath.Base(abs)
	ext := strings.ToLower(filepath.Ext(abs))
	if ext == ".html" || ext == ".htm" {
		title = extractTitle(abs)
	}

	// Load and convert content
	content, err := loadContent(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading %s: %v\n", abs, err)
		os.Exit(1)
	}

	// Inject CSS as a <style> block prepended to the HTML
	if *cssFile != "" {
		absCSS, err := filepath.Abs(*cssFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bad css path:", err)
			os.Exit(1)
		}
		styleTag := buildCSSInit(absCSS)
		if styleTag != "" {
			content = append([]byte(styleTag+"\n"), content...)
		}
	}

	// Compile (or load cached) Swift helper
	helper, err := helperBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		fmt.Fprintln(os.Stderr, "hint: install Xcode Command Line Tools with: xcode-select --install")
		os.Exit(1)
	}

	// Run helper: mdview-helper <title> <width> <height> <baseDir>
	// HTML arrives on stdin; baseURL lets WKWebView resolve relative paths.
	cmd := exec.Command(
		helper,
		title,
		fmt.Sprintf("%d", *width),
		fmt.Sprintf("%d", *height),
		filepath.Dir(abs),
	)
	cmd.Stdin = strings.NewReader(string(content))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "mdview-helper error:", err)
		os.Exit(1)
	}
}
