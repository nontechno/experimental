# Terminal clock

A Go TUI clock with large solid block digits, centered in your terminal. It updates every second and adapts to terminal resizing. Small windows use smaller digits or plain text.

Requires Go 1.22 or later and a terminal with a Unicode font and ANSI escape support.

```sh
go build -buildvcs=false -o clock .
./clock
```

Press **q**, **Esc**, or **Ctrl+C** to exit. The clock restores the terminal and cursor on exit.

```sh
./clock -install                # replace/create config with defaults
./clock -edit                   # open config in your editor, then exit
./clock -12                     # 12-hour format with AM/PM
./clock -12 -am-pm=false        # 12-hour format without AM/PM
./clock -seconds=false          # hours and minutes only
./clock -omit-leading-zero      # 09:04 becomes 9:04
./clock -zone America/New_York  # use a specific time zone
./clock -h                      # all options
```

The default is local time in 24-hour format, including seconds. For the full-size `HH:MM:SS` display, use a terminal at least 78 columns wide and 11 rows tall.

On first launch, the app creates `~/.config/clock/config.json`. Edit it and restart the clock to apply changes. Missing settings use defaults; invalid settings and unknown keys produce an error. Explicit command-line flags override the file without changing it. Use `-config /path/to/config.json` to select another file.

`-install` creates the config directory and replaces any existing config with defaults, then exits. When `CONFIGURATION_ROOT` is nonempty, it first attempts to download `<root>/clock.config.json` over HTTP or HTTPS. The download has a 10-second timeout and a 1 MiB limit. Valid downloaded settings override built-in defaults; missing settings inherit built-in defaults. If downloading or validation fails, the app prints a warning and installs built-in defaults. The config is replaced atomically.

```sh
CONFIGURATION_ROOT=https://aaa.bbb.ccc/ddd/eee ./clock -install
# Downloads https://aaa.bbb.ccc/ddd/eee/clock.config.json
```

`-edit` creates the file only if missing, then opens it using `$VISUAL`, `$EDITOR`, or `vi`, in that order. Editor variables may include arguments (for example, `EDITOR='nano -w'`). The editor receives the terminal directly. Editing works even when the existing JSON is invalid. Both commands honor `-config`; `-install -edit` replaces the config and opens it. Normal startup and `-edit` preserve existing settings and do not download configs.

```json
{
  "hour_format": 24,
  "omit_leading_zero": false,
  "show_seconds": true,
  "show_am_pm": true,
  "time_zone": "Local",
  "show_date": true,
  "date_format": "Monday, 02 January 2006",
  "show_time_zone": true,
  "show_help": true,
  "scale": 0,
  "alignment": "center",
  "digit_color": "orange",
  "date_color": "orange",
  "help_color": "orange",
  "background": "black"
}
```

- `hour_format`: `12` or `24`.
- `show_am_pm`: show the AM/PM label in 12-hour format (default `true`). Set to `false` to hide it. Has no effect in 24-hour format.
- `omit_leading_zero`: `true` displays `9:04` instead of `09:04` in either hour format. Defaults to `false`. Midnight in 24-hour format becomes `0:00`.
- `time_zone`: `Local`, `UTC`, or an IANA name such as `America/Los_Angeles`.
- `show_seconds`, `show_date`, `show_time_zone`, `show_help`: show or hide each element. Labels are omitted when the terminal is too short.
- `date_format`: a Go time layout. For example, `2006-01-02` shows a numeric date. Use printable, single-column characters.
- `scale`: `0` chooses the largest size that fits, up to `4`. Values `1`–`4` cap the digit size; smaller terminals automatically shrink the digits.
- `alignment`: choose `top-left`, `top-center`, `top-right`, `middle-left`, `middle-center`, `middle-right`, `bottom-left`, `bottom-center`, or `bottom-right`. The whole display, including visible date and help, is placed vertically; each line follows the horizontal alignment. Existing `left`, `center`, and `right` values keep vertical centering. `top`, `middle`, and `bottom` are shortcuts for horizontal centering. Default: `center`.
- Colors: `default`, `orange`, `black`, `red`, `green`, `yellow`, `blue`, `magenta`, `cyan`, `white`. The eight standard colors also accept a `bright_` prefix. Orange uses RGB `#FFA500` and requires a terminal with true-color support; standard colors follow the terminal palette. Defaults are orange text on black.

Digits always use Full Block `█` (U+2588). The digit `1` is narrower than other digits, and the display centers and scales using its actual width. A ready-to-copy configuration is included in `config.example.json`.

```sh
go test ./...
```
