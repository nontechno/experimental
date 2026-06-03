// ansi-invert: adapt ANSI-colored terminal output from dark-background to
// light/white-background display.
//
// It parses all SGR (Select Graphic Rendition) escape sequences, maps every
// color code through a configurable table, and falls back to a sensible
// default inversion for anything not explicitly mapped.
//
// Usage:
//
//	ansi-invert [flags] [input-file]
//
// If no input file is given, stdin is read.  Output goes to stdout unless -o
// is specified.
//
// Flags:
//
//	-o  <file>       write output to file instead of stdout
//	-m  FROM=TO      add/override a mapping entry (repeatable)
//	-no-invert       disable default inversion; apply only explicit -m mappings
//	-list            print all unique color sequences found and exit (no output)
//	-v               print the substitutions that were applied (to stderr)
//
// Default color map (dark-bg → light-bg):
//
//	 97 → 30   bright-white fg → black fg   (was invisible on white)
//	 37 → 90   white fg       → dark-gray fg
//	107 → 40   bright-white bg → black bg
//	 47 → 100  white bg       → dark-gray bg
//
// For any 16-color code NOT in the explicit map, the default inversion is:
//
//	bright fg (90-97)  → standard fg (30-37)   subtract 60
//	bright bg (100-107)→ standard bg (40-47)   subtract 60
//	standard fg (30-37)→ unchanged             (already dark enough on white)
//	standard bg (40-47)→ unchanged
//
// 256-color (38;5;n / 48;5;n):
//
//	indices 0-7   ↔ 8-15   (standard ↔ bright)
//	indices 16-231 (6×6×6 cube): each component c → 5-c
//	indices 232-255 (grayscale): index → 487-index
//
// True-color (38;2;R;G;B / 48;2;R;G;B):
//
//	each channel c → 255-c   (bitwise complement)
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ─── built-in color map ───────────────────────────────────────────────────────

// builtinMap provides explicit color mappings for dark-background → light-background
// conversion.  Every entry here was deliberately chosen; the comment explains why.
//
// Standard fg colors (30–37) that are already readable on a white background are
// listed as identity mappings (FROM == TO) to document that they have been
// considered and require no change.  They take precedence over the generic
// "subtract-60" default rule, but the visible result is the same.
//
// Override any entry at runtime with  -m FROM=TO.
var builtinMap = map[int]int{
	// ── Standard foreground colors (30–36): visible on white, no change needed ──
	//    These are the colors used by the application:
	//      ansiRed    \033[31m
	//      ansiGreen  \033[32m
	//      ansiYellow \033[33m
	//      ansiBlue   \033[34m
	//      ansiCyan   \033[36m
	31: 31, // red      → red      (standard red is clearly visible on white)
	32: 32, // green    → green    (standard green is clearly visible on white)
	33: 33, // yellow   → yellow   (renders as dark-yellow/olive on white; readable)
	34: 34, // blue     → blue     (dark blue on white has excellent contrast)
	36: 36, // cyan     → cyan     (standard cyan is visible on white; use -m 36=34 for blue if preferred)

	// ── Near-white foreground: must darken for a white background ────────────
	//      ansiWhite  \033[97m
	//      ansiGray   \033[90m  (also handled by default bright→standard rule)
	97: 30, // bright-white → black     (was invisible on white)
	37: 90, // white        → dark-gray (plain white is invisible on white)
	90: 30, // dark-gray    → black     (improves contrast; same result as default rule)

	// ── Background colors ────────────────────────────────────────────────────
	107: 40,  // bright-white bg → black bg
	47:  100, // white bg        → dark-gray bg
}

// ─── SGR regex ────────────────────────────────────────────────────────────────

// sgrRe matches: ESC [ <params> m
// Params are digits and semicolons; we also handle the bare ESC[m (reset).
var sgrRe = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// ─── state ────────────────────────────────────────────────────────────────────

// activeMap is builtinMap + user -m overrides, built at startup.
var activeMap map[int]int

