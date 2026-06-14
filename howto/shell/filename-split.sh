#!/usr/bin/env bash

full_path="/aaa/bbb/ccc.ddd"

dir=$(dirname "$full_path")
base=$(basename "$full_path")
ext="${base##*.}"
name="${base%.*}"

# Handle no-extension case (no dot, or dotfile like ".bashrc")
if [[ "$base" == "$ext" || "$base" == ".$ext" ]]; then
    ext=""
fi

printf 'dir:  %s\n' "$dir"
printf 'name: %s\n' "$name"
printf 'ext:  %s\n' "$ext"
