// Command journal-tail reads entries from the systemd journal using
// github.com/coreos/go-systemd/v22/sdjournal.
//
// It verifies that the host is actually running systemd and that the
// current process has sufficient privileges to open the journal before
// attempting to read anything, and reports clear, actionable errors when
// either check fails. It exits cleanly on SIGINT/SIGTERM (Ctrl-C).
//
// Build requirements:
//   - cgo enabled (CGO_ENABLED=1, the default on Linux)
//   - libsystemd development headers installed, e.g.:
//     Debian/Ubuntu: apt install libsystemd-dev
//     Fedora/RHEL:   dnf install systemd-devel
//
// Usage examples:
//
//	journal-tail                          # last 10 entries, then exit
//	journal-tail -f                       # last 10 entries, then follow
//	journal-tail -u sshd.service -f       # follow a specific unit
//	journal-tail -u sshd.service,cron -f  # follow multiple units (OR)
//	journal-tail -k -f                    # follow kernel messages only
//	journal-tail -b -p err -f             # current boot, priority <= err
//	journal-tail -n 50                    # last 50 entries
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/sdjournal"
)

// syslog priority levels, as used by the PRIORITY journal field.
var priorityLevels = map[string]int{
	"emerg":   0,
	"alert":   1,
	"crit":    2,
	"err":     3,
	"error":   3,
	"warning": 4,
	"warn":    4,
	"notice":  5,
	"info":    6,
	"debug":   7,
}

type config struct {
	units      []string
	follow     bool
	numEntries int
	kernel     bool
	thisBoot   bool
	priority   int // -1 means "no filter"
}

func main() {
	cfg, err := parseFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		flag.Usage()
		os.Exit(2)
	}

	if err := checkSystemdPresent(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	j, err := openJournal()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	defer j.Close()

	if err := applyFilters(j, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, j, cfg); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "\nexiting cleanly")
}

func parseFlags() (config, error) {
	var (
		unitsFlag = flag.String("u", "", "comma-separated systemd unit names to filter on, e.g. sshd.service,cron")
		follow    = flag.Bool("f", false, "follow the journal (like tail -f)")
		numLines  = flag.Int("n", 10, "number of most recent entries to show before following")
		kernel    = flag.Bool("k", false, "show kernel messages only (equivalent to -k in journalctl)")
		thisBoot  = flag.Bool("b", false, "only show entries from the current boot")
		priority  = flag.String("p", "", "only show entries at or above this severity (emerg,alert,crit,err,warning,notice,info,debug)")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	cfg := config{
		follow:     *follow,
		numEntries: *numLines,
		kernel:     *kernel,
		thisBoot:   *thisBoot,
		priority:   -1,
	}

	if *unitsFlag != "" {
		for _, u := range strings.Split(*unitsFlag, ",") {
			u = strings.TrimSpace(u)
			if u != "" {
				cfg.units = append(cfg.units, u)
			}
		}
	}

	if cfg.kernel && len(cfg.units) > 0 {
		return cfg, errors.New("-k (kernel messages) cannot be combined with -u (unit filter); kernel entries have no systemd unit")
	}

	if *priority != "" {
		level, ok := priorityLevels[strings.ToLower(*priority)]
		if !ok {
			return cfg, fmt.Errorf("invalid -p value %q (expected one of: emerg,alert,crit,err,warning,notice,info,debug)", *priority)
		}
		cfg.priority = level
	}

	if cfg.numEntries < 0 {
		return cfg, errors.New("-n must be >= 0")
	}

	return cfg, nil
}

// checkSystemdPresent verifies the host is actually running systemd.
// This is the same check systemd's own tools use internally.
func checkSystemdPresent() error {
	info, err := os.Stat("/run/systemd/system")
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("this host does not appear to be running systemd (/run/systemd/system not found); journal-tail requires systemd-journald")
		}
		return fmt.Errorf("could not check for systemd (/run/systemd/system): %w", err)
	}
	if !info.IsDir() {
		return errors.New("/run/systemd/system exists but is not a directory; systemd does not appear to be running normally")
	}
	return nil
}

// openJournal opens the local systemd journal, translating common failure
// modes (missing library, insufficient privileges) into actionable messages.
func openJournal() (*sdjournal.Journal, error) {
	j, err := sdjournal.NewJournal()
	if err != nil {
		switch {
		case os.IsPermission(err) || errors.Is(err, syscall.EACCES):
			return nil, fmt.Errorf(
				"permission denied opening the systemd journal.\n"+
					"Try one of the following:\n"+
					"  - run this program with sudo / as root, or\n"+
					"  - add your user to the 'systemd-journal' group:\n"+
					"      sudo usermod -aG systemd-journal $USER\n"+
					"    then log out and back in for it to take effect\n"+
					"(underlying error: %v)", err)
		case errors.Is(err, syscall.ENOENT):
			return nil, fmt.Errorf(
				"no journal files found. Is systemd-journald running?\n"+
					"  systemctl status systemd-journald\n"+
					"(underlying error: %v)", err)
		default:
			return nil, fmt.Errorf("failed to open the systemd journal: %w", err)
		}
	}
	return j, nil
}

