#!/bin/sh
# Redirects port 80 -> 8080 on macOS (loopback), persists across reboots.
# Usage: sudo ./port80-redirect.sh [target-port]
# Default target port is 8080.

set -e

TARGET_PORT="${1:-8080}"
ANCHOR_NAME="dev-redirect"
ANCHOR_FILE="/etc/pf.anchors/${ANCHOR_NAME}"
PF_CONF="/etc/pf.conf"
LAUNCH_DAEMON="/Library/LaunchDaemons/com.user.pf-redirect.plist"

if [ "$(id -u)" -ne 0 ]; then
  echo "Error: must be run as root (sudo)." >&2
  exit 1
fi

echo "==> Writing pf anchor to ${ANCHOR_FILE}"
cat > "${ANCHOR_FILE}" <<EOF
rdr pass inet proto tcp from any to 127.0.0.1 port 80 -> 127.0.0.1 port ${TARGET_PORT}
EOF

echo "==> Updating ${PF_CONF}"
# Remove any previous dev-redirect lines to avoid duplicates
sed -i '' '/dev-redirect/d' "${PF_CONF}"
# Append anchor load directives
cat >> "${PF_CONF}" <<EOF

# dev-redirect: port 80 -> ${TARGET_PORT}
rdr-anchor "${ANCHOR_NAME}"
load anchor "${ANCHOR_NAME}" from "${ANCHOR_FILE}"
EOF

echo "==> Writing LaunchDaemon to ${LAUNCH_DAEMON}"
cat > "${LAUNCH_DAEMON}" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.user.pf-redirect</string>
  <key>ProgramArguments</key>
  <array>
    <string>/sbin/pfctl</string>
    <string>-ef</string>
    <string>/etc/pf.conf</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
</dict>
</plist>
EOF

echo "==> Loading LaunchDaemon"
launchctl unload "${LAUNCH_DAEMON}" 2>/dev/null || true
launchctl load "${LAUNCH_DAEMON}"

echo "==> Applying pf rules now"
pfctl -ef /etc/pf.conf

echo ""
echo "Done. Port 80 -> ${TARGET_PORT} on localhost."
echo "To verify: sudo pfctl -s nat"
