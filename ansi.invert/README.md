# ansi.invert

Adapt terminal output that was designed for a **dark background** so it looks
good on a **light / white background**.

It reads a text file containing ANSI SGR escape sequences, maps every color
code through a configurable table, and writes the adjusted file.

---

## Install

```sh
go build -o ansi.invert .
# or install to $GOPATH/bin
go install .
```

Requires Go 1.21+, zero external dependencies.

---

## Usage

```
ansi.invert [flags] [input-file]
```

If no input file is given, stdin is read.  
Output goes to stdout unless `-o` is given.

### Flags

| Flag | Description |
|------|-------------|
| `-o <file>` | Write output to this file |
| `-m FROM=TO` | Add / override a color mapping (repeatable) |
| `-no-invert` | Disable default inversion; apply only explicit `-m` mappings |
| `-list` | Print all unique color sequences found, then exit |
| `-v` | Print substitutions that were applied (to stderr) |

---

## How it works

### Step 1 — Parse

Every `ESC [ <params> m` (SGR) sequence is extracted.  The parameter list
(`0;1;32`, etc.) is split on `;` and each integer is classified.

### Step 2 — Identify colors

Color-bearing parameters are:

| Range | Meaning |
|-------|---------|
| 30–37 | Standard foreground (black … white) |
| 40–47 | Standard background |
| 90–97 | Bright / intense foreground |
| 100–107 | Bright / intense background |
| `38;5;n` | 256-color foreground |
| `48;5;n` | 256-color background |
| `38;2;R;G;B` | True-color foreground |
| `48;2;R;G;B` | True-color background |

Non-color codes (reset `0`, bold `1`, underline `4`, …) pass through unchanged.

### Step 3 — Map / invert

For each color code, in priority order:

1. **Explicit `-m` override** — used verbatim.  
2. **Built-in map** — handles the worst cases for white backgrounds:

   | From | To | Reason |
   |------|----|--------|
   | `97` | `30` | bright-white fg → black (was invisible on white) |
   | `37` | `90` | white fg → dark-gray |
   | `107` | `40` | bright-white bg → black bg |
   | `47` | `100` | white bg → dark-gray bg |

3. **Default inversion** (unless `-no-invert`):

   | Code range | Action |
   |------------|--------|
   | Bright fg 90–97 | → standard fg 30–37 (subtract 60) |
   | Bright bg 100–107 | → standard bg 40–47 (subtract 60) |
   | Standard fg/bg 30–37, 40–47 | unchanged (already dark enough on white) |
   | 256-color 0–7 | → 8–15 (standard ↔ bright) |
   | 256-color 8–15 | → 0–7 |
   | 256-color 16–231 (cube) | each component c → 5−c |
   | 256-color 232–255 (gray) | index → 487−index |
   | True-color R,G,B | → 255−R, 255−G, 255−B |

### Step 4 — Rebuild & write

The modified parameter list is reassembled into the same `ESC[…m` syntax and
the file is written out.

---

## Examples

```sh
# Basic: convert for white background
ansi.invert output.txt > output-light.txt

# Write to a file instead of stdout
ansi.invert -o output-light.txt output.txt

# See what changed
ansi.invert -v output.txt > output-light.txt

# Also remap cyan to dark-blue (in addition to built-in rules)
ansi.invert -m 36=34 output.txt > output-light.txt

# Disable all defaults; only fix bright-white
ansi.invert -no-invert -m 97=30 output.txt > output-light.txt

# Inspect what colors a file uses before converting
ansi.invert -list output.txt

# Pipe from a command
some-tool | ansi.invert | less -R
```

---

## Extending the color map

Use one or more `-m FROM=TO` flags to add or override any mapping entry.
Values are raw SGR integers.  Custom mappings take priority over the built-in
map.

```sh
# Override built-in: keep bright-white instead of converting to black
ansi.invert -m 97=97 output.txt

# Remap several colors at once
ansi.invert -m 90=30 -m 36=34 -m 32=22 output.txt
```

---

## SGR color reference

```
30 black fg     31 red fg      32 green fg    33 yellow fg
34 blue fg      35 magenta fg  36 cyan fg     37 white fg
40 black bg     41 red bg      42 green bg    43 yellow bg
44 blue bg      45 magenta bg  46 cyan bg     47 white bg

90 dark-gray fg   91 bright-red fg   92 bright-green fg  93 bright-yellow fg
94 bright-blue fg 95 bright-magenta  96 bright-cyan fg   97 bright-white fg
100–107: same as 90–97 but for background
```