// applyFilters configures server-side match filters on the journal handle.
// Priority filtering is intentionally not done here because the journal
// match system only supports equality, not "<=" comparisons; it is applied
// client-side in run() instead.
func applyFilters(j *sdjournal.Journal, cfg config) error {
	if cfg.kernel {
		if err := j.AddMatch("_TRANSPORT=kernel"); err != nil {
			return fmt.Errorf("failed to add kernel match: %w", err)
		}
	}

	if len(cfg.units) > 0 {
		for i, u := range cfg.units {
			if i > 0 {
				if err := j.AddDisjunction(); err != nil {
					return fmt.Errorf("failed to add unit disjunction: %w", err)
				}
			}
			if err := j.AddMatch("_SYSTEMD_UNIT=" + u); err != nil {
				return fmt.Errorf("failed to add unit match for %q: %w", u, err)
			}
		}
	}

	if cfg.thisBoot {
		bootID, err := currentBootID()
		if err != nil {
			return fmt.Errorf("failed to determine current boot ID: %w", err)
		}
		if len(cfg.units) > 0 || cfg.kernel {
			// Close the preceding OR group so the boot filter ANDs with it.
			if err := j.AddConjunction(); err != nil {
				return fmt.Errorf("failed to add conjunction before boot filter: %w", err)
			}
		}
		if err := j.AddMatch("_BOOT_ID=" + bootID); err != nil {
			return fmt.Errorf("failed to add boot filter: %w", err)
		}
	}

	return nil
}

// currentBootID reads the running kernel's boot ID and returns it in the
// dash-free hex form the journal stores in the _BOOT_ID field.
func currentBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	id := strings.ReplaceAll(strings.TrimSpace(string(data)), "-", "")
	if id == "" {
		return "", errors.New("empty boot id")
	}
	return id, nil
}

// run seeks to the last N matching entries, prints them, and then either
// exits or follows the journal for new entries until ctx is cancelled.
func run(ctx context.Context, j *sdjournal.Journal, cfg config) error {
	if err := seekInitialWindow(j, cfg.numEntries); err != nil {
		return fmt.Errorf("failed to seek journal: %w", err)
	}

	if cfg.follow {
		fmt.Fprintln(os.Stderr, "-- following journal, press Ctrl-C to exit --")
	}

	const waitTimeout = 1 * time.Second

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		n, err := j.Next()
		if err != nil {
			return fmt.Errorf("failed to advance journal: %w", err)
		}

		if n == 0 {
			if !cfg.follow {
				return nil
			}
			// Wait briefly for new entries; re-check ctx afterwards so
			// Ctrl-C is handled promptly instead of blocking indefinitely.
			j.Wait(waitTimeout)
			continue
		}

		entry, err := j.GetEntry()
		if err != nil {
			fmt.Fprintln(os.Stderr, "warning: failed to read entry:", err)
			continue
		}

		if cfg.priority >= 0 && !passesPriority(entry, cfg.priority) {
			continue
		}

		printEntry(entry)
	}
}

// seekInitialWindow positions the read cursor so the next `n` calls to
// Next() will return the last n matching entries currently in the journal.
func seekInitialWindow(j *sdjournal.Journal, n int) error {
	if err := j.SeekTail(); err != nil {
		return err
	}
	// SeekTail() positions just past the last entry; step back by n so
	// that Next() will yield the last n entries in chronological order.
	if _, err := j.PreviousSkip(uint64(n)); err != nil {
		return err
	}
	return nil
}

func passesPriority(entry *sdjournal.JournalEntry, threshold int) bool {
	raw, ok := entry.Fields["PRIORITY"]
	if !ok {
		return true // no priority field, don't filter it out
	}
	p, err := strconv.Atoi(raw)
	if err != nil {
		return true
	}
	return p <= threshold
}

func printEntry(entry *sdjournal.JournalEntry) {
	ts := time.UnixMicro(int64(entry.RealtimeTimestamp))

	source := entry.Fields["_SYSTEMD_UNIT"]
	if source == "" {
		source = entry.Fields["SYSLOG_IDENTIFIER"]
	}
	if source == "" {
		if entry.Fields["_TRANSPORT"] == "kernel" {
			source = "kernel"
		} else {
			source = "-"
		}
	}

	msg := entry.Fields["MESSAGE"]

	fmt.Printf("%s %-20s %s\n", ts.Format("2006-01-02T15:04:05.000"), source, msg)
}
