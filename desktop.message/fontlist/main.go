// fontlist — scan system font directories and print available fonts.
//
// Usage:
//
//	fontlist [flags]
//
// Flags:
//
//	-search <term>   filter by family/full-name (case-insensitive substring)
//	-dir    <path>   scan a specific directory instead of system defaults
//	-details         print PostScript name + subfamily for each font
//	-json            output as JSON
//	-count           print only the total count
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/golang/freetype/truetype"
)

// FontInfo holds extracted metadata for one font file.
type FontInfo struct {
	File       string `json:"file"`
	Family     string `json:"family"`
	Subfamily  string `json:"subfamily"`
	FullName   string `json:"full_name"`
	PostScript string `json:"postscript_name,omitempty"`
}

func main() {
	search := flag.String("search", "", "filter fonts by name (case-insensitive)")
	dir := flag.String("dir", "", "scan a specific directory (overrides system defaults)")
	details := flag.Bool("details", false, "show PostScript name and subfamily")
	asJSON := flag.Bool("json", false, "output as JSON array")
	countOnly := flag.Bool("count", false, "print only the number of fonts found")
	flag.Parse()

	dirs := systemFontDirs()
	if *dir != "" {
		dirs = []string{*dir}
	}

	fonts := scanDirs(dirs)

	if *search != "" {
		needle := strings.ToLower(*search)
		var filtered []FontInfo
		for _, f := range fonts {
			if strings.Contains(strings.ToLower(f.Family), needle) ||
				strings.Contains(strings.ToLower(f.FullName), needle) ||
				strings.Contains(strings.ToLower(f.PostScript), needle) {
				filtered = append(filtered, f)
			}
		}
		fonts = filtered
	}

	// Sort: family name, then subfamily.
	sort.Slice(fonts, func(i, j int) bool {
		if fonts[i].Family != fonts[j].Family {
			return fonts[i].Family < fonts[j].Family
		}
		return fonts[i].Subfamily < fonts[j].Subfamily
	})

	switch {
	case *countOnly:
		fmt.Println(len(fonts))

	case *asJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(fonts)

	default:
		printTable(fonts, *details)
	}
}

