#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 ]]; then
    echo "Usage: $0 <file>" >&2
    exit 1
fi

file=$1

if [[ ! -f $file ]]; then
    echo "Error: file not found: $file" >&2
    exit 1
fi

first_line=$(head -n 1 "$file")