package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if err := start(); err != nil {
		fmt.Fprintln(os.Stderr, "clock:", err)
		os.Exit(1)
	}
}

func start() error {
	twelve := flag.Bool("12", false, "use a 12-hour clock")
	seconds := flag.Bool("seconds", true, "show seconds")
	omitLeadingZero := flag.Bool("omit-leading-zero", false, "omit the leading zero in hours")
	showAMPM := flag.Bool("am-pm", true, "show AM/PM in 12-hour format")
	zone := flag.String("zone", "", "IANA time zone, e.g. America/New_York (default: local)")
	configPath := flag.String("config", "", "config file (default: ~/.config/clock/config.json)")
	install := flag.Bool("install", false, "replace config with downloaded or built-in defaults, then exit")
	edit := flag.Bool("edit", false, "open config in $VISUAL, $EDITOR, or vi, then exit")
	flag.Parse()
	if *configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		*configPath = filepath.Join(home, ".config", "clock", "config.json")
	}
	if *install {
		if err := installConfig(*configPath, os.Getenv("CONFIGURATION_ROOT"), os.Stderr); err != nil {
			return fmt.Errorf("install: %w", err)
		}
		fmt.Println("Installed config:", *configPath)
	}
	if *edit {
		if err := editConfig(*configPath); err != nil {
			return err
		}
	}
	if *install || *edit {
		return nil
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("config %s: %w", *configPath, err)
	}
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "12":
			cfg.HourFormat = 24
			if *twelve {
				cfg.HourFormat = 12
			}
		case "seconds":
			cfg.ShowSeconds = *seconds
		case "omit-leading-zero":
			cfg.OmitLeadingZero = *omitLeadingZero
		case "am-pm":
			cfg.ShowAMPM = *showAMPM
		case "zone":
			cfg.TimeZone = *zone
		}
	})
	opts, err := cfg.options()
	if err != nil {
		return fmt.Errorf("config %s: %w", *configPath, err)
	}
	return run(opts)
}
