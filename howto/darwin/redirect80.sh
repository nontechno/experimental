#!/bin/sh
# Redirects localhost:80 -> localhost:8080 on macOS.
# Ephemeral: does NOT persist across reboots. Run again after each reboot.
#
# Usage: sudo ./redirect80.sh [target-port]
# Default target port is 8080.

set -e

if [ "$(id -u)" -ne 0 ]; then
  echo "Error: must be run with sudo." >&2
  exit 1
fi

TARGET_PORT="${1:-8080}"

echo "==> Redirecting 127.0.0.1:80 -> 127.0.0.1:${TARGET_PORT}"

echo "rdr pass inet proto tcp from any to 127.0.0.1 port 80 -> 127.0.0.1 port ${TARGET_PORT}" | pfctl -ef -

echo ""
echo "Done. Verify with: sudo pfctl -s nat"
echo "This is ephemeral — re-run this script after every reboot."
