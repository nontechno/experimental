package svgdoc

import (
	"fmt"
	"strings"
)

var named = map[string]string{
	"black": "#000000", "white": "#ffffff", "red": "#ff0000", "green": "#008000",
	"blue": "#0000ff", "yellow": "#ffff00", "gray": "#808080", "grey": "#808080",
	"lightgray": "#d3d3d3", "lightgrey": "#d3d3d3", "darkgray": "#a9a9a9",
	"darkgrey": "#a9a9a9", "silver": "#c0c0c0", "orange": "#ffa500",
	"purple": "#800080", "navy": "#000080", "teal": "#008080", "maroon": "#800000",
	"olive": "#808000", "lime": "#00ff00", "aqua": "#00ffff", "cyan": "#00ffff",
	"fuchsia": "#ff00ff", "magenta": "#ff00ff", "pink": "#ffc0cb", "brown": "#a52a2a",
	"lightblue": "#add8e6", "lightyellow": "#ffffe0", "whitesmoke": "#f5f5f5",
	"gold": "#ffd700", "beige": "#f5f5dc", "lightgreen": "#90ee90",
}

// Color normalizes an SVG paint value into "#rrggbb". ok is false for
// none / transparent / url(...) / unknown values.
func Color(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case v == "" || v == "none" || v == "transparent" || strings.HasPrefix(v, "url("):
		return "", false
	case strings.HasPrefix(v, "#") && len(v) == 7:
		return v, true
	case strings.HasPrefix(v, "#") && len(v) == 4:
		return fmt.Sprintf("#%c%c%c%c%c%c", v[1], v[1], v[2], v[2], v[3], v[3]), true
	case strings.HasPrefix(v, "#") && len(v) == 9: // #rrggbbaa
		return v[:7], true
	case strings.HasPrefix(v, "rgb"):
		n := Numbers(v)
		if len(n) >= 3 {
			if strings.Contains(v, "%") {
				for i := range n[:3] {
					n[i] = n[i] * 2.55
				}
			}
			return fmt.Sprintf("#%02x%02x%02x", clampByte(n[0]), clampByte(n[1]), clampByte(n[2])), true
		}
	}
	if c, ok := named[v]; ok {
		return c, true
	}
	return "", false
}

func clampByte(f float64) int {
	if f < 0 {
		return 0
	}
	if f > 255 {
		return 255
	}
	return int(f + 0.5)
}