// noInvert disables the default brightness inversion for unmapped codes.
var noInvert bool

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	outputPath := flag.String("o", "", "output file (default: stdout)")
	listMode := flag.Bool("list", false, "list all color sequences found and exit")
	verboseFlag := flag.Bool("v", false, "print substitutions applied (stderr)")
	flag.BoolVar(&noInvert, "no-invert", false, "disable default inversion; use only explicit -m mappings")

	mappings := &repeatFlag{}
	flag.Var(mappings, "m", "color mapping FROM=TO, e.g. -m 97=30  (repeatable; overrides built-ins)")

	flag.Usage = usage
	flag.Parse()

	// Build the active map.
	activeMap = make(map[int]int, len(builtinMap))
	for k, v := range builtinMap {
		activeMap[k] = v
	}
	for _, kv := range mappings.vals {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			die("invalid mapping %q: expected FROM=TO", kv)
		}
		from, e1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		to, e2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if e1 != nil || e2 != nil {
			die("invalid mapping %q: both FROM and TO must be integers", kv)
		}
		activeMap[from] = to
	}

	// Read input.
	var raw []byte
	var err error
	switch flag.NArg() {
	case 0:
		raw, err = io.ReadAll(os.Stdin)
	case 1:
		raw, err = os.ReadFile(flag.Arg(0))
	default:
		die("too many arguments; expected at most one input file")
	}
	if err != nil {
		die("reading input: %v", err)
	}

	// ── list mode ──────────────────────────────────────────────────────────────
	if *listMode {
		listColors(raw)
		return
	}

	// ── transform ─────────────────────────────────────────────────────────────
	subs := map[int]int{}

	out := sgrRe.ReplaceAllStringFunc(string(raw), func(match string) string {
		sub := sgrRe.FindStringSubmatch(match)
		if sub == nil {
			return match
		}
		params := parseParams(sub[1])
		transformed, localSubs := transformParams(params)
		for k, v := range localSubs {
			subs[k] = v
		}
		parts := make([]string, len(transformed))
		for i, p := range transformed {
			parts[i] = strconv.Itoa(p)
		}
		return "\x1b[" + strings.Join(parts, ";") + "m"
	})

	// ── verbose report ─────────────────────────────────────────────────────────
	if *verboseFlag && len(subs) > 0 {
		keys := sortedKeys(subs)
		fmt.Fprintln(os.Stderr, "ansi-invert: substitutions applied:")
		for _, k := range keys {
			fmt.Fprintf(os.Stderr, "  SGR %3d → %3d  (%s → %s)\n",
				k, subs[k], colorName(k), colorName(subs[k]))
		}
	}

	// ── write output ───────────────────────────────────────────────────────────
	var outFile *os.File
	if *outputPath != "" {
		outFile, err = os.Create(*outputPath)
		if err != nil {
			die("creating output file: %v", err)
		}
		defer outFile.Close()
	} else {
		outFile = os.Stdout
	}
	if _, err := fmt.Fprint(outFile, out); err != nil {
		die("writing output: %v", err)
	}
}

// ─── SGR parameter transformation ─────────────────────────────────────────────

