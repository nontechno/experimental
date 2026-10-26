package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"
)

func run(opts options) error {
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(in) || !term.IsTerminal(out) {
		return fmt.Errorf("run in an interactive terminal")
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	state, err := term.MakeRaw(in)
	if err != nil {
		return err
	}
	defer term.Restore(in, state)
	defer io.WriteString(os.Stdout, "\x1b[0m\x1b[?25h\x1b[?1049l")
	if _, err := io.WriteString(os.Stdout, "\x1b[?1049h\x1b[?25l\x1b[2J"); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32)
		for {
			n, err := os.Stdin.Read(buf)
			for _, key := range buf[:n] {
				if key == 'q' || key == 'Q' || key == 3 || key == 4 || key == 27 {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	previous := ""
	for {
		width, height, err := term.GetSize(out)
		if err != nil {
			return err
		}
		frame := render(time.Now().In(opts.location), opts, width, height)
		if frame != previous {
			if _, err := io.WriteString(os.Stdout, "\x1b[H"+frame); err != nil {
				return err
			}
			previous = frame
		}
		select {
		case <-done:
			return nil
		case <-signals:
			return nil
		case <-ticker.C:
		}
	}
}
