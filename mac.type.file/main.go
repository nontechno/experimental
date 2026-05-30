package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation -framework Carbon

#include <CoreGraphics/CoreGraphics.h>
#include <Carbon/Carbon.h>
#include <stdlib.h>
#include <unistd.h>

// Post a key event (down + up) for a given virtual keycode with optional modifiers.
static void postKeyCode(CGKeyCode keyCode, CGEventFlags flags) {
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, keyCode, true);
    CGEventRef up   = CGEventCreateKeyboardEvent(NULL, keyCode, false);
    if (flags) {
        CGEventSetFlags(down, flags);
        CGEventSetFlags(up,   flags);
    }
    CGEventPost(kCGHIDEventTap, down);
    CGEventPost(kCGHIDEventTap, up);
    CFRelease(down);
    CFRelease(up);
    // Small delay so the receiving app can keep up.
    usleep(8000);
}

// Type a Unicode character by setting it directly on a keyboard event.
// This works for any Unicode codepoint without needing a keycode mapping.
static void postUnicodeChar(UniChar c) {
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0, true);
    CGEventRef up   = CGEventCreateKeyboardEvent(NULL, 0, false);
    CGEventKeyboardSetUnicodeString(down, 1, &c);
    CGEventKeyboardSetUnicodeString(up,   1, &c);
    CGEventPost(kCGHIDEventTap, down);
    CGEventPost(kCGHIDEventTap, up);
    CFRelease(down);
    CFRelease(up);
    usleep(8000);
}

// Post a Return key event.
static void postReturn() {
    postKeyCode(kVK_Return, 0);
}

// Post a Tab key event.
static void postTab() {
    postKeyCode(kVK_Tab, 0);
}

// Post a Delete (backspace) key event.
static void postDelete() {
    postKeyCode(kVK_Delete, 0);
}
*/
import "C"

import (
	"flag"
	"fmt"
	"os"
	"time"
	"unicode/utf16"
)

func main() {
	delay := flag.Duration("delay", 2*time.Second, "Initial delay before typing starts (gives you time to focus the target window)")
	charDelay := flag.Duration("char-delay", 0, "Extra delay between characters (default 8ms from C layer is usually enough)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: typefile [options] <file>\n\nOptions:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  typefile snippet.txt\n")
		fmt.Fprintf(os.Stderr, "  typefile -delay 3s snippet.txt\n")
		fmt.Fprintf(os.Stderr, "  typefile -char-delay 20ms snippet.txt\n")
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}

	filePath := flag.Arg(0)
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading file: %v\n", err)
		os.Exit(1)
	}

	if len(data) == 0 {
		fmt.Fprintf(os.Stderr, "file is empty\n")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Typing %d bytes from %q in %v — focus your target window now...\n",
		len(data), filePath, *delay)

	time.Sleep(*delay)

	text := string(data)
	runes := []rune(text)
	total := len(runes)

	for i, r := range runes {
		if *charDelay > 0 && i > 0 {
			time.Sleep(*charDelay)
		}

		switch r {
		case '\n':
			C.postReturn()
		case '\t':
			C.postTab()
		case '\x08': // backspace
			C.postDelete()
		default:
			// Encode rune to UTF-16. Most chars are a single UniChar;
			// supplementary plane chars need a surrogate pair.
			utf16Runes := utf16.Encode([]rune{r})
			for _, u := range utf16Runes {
				C.postUnicodeChar(C.UniChar(u))
			}
		}

		// Progress every 100 chars to stderr so you can see it's working.
		if (i+1)%100 == 0 || i+1 == total {
			fmt.Fprintf(os.Stderr, "\r  %d / %d chars typed", i+1, total)
		}
	}

	fmt.Fprintln(os.Stderr, "\nDone.")
}