// parseParams splits "0;1;32" into []int{0,1,32}.
// A bare "" (from ESC[m) is treated as []int{0} (reset).
func parseParams(s string) []int {
	if s == "" {
		return []int{0}
	}
	parts := strings.Split(s, ";")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		if n, err := strconv.Atoi(p); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// transformParams converts a list of SGR parameters, returning the new list and
// a map of (original → new) for every substitution that was made.
func transformParams(params []int) ([]int, map[int]int) {
	out := make([]int, 0, len(params))
	subs := map[int]int{}
	i := 0

	for i < len(params) {
		p := params[i]

		switch {
		// ── extended color: 38 or 48 followed by a subtype ──────────────────
		case (p == 38 || p == 48) && i+1 < len(params):
			switch params[i+1] {

			case 5: // 256-color:  p;5;n
				if i+2 < len(params) {
					orig := params[i+2]
					inv := invert256(orig)
					if inv != orig {
						subs[orig] = inv
					}
					out = append(out, p, 5, inv)
					i += 3
					continue
				}

			case 2: // truecolor: p;2;R;G;B
				if i+4 < len(params) {
					r, g, b := params[i+2], params[i+3], params[i+4]
					nr, ng, nb := 255-r, 255-g, 255-b
					out = append(out, p, 2, nr, ng, nb)
					i += 5
					continue
				}
			}
			// Incomplete or unknown sub-type: pass through verbatim.
			out = append(out, p)
			i++

		// ── standard 16-color codes ──────────────────────────────────────────
		case is16Color(p):
			newP := map16Color(p)
			if newP != p {
				subs[p] = newP
			}
			out = append(out, newP)
			i++

		// ── everything else (reset, bold, underline, …): pass through ────────
		default:
			out = append(out, p)
			i++
		}
	}
	return out, subs
}

// is16Color returns true for all standard and bright fg/bg color codes.
func is16Color(n int) bool {
	return (n >= 30 && n <= 37) || (n >= 40 && n <= 47) ||
		(n >= 90 && n <= 97) || (n >= 100 && n <= 107)
}

// map16Color applies the active map, then the default inversion.
func map16Color(n int) int {
	if v, ok := activeMap[n]; ok {
		return v
	}
	if noInvert {
		return n
	}
	return defaultInvert16(n)
}

// defaultInvert16 darkens bright colors for a light background.
//
//   - Bright fg (90-97)  → standard fg (30-37): subtract 60
//   - Bright bg (100-107)→ standard bg (40-47): subtract 60
//   - Standard fg/bg: left unchanged (already dark, readable on white)
func defaultInvert16(n int) int {
	switch {
	case n >= 90 && n <= 97:
		return n - 60
	case n >= 100 && n <= 107:
		return n - 60
	default:
		return n
	}
}

// invert256 inverts a 256-color palette index.
func invert256(n int) int {
	// Explicit override?
	if v, ok := activeMap[n]; ok {
		return v
	}
	if noInvert {
		return n
	}
	switch {
	case n < 8: // standard colors  → bright equivalents
		return n + 8
	case n < 16: // bright colors    → standard equivalents
		return n - 8
	case n < 232: // 6×6×6 color cube: invert each component
		idx := n - 16
		b := idx % 6
		g := (idx / 6) % 6
		r := idx / 36
		return 16 + 36*(5-r) + 6*(5-g) + (5 - b)
	default: // grayscale ramp (232-255): mirror within the ramp
		return 487 - n
	}
}

// ─── --list mode ──────────────────────────────────────────────────────────────

type colorEntry struct {
	raw    string // the full escape sequence
	params []int
}

func listColors(data []byte) {
	seen := map[string]struct{}{}
	var entries []colorEntry

	sgrRe.ReplaceAllFunc(data, func(match []byte) []byte {
		sub := sgrRe.FindSubmatch(match)
		if sub == nil {
			return match
		}
		params := parseParams(string(sub[1]))

		// Only report sequences that contain at least one color code.
		hasColor := false
		for _, p := range params {
			if is16Color(p) || p == 38 || p == 48 {
				hasColor = true
				break
			}
		}
		if !hasColor {
			return match
		}

		key := string(match)
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			entries = append(entries, colorEntry{raw: key, params: params})
		}
		return match
	})

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].raw < entries[j].raw
	})

	fmt.Printf("Found %d unique color sequence(s):\n\n", len(entries))
	fmt.Printf("  %-22s  %-22s  %s\n", "Sequence (ESC=\\e)", "Params", "Description")
	fmt.Printf("  %-22s  %-22s  %s\n", strings.Repeat("-", 22), strings.Repeat("-", 22), strings.Repeat("-", 30))

	for _, e := range entries {
		display := strings.ReplaceAll(e.raw, "\x1b", `\e`)
		paramStrs := make([]string, len(e.params))
		for i, p := range e.params {
			paramStrs[i] = strconv.Itoa(p)
		}
		fmt.Printf("  %-22s  %-22s  %s\n",
			display,
			strings.Join(paramStrs, ";"),
			describeParams(e.params),
		)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// colorName returns a human-readable name for a single SGR color code.
func colorName(n int) string {
	names := map[int]string{
		30: "black fg", 31: "red fg", 32: "green fg", 33: "yellow fg",
		34: "blue fg", 35: "magenta fg", 36: "cyan fg", 37: "white fg",
		40: "black bg", 41: "red bg", 42: "green bg", 43: "yellow bg",
		44: "blue bg", 45: "magenta bg", 46: "cyan bg", 47: "white bg",
		90: "dark gray fg", 91: "bright red fg", 92: "bright green fg", 93: "bright yellow fg",
		94: "bright blue fg", 95: "bright magenta fg", 96: "bright cyan fg", 97: "bright white fg",
		100: "dark gray bg", 101: "bright red bg", 102: "bright green bg", 103: "bright yellow bg",
		104: "bright blue bg", 105: "bright magenta bg", 106: "bright cyan bg", 107: "bright white bg",
	}
	if s, ok := names[n]; ok {
		return s
	}
	return fmt.Sprintf("SGR %d", n)
}

// describeParams returns a compact description for a list of SGR params.
func describeParams(params []int) string {
	var parts []string
	for _, p := range params {
		parts = append(parts, colorName(p))
	}
	return strings.Join(parts, " + ")
}

func sortedKeys(m map[int]int) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ansi-invert: "+format+"\n", args...)
	os.Exit(1)
}

func usage() {
	fmt.Fprintf(os.Stderr, `ansi-invert — adapt ANSI-colored output from dark to light/white background

Usage:
  ansi-invert [flags] [input-file]

  If no input file is given stdin is read.  Output goes to stdout unless -o is set.

Flags:
  -o <file>       write output to this file
  -m FROM=TO      override or add a color mapping (repeatable)
                  e.g.  -m 36=34   remap cyan → dark-blue
  -no-invert      disable default inversion; only apply explicit -m mappings
  -list           print all unique color sequences found, then exit
  -v              print substitutions applied (to stderr)
  -h / --help     this message

Default built-in map (dark → light bg):
  97 → 30   bright-white fg → black fg
  37 → 90   white fg        → dark-gray fg
 107 → 40   bright-white bg → black bg
  47 → 100  white bg        → dark-gray bg

Default inversion for all other 16-color codes not in the map:
  bright fg  (90–97)  → standard fg  (30–37)   [subtract 60]
  bright bg (100–107) → standard bg  (40–47)   [subtract 60]
  standard fg/bg      → unchanged
  256-color, truecolor → component-wise inversion

Examples:
  ansi-invert output.txt                  # auto-invert for white bg, to stdout
  ansi-invert -o output-light.txt output.txt
  ansi-invert -list output.txt            # list colors used
  ansi-invert -v output.txt 2>subs.txt    # see what changed
  ansi-invert -m 36=34 output.txt         # also remap cyan to dark-blue
  ansi-invert -no-invert -m 97=30 out.txt # only fix bright-white, nothing else
`)
}

// ─── flag helper ──────────────────────────────────────────────────────────────

// repeatFlag collects multiple -m flags.
type repeatFlag struct{ vals []string }

func (f *repeatFlag) String() string     { return strings.Join(f.vals, ",") }
func (f *repeatFlag) Set(v string) error { f.vals = append(f.vals, v); return nil }
