package main

import (
	"strings"
	"time"
)

const (
	glyphHeight = 7
	quitHelp    = "q / Esc / Ctrl+C to quit"
)

var digits = map[rune][glyphHeight]string{
	'0': {"11111", "11 11", "11 11", "11 11", "11 11", "11 11", "11111"},
	'1': {"11", "11", "11", "11", "11", "11", "11"},
	'2': {"11111", "   11", "   11", "11111", "11   ", "11   ", "11111"},
	'3': {"11111", "   11", "   11", "11111", "   11", "   11", "11111"},
	'4': {"11 11", "11 11", "11 11", "11111", "   11", "   11", "   11"},
	'5': {"11111", "11   ", "11   ", "11111", "   11", "   11", "11111"},
	'6': {"11111", "11   ", "11   ", "11111", "11 11", "11 11", "11111"},
	'7': {"11111", "   11", "   11", "   11", "   11", "   11", "   11"},
	'8': {"11111", "11 11", "11 11", "11111", "11 11", "11 11", "11111"},
	'9': {"11111", "11 11", "11 11", "11111", "   11", "   11", "11111"},
	':': {" ", "1", "1", " ", "1", "1", " "},
}

type options struct {
	twelve          bool
	seconds         bool
	omitLeadingZero bool
	showAMPM        bool
	location        *time.Location

	showDate   bool
	showZone   bool
	showHelp   bool
	dateFormat string

	alignment string
	scale     int

	digitColor string
	dateColor  string
	helpColor  string
	background string
}

func clockText(now time.Time, opts options) string {
	format := "15:04"
	if opts.twelve {
		format = "03:04"
	}
	if opts.seconds {
		format += ":05"
	}
	value := now.Format(format)
	if opts.omitLeadingZero {
		value = strings.TrimPrefix(value, "0")
	}
	return value
}

func bigText(value string, horizontal, vertical int) []string {
	lines := make([]string, 0, glyphHeight*vertical)
	space := strings.Repeat(" ", horizontal)
	block := strings.Repeat("█", horizontal)
	for row := 0; row < glyphHeight; row++ {
		var line strings.Builder
		for i, digit := range value {
			if i > 0 {
				line.WriteString(space)
			}
			for _, cell := range digits[digit][row] {
				pixel := space
				if cell == '1' {
					pixel = block
				}
				line.WriteString(pixel)
			}
		}
		for repeat := 0; repeat < vertical; repeat++ {
			lines = append(lines, line.String())
		}
	}
	return lines
}

func textWidth(value string) int {
	width := max(0, len(value)-1) // One cell between glyphs.
	for _, digit := range value {
		width += len(digits[digit][0])
	}
	return width
}

type displayLine struct {
	text, color string
}

func render(now time.Time, opts options, width, height int) string {
	if width < 1 || height < 1 {
		return ""
	}
	value := clockText(now, opts)
	baseWidth := textWidth(value)
	clockLines := []string{value}
	var labelParts []string
	if opts.showDate {
		labelParts = append(labelParts, now.Format(opts.dateFormat))
	}
	if opts.showZone {
		labelParts = append(labelParts, now.Format("MST"))
	}
	if opts.twelve && opts.showAMPM {
		labelParts = append(labelParts, now.Format("PM"))
	}
	label := strings.Join(labelParts, "  ·  ")
	overhead := 0
	if label != "" {
		overhead += 2
	}
	if opts.showHelp {
		overhead += 2
	}
	if width >= baseWidth && height >= glyphHeight {
		horizontal, vertical := 1, 1
		if width >= baseWidth*2 {
			scale := min(width/(baseWidth*2), max(1, (height-overhead)/glyphHeight), 4)
			if opts.scale > 0 {
				scale = min(scale, opts.scale)
			}
			horizontal, vertical = 2*scale, scale
		}
		clockLines = bigText(value, horizontal, vertical)
	}
	var lines []displayLine
	if label != "" && height >= len(clockLines)+2 {
		lines = append(lines, displayLine{label, opts.dateColor}, displayLine{})
	}
	for _, line := range clockLines {
		lines = append(lines, displayLine{line, opts.digitColor})
	}
	if opts.showHelp && height >= len(lines)+2 {
		lines = append(lines, displayLine{}, displayLine{quitHelp, opts.helpColor})
	}
	top := (height - len(lines)) / 2
	screen := make([]string, height)
	for i := range screen {
		line := displayLine{}
		if index := i - top; index >= 0 && index < len(lines) {
			line = lines[index]
		}
		runes := []rune(line.text)
		if len(runes) > width {
			runes = runes[:width]
		}
		padding := (width - len(runes)) / 2
		if opts.alignment == "left" {
			padding = 0
		}
		if opts.alignment == "right" {
			padding = width - len(runes)
		}
		screen[i] = opts.background + "\x1b[2K" + line.color + strings.Repeat(" ", padding) + string(runes)
		if opts.background != "" || line.color != "" {
			screen[i] += "\x1b[0m"
		}
	}
	return strings.Join(screen, "\r\n")
}