// printTable renders a clean columnar list grouped by family.
func printTable(fonts []FontInfo, details bool) {
	if len(fonts) == 0 {
		fmt.Fprintln(os.Stderr, "no fonts found")
		return
	}

	// Group by family for a tidy display.
	type group struct {
		family  string
		members []FontInfo
	}
	var groups []group
	idx := map[string]int{}
	for _, f := range fonts {
		fam := f.Family
		if fam == "" {
			fam = "(unknown)"
		}
		if i, ok := idx[fam]; ok {
			groups[i].members = append(groups[i].members, f)
		} else {
			idx[fam] = len(groups)
			groups = append(groups, group{family: fam, members: []FontInfo{f}})
		}
	}

	// Measure column widths.
	maxFamily := 0
	maxFull := 0
	maxPS := 0
	for _, g := range groups {
		if len(g.family) > maxFamily {
			maxFamily = len(g.family)
		}
		for _, m := range g.members {
			if len(m.FullName) > maxFull {
				maxFull = len(m.FullName)
			}
			if len(m.PostScript) > maxPS {
				maxPS = len(m.PostScript)
			}
		}
	}
	cap := func(n, max int) int {
		if n > max {
			return max
		}
		return n
	}
	maxFamily = cap(maxFamily, 34)
	maxFull = cap(maxFull, 40)
	maxPS = cap(maxPS, 36)

	// Header.
	sep := strings.Repeat("─", 80)
	if details {
		sep = strings.Repeat("─", 100)
	}
	fmt.Println(sep)
	if details {
		fmt.Printf("%-*s  %-*s  %-*s  %s\n", maxFamily, "FAMILY", 14, "SUBFAMILY", maxPS, "POSTSCRIPT NAME", "FILE")
	} else {
		fmt.Printf("%-*s  %-*s  %s\n", maxFamily, "FAMILY", maxFull, "FULL NAME", "FILE")
	}
	fmt.Println(sep)

	prevFamily := ""
	for _, g := range groups {
		for i, m := range g.members {
			fam := g.family
			if fam == prevFamily {
				fam = "" // don't repeat family on continuation rows
			} else {
				prevFamily = fam
				if i > 0 {
					// already printed family on first member; blank it for rest
					fam = g.family
				}
			}
			file := filepath.Base(m.File)
			if details {
				sub := m.Subfamily
				if sub == "" {
					sub = "—"
				}
				ps := m.PostScript
				if ps == "" {
					ps = "—"
				}
				fmt.Printf("%-*s  %-14s  %-*s  %s\n",
					maxFamily, truncate(fam, maxFamily),
					truncate(sub, 14),
					maxPS, truncate(ps, maxPS),
					file)
			} else {
				fmt.Printf("%-*s  %-*s  %s\n",
					maxFamily, truncate(fam, maxFamily),
					maxFull, truncate(m.FullName, maxFull),
					file)
			}
		}
	}
	fmt.Println(sep)
	fmt.Printf("%d font file(s) in %d famil", len(fonts), len(groups))
	if len(groups) == 1 {
		fmt.Println("y")
	} else {
		fmt.Println("ies")
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// scanDirs walks all given directories and returns parsed font metadata.
func scanDirs(dirs []string) []FontInfo {
	var results []FontInfo
	seen := map[string]bool{}

	for _, dir := range dirs {
		_ = walkDir(dir, func(path string) {
			if seen[path] {
				return
			}
			seen[path] = true

			data, err := os.ReadFile(path)
			if err != nil {
				return
			}
			f, err := truetype.Parse(data)
			if err != nil {
				// Unreadable font (bad tables etc) — skip silently.
				return
			}
			info := FontInfo{
				File:       path,
				Family:     f.Name(truetype.NameIDFontFamily),
				Subfamily:  f.Name(truetype.NameIDFontSubfamily),
				FullName:   f.Name(truetype.NameIDFontFullName),
				PostScript: f.Name(truetype.NameIDPostscriptName),
			}
			// Prefer "Preferred Family" (name id 16) when present — it groups
			// weights together (e.g. "Helvetica Neue" instead of "Helvetica Neue UltraLight").
			if pf := f.Name(truetype.NameIDPreferredFamily); pf != "" {
				info.Family = pf
			}
			if ps := f.Name(truetype.NameIDPreferredSubfamily); ps != "" {
				info.Subfamily = ps
			}
			if info.Family == "" {
				info.Family = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
			results = append(results, info)
		})
	}
	return results
}

func walkDir(dir string, fn func(string)) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			_ = walkDir(full, fn)
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".ttf" || ext == ".otf" {
			fn(full)
		}
	}
	return nil
}

// systemFontDirs returns the canonical font search paths for the current OS.
func systemFontDirs() []string {
	var dirs []string
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "darwin":
		dirs = []string{
			"/System/Library/Fonts",
			"/Library/Fonts",
		}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, "Library", "Fonts"))
		}
		// macOS 10.15+ also has per-user font container
		dirs = append(dirs,
			"/System/Library/Fonts/Supplemental",
			"/Network/Library/Fonts",
		)

	case "windows":
		windir := os.Getenv("WINDIR")
		if windir == "" {
			windir = `C:\Windows`
		}
		dirs = []string{filepath.Join(windir, "Fonts")}
		if home != "" {
			dirs = append(dirs,
				filepath.Join(home, "AppData", "Local", "Microsoft", "Windows", "Fonts"),
			)
		}

	default: // Linux and other Unix-likes
		dirs = []string{
			"/usr/share/fonts",
			"/usr/local/share/fonts",
		}
		if home != "" {
			dirs = append(dirs,
				filepath.Join(home, ".fonts"),
				filepath.Join(home, ".local", "share", "fonts"),
			)
		}
	}
	return dirs
}
