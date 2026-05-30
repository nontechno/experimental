# typefile

Types the contents of a file into the focused window, character by character, using macOS CoreGraphics keyboard events. No clipboard is touched, no UI appears.

## How it works

Uses `CGEventCreateKeyboardEvent` + `CGEventKeyboardSetUnicodeString` via cgo to synthesize real HID-level keyboard events. Works with any Unicode text. Newlines become Return keypresses, tabs become Tab keypresses.

## Requirements

- macOS (uses CoreGraphics, Carbon)
- Go with cgo enabled (standard macOS Go install)
- **Accessibility permission**: the binary (or Terminal.app) must be granted Accessibility access in  
  *System Settings → Privacy & Security → Accessibility*

## Build

```bash
go build -o typefile .
```

Or install to your PATH:

```bash
go install .
```

## Usage

```
typefile [options] <file>

Options:
  -delay duration       Initial delay before typing starts — use this to
                        switch to the target window (default 2s)
  -char-delay duration  Extra per-character delay on top of the built-in 8ms
                        (e.g. -char-delay 20ms for slow apps)
```

## Examples

```bash
# Type snippet.txt after a 2-second delay (default)
typefile snippet.txt

# Give yourself 5 seconds to focus the target window
typefile -delay 5s snippet.txt

# Slow down per-character for a laggy app
typefile -delay 3s -char-delay 15ms snippet.txt
```

## Karabiner-Elements integration

In your `complex_modifications` rule, call it per key with a hardcoded file argument:

```json
{
  "manipulators": [
    {
      "type": "basic",
      "from": { "key_code": "f1" },
      "to": [{ "shell_command": "/usr/local/bin/typefile -delay 0s ~/snippets/f1.txt" }]
    }
  ]
}
```

With `-delay 0s` Karabiner triggers it instantly; the focused window is already correct since you're pressing the key there.

## Notes

- Progress is printed to stderr so it doesn't interfere with stdout redirection.
- The built-in 8 ms inter-character pause (`usleep(8000)` in the C layer) is enough for most apps. Increase with `-char-delay` if characters get dropped.
- Surrogate pairs (emoji, rare CJK) are handled correctly via UTF-16 encoding.
