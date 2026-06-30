#!/usr/bin/env bash

get_version() {
    local app="$1"
    local flag="$2"
    local match="$3"  # optional

    # Check if app exists
    if ! command -v "$app" &>/dev/null; then
        echo "n/a"
        return
    fi

    local output
    output=$("$app" "$flag" 2>/dev/null)

    # If match string provided, grep for it; otherwise use first line
    local line
    if [ -n "$match" ]; then
        line=$(echo "$output" | grep -F "$match" | head -1)
        if [ -z "$line" ]; then
            echo "n/a"
            return
        fi
    else
        line=$(echo "$output" | head -1)
    fi

    # Extract version number from the line
    local version
    version=$(echo "$line" | grep -oE '\d+\.\d+[\.\d]*' | head -1)

    echo "${version:-n/a}"
}

# First line, extract semver
get_version "git" "--version"
# git version 2.43.0 -> "2.43.0"

# Match specific line in multiline output
get_version "openssl" "version" "OpenSSL"
# OpenSSL 3.0.2 15 Mar 2022 -> "3.0.2"

# App doesn't exist
get_version "nonexistent" "--version"
# -> "n/a"

# Store in variable
GIT_VERSION=$(get_version "git" "--version")
