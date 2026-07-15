#!/usr/bin/env bash
# rename-prefix.sh — bulk rename files matching an old prefix to a new prefix
#
# Usage: ./rename-prefix.sh OLD_PREFIX NEW_PREFIX [DIRECTORY] [--dry-run]
#
# Example:
#   ./rename-prefix.sh "aaaaaaaaaaaaaaaaaaaaabbbbbbb-" "ab-" .
#   ./rename-prefix.sh "aaaaaaaaaaaaaaaaaaaaabbbbbbb-" "ab-" . --dry-run

set -euo pipefail

if [[ $# -lt 2 ]]; then
    echo "Usage: $0 OLD_PREFIX NEW_PREFIX [DIRECTORY] [--dry-run]" >&2
    exit 1
fi

OLD_PREFIX="$1"
NEW_PREFIX="$2"
DIR="${3:-.}"
DRY_RUN=false

for arg in "$@"; do
    [[ "$arg" == "--dry-run" ]] && DRY_RUN=true
done

shopt -s nullglob

count=0
for file in "$DIR"/"${OLD_PREFIX}"*; do
    base="$(basename "$file")"
    newbase="${NEW_PREFIX}${base#"$OLD_PREFIX"}"
    newpath="$(dirname "$file")/$newbase"

    if [[ -e "$newpath" ]]; then
        echo "SKIP (target exists): $base -> $newbase" >&2
        continue
    fi

    if $DRY_RUN; then
        echo "[dry-run] $base -> $newbase"
    else
        mv -- "$file" "$newpath"
        echo "$base -> $newbase"
    fi
    ((count++)) || true
done

echo "Done. $count file(s) $($DRY_RUN && echo would be renamed || echo renamed)."
