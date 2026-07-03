# sys.journal

A small Go CLI that reads the systemd journal via
`github.com/coreos/go-systemd/v22/sdjournal` (native cgo bindings to
libsystemd — no shelling out to `journalctl`).

## Features

- Checks the host is actually running systemd (`/run/systemd/system`)
  before doing anything else, and reports a clear error if not.
- Opens the journal and reports a clear, actionable error if it fails
  due to insufficient privileges (not root / not in `systemd-journal`
  group) instead of a raw cgo error.
- Prints the last N entries, then optionally follows (`-f`) for new
  ones, like `journalctl -f`.
- Filters: unit(s) (`-u`), kernel-only (`-k`), current boot only
  (`-b`), minimum severity (`-p`).
- Handles Ctrl-C (SIGINT) and SIGTERM cleanly via
  `signal.NotifyContext`, closing the journal handle before exiting.

## Build

Requires cgo and the libsystemd development headers:

```bash
# Debian/Ubuntu
sudo apt install libsystemd-dev

# Fedora/RHEL
sudo dnf install systemd-devel
```

Then:

```bash
cd sys.journal
go mod tidy   # fetches github.com/coreos/go-systemd/v22
go build -o sys.journal .
```

## Run

```bash
./sys.journal                          # last 10 entries, then exit
./sys.journal -f                       # last 10 entries, then follow
./sys.journal -u sshd.service -f       # follow one unit
./sys.journal -u sshd.service,cron -f  # follow multiple units (OR'd)
./sys.journal -k -f                    # kernel messages only
./sys.journal -b -p err -f             # current boot, err severity or worse
./sys.journal -n 50                    # last 50 entries, then exit
```

Reading the journal typically requires root, or membership in the
`systemd-journal` group:

```bash
sudo usermod -aG systemd-journal $USER
# then log out and back in
```

If you run it without sufficient privileges, or on a non-systemd host,
the program explains exactly what's wrong and how to fix it rather than
printing a raw library error.
