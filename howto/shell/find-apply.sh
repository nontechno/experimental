#!/usr/bin/env bash
set -euo pipefail

# Finds files matching a wildcard pattern in the current directory and runs a
# script for each match, passing the filename broken into components.
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
#               Receives three arguments:
#                 -n <name>  filename without extension and without directory
#                 -d <dir>   relative subdirectory path (empty string if none)
#                 -e <ext>   the stripped extension (without leading dot)
#
# Examples:
#   $0 '*.mmd' ./render.sh
#   $0 -r '*.mmd' ./render.sh

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

while IFS= read -r -d '' file; do
    # strip leading "./"
    rel="${file#./}"

    # split into directory and base filename
    dir="${rel%/*}"
    base="${rel##*/}"
    [[ "$dir" == "$base" ]] && dir=""   # no subdirectory

    # split base into stem and extension
    ext="${base##*.}"
    stem="${base%.*}"
    [[ "$ext" == "$base" ]] && ext=""   # no extension

    "$script" -n "$stem" -d "$dir" -e "$ext"
done < <(find . "${maxdepth_arg[@]}" -name "$pattern" -type f -print0)