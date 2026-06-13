#!/usr/bin/env bash

# Finds files matching a wildcard pattern in the current directory and runs a
# script for each match, passing the filename with its extension stripped.
#
# Usage: $0 [-r] <wildcard> <script>
#
#   -r  Recurse into subdirectories. When set, the relative path (including
#       any intermediate directories) is preserved and passed to the script.
#       Without this flag, only the current directory is searched.
#
#   <wildcard>  Glob pattern to match files against (e.g. '*.mmd').
#               Quote the pattern to prevent shell expansion.
#
#   <script>    Path to the script to invoke for each matched file.
#               It receives one argument: the matched file's path with the
#               last extension removed (e.g. 'subdir/foo.bar' for 'subdir/foo.bar.mmd').
#
# Examples:
#   $0 '*.mmd' ./render.sh
#   $0 -r '*.mmd' ./render.sh

set -euo pipefail

usage() {
    echo "Usage: $0 [-r] <wildcard> <script>" >&2
    echo "Example: $0 -r '*.mmd' ./render.sh" >&2
    exit 1
}

recursive=false

while getopts "r" opt; do
    case "$opt" in
        r) recursive=true ;;
        *) usage ;;
    esac
done
shift $((OPTIND - 1))

[[ $# -lt 2 ]] && usage

pattern="$1"
script="$2"

if "$recursive"; then
    maxdepth_arg=()
else
    maxdepth_arg=(-maxdepth 1)
fi

find . "${maxdepth_arg[@]}" -name "$pattern" -type f | while read -r file; do
    # strip leading "./"
    rel="${file#./}"
    # strip last extension
    stem="${rel%.*}"
    "$script" "$stem"
done
