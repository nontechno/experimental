package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/golang/freetype"
	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font"
)

// Config mirrors the JSON input format.
type Config struct {
	Tile      string  `json:"tile"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	Message   string  `json:"message"`
	FontName  string  `json:"font.name"`
	FontSize  float64 `json:"font.size"`
	FontColor string  `json:"font.color"`
	Margin    int     `json:"margin"`
	Placement string  `json:"placement"`
	Output    string  `json:"output"`
}

func main() {
	configPath := flag.String("config", "config.json", "path to JSON config file")
	flag.Parse()

	data, err := os.ReadFile(expandHome(*configPath))
	if err != nil {
		fatalf("reading config: %v", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		fatalf("parsing config: %v", err)
	}

	// 1. Load tile image.
	tileImg, err := loadImage(expandHome(cfg.Tile))
	if err != nil {
		fatalf("loading tile: %v", err)
	}

	// 2. Create canvas and tile it.
	canvas := image.NewRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
	tileBounds := tileImg.Bounds()
	tw, th := tileBounds.Max.X-tileBounds.Min.X, tileBounds.Max.Y-tileBounds.Min.Y
	if tw == 0 || th == 0 {
		fatalf("tile image has zero dimension")
	}
	for y := 0; y < cfg.Height; y += th {
		for x := 0; x < cfg.Width; x += tw {
			draw.Draw(canvas, image.Rect(x, y, x+tw, y+th), tileImg, tileBounds.Min, draw.Src)
		}
	}

	// 3. Draw text overlay.
	fontData, err := findAndLoadFont(cfg.FontName)
	if err != nil {
		fatalf("loading font %q: %v", cfg.FontName, err)
	}
	ttFont, err := truetype.Parse(fontData)
	if err != nil {
		fatalf("parsing font: %v", err)
	}

	textColor, err := parseHexColor(cfg.FontColor)
	if err != nil {
		fatalf("parsing font.color: %v", err)
	}

	// Measure text to find bounding box.
	dpi := 72.0
	face := truetype.NewFace(ttFont, &truetype.Options{
		Size:    cfg.FontSize,
		DPI:     dpi,
		Hinting: font.HintingFull,
	})
	defer face.Close()

	lines := strings.Split(cfg.Message, "\n")
	metrics := face.Metrics()
	lineHeight := int((metrics.Ascent + metrics.Descent).Ceil())
	textWidth := 0
	for _, line := range lines {
		w := measureTextWidth(face, line)
		if w > textWidth {
			textWidth = w
		}
	}
	textHeight := lineHeight * len(lines)

	// Determine anchor position from placement string.
	px, py := calcPosition(cfg.Placement, cfg.Width, cfg.Height, textWidth, textHeight, cfg.Margin, lineHeight, metrics)

	// Render each line.
	c := freetype.NewContext()
	c.SetDPI(dpi)
	c.SetFont(ttFont)
	c.SetFontSize(cfg.FontSize)
	c.SetClip(canvas.Bounds())
	c.SetDst(canvas)
	c.SetSrc(image.NewUniform(textColor))
	c.SetHinting(font.HintingFull)

	for i, line := range lines {
		pt := freetype.Pt(px, py+i*lineHeight)
		if _, err := c.DrawString(line, pt); err != nil {
			fatalf("drawing text: %v", err)
		}
	}

	// 4. Save output.
	if err := saveImage(expandHome(cfg.Output), canvas); err != nil {
		fatalf("saving output: %v", err)
	}

	fmt.Printf("Saved wallpaper to %s (%dx%d)\n", cfg.Output, cfg.Width, cfg.Height)
}

// calcPosition returns the top-left X and the baseline Y for the first line,
// honouring placement ("top/center/bottom", "left/center/right") and margin.
func calcPosition(placement string, imgW, imgH, textW, textH, margin, lineHeight int, metrics font.Metrics) (int, int) {
	parts := strings.Split(placement, ",")
	var vert, horiz string
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		switch p {
		case "top", "bottom", "center", "middle":
			vert = p
		case "left", "right":
			horiz = p
		}
	}
	if vert == "" {
		vert = "bottom"
	}
	if horiz == "" {
		horiz = "right"
	}

	// Horizontal
	var x int
	switch horiz {
	case "left":
		x = margin
	case "right":
		x = imgW - textW - margin
	default: // center
		x = (imgW - textW) / 2
	}

	// Vertical — Y is the baseline of the first line.
	ascent := metrics.Ascent.Ceil()
	var y int
	switch vert {
	case "top":
		y = margin + ascent
	case "bottom":
		y = imgH - textH - margin + ascent
	default: // center/middle
		y = (imgH-textH)/2 + ascent
	}

	return x, y
}

// measureTextWidth returns the advance width of a string in pixels.
func measureTextWidth(face font.Face, s string) int {
	var advance int
	for _, r := range s {
		a, ok := face.GlyphAdvance(r)
		if !ok {
			continue
		}
		advance += a.Ceil()
	}
	return advance
}

// parseHexColor parses "#RRGGBB" or "#RRGGBBAA".
func parseHexColor(s string) (color.RGBA, error) {
	s = strings.TrimPrefix(s, "#")
	if len(s) == 6 {
		s += "FF"
	}
	if len(s) != 8 {
		return color.RGBA{}, fmt.Errorf("invalid color %q", "#"+s)
	}
	vals := make([]uint8, 4)
	for i := 0; i < 4; i++ {
		v, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if err != nil {
			return color.RGBA{}, fmt.Errorf("invalid color component: %v", err)
		}
		vals[i] = uint8(v)
	}
	return color.RGBA{R: vals[0], G: vals[1], B: vals[2], A: vals[3]}, nil
}

// loadImage loads a PNG or JPEG image from disk.
func loadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	return img, err
}

// saveImage writes a PNG or JPEG depending on the output file extension.
func saveImage(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return jpeg.Encode(f, img, &jpeg.Options{Quality: 95})
	default: // .png and anything else
		return png.Encode(f, img)
	}
}

// expandHome replaces a leading "~/" with the user's home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}

// findAndLoadFont searches common system font directories for a TTF/OTF whose
// filename matches name. It collects all candidates (exact matches first, then
// prefix matches) and tries truetype.Parse on each in order, skipping files
// with corrupt tables (e.g. bad kern table length). Returns the raw bytes of
// the first file that parses successfully.
func findAndLoadFont(name string) ([]byte, error) {
	searchDirs := []string{
		"/usr/share/fonts",
		"/usr/local/share/fonts",
		"/Library/Fonts",
		"/System/Library/Fonts",
	}
	if home, err := os.UserHomeDir(); err == nil {
		searchDirs = append(searchDirs,
			filepath.Join(home, ".fonts"),
			filepath.Join(home, "Library", "Fonts"),
			filepath.Join(home, "AppData", "Local", "Microsoft", "Windows", "Fonts"),
		)
	}
	searchDirs = append(searchDirs, `C:\Windows\Fonts`)

	nameLower := strings.ToLower(name)

	// Collect candidates: priority 0 = exact name match, 1 = prefix match.
	type candidate struct {
		path     string
		priority int
	}
	var candidates []candidate

	_ = walkFontDirs(searchDirs, func(path string) bool {
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		if base == nameLower {
			candidates = append(candidates, candidate{path, 0})
		} else if strings.HasPrefix(base, nameLower) {
			candidates = append(candidates, candidate{path, 1})
		}
		return false // collect all; don't stop early
	})

	if len(candidates) == 0 {
		return nil, fmt.Errorf("font %q not found in system font directories", name)
	}

	// Stable sort: exact matches before prefix matches.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].priority < candidates[j].priority
	})

	// Try each candidate until one parses cleanly.
	var lastErr error
	for _, c := range candidates {
		data, err := os.ReadFile(c.path)
		if err != nil {
			lastErr = err
			continue
		}
		if _, err := truetype.Parse(data); err != nil {
			// File found but unparseable (e.g. bad kern table) — try next.
			fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", c.path, err)
			lastErr = err
			continue
		}
		return data, nil
	}
	return nil, fmt.Errorf("no parseable font file found for %q: last error: %v", name, lastErr)
}

// walkFontDirs walks directories looking for .ttf/.otf files, calling fn for each.
// fn returns true to stop early.
func walkFontDirs(dirs []string, fn func(path string) bool) error {
	for _, dir := range dirs {
		stop, err := walkDir(dir, fn)
		if err != nil {
			continue // skip missing dirs silently
		}
		if stop {
			return nil
		}
	}
	return nil
}

func walkDir(dir string, fn func(string) bool) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			stop, _ := walkDir(full, fn)
			if stop {
				return true, nil
			}
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".ttf" || ext == ".otf" {
			if fn(full) {
				return true, nil
			}
		}
	}
	return false, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
