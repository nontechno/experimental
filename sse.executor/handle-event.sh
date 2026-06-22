#!/usr/bin/env bash
# handle-event.sh — called by sse.executor when an event fires.
#
# Arguments:
#   $1  — event data (raw string)
#
# Environment:
#   SSE_EVENT_DATA  — same as $1
#   SSE_EVENT_TYPE  — e.g. "deploy", "rollback", "message"
#   SSE_EVENT_ID    — SSE event id field (may be empty)

set -euo pipefail

EVENT_DATA="${1:-}"
EVENT_TYPE="${SSE_EVENT_TYPE:-unknown}"
EVENT_ID="${SSE_EVENT_ID:-}"

echo "[$(date -u +%FT%TZ)] type=${EVENT_TYPE} id=${EVENT_ID}"
echo "data: ${EVENT_DATA}"

# Example: parse JSON payload and act on it
# IMAGE=$(echo "${EVENT_DATA}" | jq -r '.image')
# kubectl set image deployment/myapp app="${IMAGE}"

exit 0
