# wallpaper

Tiles an image across a canvas of a given size and draws a text overlay with configurable font, color, position and margin.

## Build

```sh
go build -o wallpaper .
```

Dependencies (`github.com/golang/freetype` and `golang.org/x/image`) are
vendored — no network access needed.

## Usage

```sh
./wallpaper -config config.json
```

`-config` defaults to `config.json` in the current directory.

## Config fields

| Field        | Type   | Description |
|--------------|--------|-------------|
| `tile`       | string | Path to the tile image (PNG/JPEG). `~/` is expanded. |
| `width`      | int    | Output image width in pixels. |
| `height`     | int    | Output image height in pixels. |
| `message`    | string | Text to overlay. `\n` produces multiple lines. |
| `font.name`  | string | Font name (TTF/OTF) to search in system font dirs. |
| `font.size`  | float  | Font size in points (at 72 DPI). |
| `font.color` | string | Hex color `#RRGGBB` or `#RRGGBBAA`. |
| `margin`     | int    | Minimum pixels between text and image edge. |
| `placement`  | string | Comma-separated vertical + horizontal: `top/center/bottom` and `left/center/right`. |
| `output`     | string | Output image path (PNG or JPEG by extension). `~/` is expanded. |

### Placement examples

```
"bottom, right"   ← default-style: bottom-right corner
"top, left"       ← top-left corner
"center"          ← centred both axes
"top, center"     ← top-centre
```

## Font discovery

The program walks these directories in order, looking for a TTF/OTF file
whose base name matches `font.name` (case-insensitive, prefix match as
fallback):

- `/usr/share/fonts`, `/usr/local/share/fonts` (Linux)
- `~/.fonts` (Linux user fonts)
- `/Library/Fonts`, `/System/Library/Fonts`, `~/Library/Fonts` (macOS)
- `C:\Windows\Fonts`, `%LOCALAPPDATA%\Microsoft\Windows\Fonts` (Windows)
