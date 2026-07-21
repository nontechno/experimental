package main

import (
	_ "embed"
	"fmt"
	"os"
	"sort"
	"strings"
)

//go:embed styles/mermaid/mermaid-sequence-theme-bold.json
var mermaidThemeBold string

//go:embed styles/mermaid/mermaid-sequence-theme-bright.json
var mermaidThemeBright string

//go:embed styles/mermaid/mermaid-sequence-theme-compact.json
var mermaidThemeCompact string

//go:embed styles/mermaid/mermaid-sequence-theme-default.json
var mermaidThemeDefault string

//go:embed styles/mermaid/mermaid-sequence-theme-dusk.json
var mermaidThemeDusk string

//go:embed styles/mermaid/mermaid-sequence-theme-forest.json
var mermaidThemeForest string

//go:embed styles/mermaid/mermaid-sequence-theme-large.json
var mermaidThemeLarge string

//go:embed styles/mermaid/mermaid-sequence-theme-light.json
var mermaidThemeLight string

//go:embed styles/mermaid/mermaid-sequence-theme-navy-blue.json
var mermaidThemeNavyBlue string

//go:embed styles/mermaid/mermaid-sequence-theme-ocean.json
var mermaidThemeOcean string

//go:embed styles/mermaid/mermaid-sequence-theme-orange.json
var mermaidThemeOrange string

//go:embed styles/mermaid/mermaid-sequence-theme-pastel.json
var mermaidThemePastel string

//go:embed styles/mermaid/mermaid-sequence-theme-sepia.json
var mermaidThemeSepia string

var mermaidThemes = map[string]string{
	"Bold":     mermaidThemeBold,
	"Bright":   mermaidThemeBright,
	"Compact":  mermaidThemeCompact,
	"default":  mermaidThemeDefault,
	"Dusk":     mermaidThemeDusk,
	"Forest":   mermaidThemeForest,
	"Large":    mermaidThemeLarge,
	"Light":    mermaidThemeLight,
	"NavyBlue": mermaidThemeNavyBlue,
	"Ocean":    mermaidThemeOcean,
	"Orange":   mermaidThemeOrange,
	"Pastel":   mermaidThemePastel,
	"Sepia":    mermaidThemeSepia,
}

func ResolveMermaidTheme(themeName string) (string, error) {
	if theme, ok := mermaidThemes[themeName]; !ok {
		// let's see if what we were given is a filename
		if raw, err := os.ReadFile(themeName); err == nil && len(raw) > 0 { // todo: this is untested
			return string(raw), nil
		}

		// todo: one more option - external .css file
		// <link rel="stylesheet" href="styles.css">

		return "", fmt.Errorf("unknown theme %q — available: %s", themeName, availableThemes())
	} else {
		return theme, nil
	}
}

func availableMermaidThemes() string {
	// Build sorted theme names for the usage string.
	themeNames := make([]string, 0, len(mermaidThemes))
	for k := range mermaidThemes {
		themeNames = append(themeNames, k)
	}
	sort.Strings(themeNames)
	return strings.Join(themeNames, ", ")
}
