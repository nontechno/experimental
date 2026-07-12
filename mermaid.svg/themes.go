package main

import (
	_ "embed"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Each theme is a standalone CSS file under themes/.
// The Go embed directive pulls them in at compile time —
// no runtime file I/O, single binary, editable as plain CSS.

//go:embed styles/default.css
var Default string

//go:embed styles/dark.css
var Dark string

//go:embed styles/condensed.css
var Condensed string

//go:embed styles/large.css
var Large string

//go:embed styles/sky.css
var Sky string

//go:embed styles/forest.css
var Forest string

//go:embed styles/sepia.css
var Sepia string

//go:embed styles/interactive.html
var interactivePlug string

//go:embed styles/mermaidInit.js
var mermaidInit string

// Themes is the runtime registry used by the -theme flag.
var Themes = map[string]string{
	"default":   Default,
	"dark":      Dark,
	"condensed": Condensed,
	"large":     Large,
	"sky":       Sky,
	"forest":    Forest,
	"sepia":     Sepia,
}

func ResolveTheme(themeName string) (string, error) {
	if theme, ok := Themes[themeName]; !ok {
		// let's see if what we were given is a filename
		if raw, err := os.ReadFile(themeName); err == nil && len(raw) > 0 {
			return string(raw), nil
		}

		// todo: one more option - external .css file
		// <link rel="stylesheet" href="styles.css">

		return "", fmt.Errorf("unknown theme %q — available: %s", themeName, availableThemes())
	} else {
		return theme, nil
	}
}

func availableThemes() string {
	// Build sorted theme names for the usage string.
	themeNames := make([]string, 0, len(Themes))
	for k := range Themes {
		themeNames = append(themeNames, k)
	}
	sort.Strings(themeNames)
	return strings.Join(themeNames, ", ")
}
