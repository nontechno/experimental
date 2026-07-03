#!/usr/bin/env bash

MISSING="n/a"

get_version() {
    local app="$1"
    local flag="$2"
    local match="$3"  # optional
    local title="$4"  # optional

    if [ -z "$title" ]; then
	title="$app"
    fi

    # Check if app exists
    if ! command -v "$app" &>/dev/null; then
        #echo "-----------------------missing ${app}"
        echo "${title}: ${MISSING}"
        return
    fi

    local output
    output=$("$app" "$flag" 2>/dev/null)
    #echo "-----------------------output: ${output}"

    # If match string provided, grep for it; otherwise use first line
    local line
    if [ -n "$match" ]; then
        line=$(echo "$output" | grep -F "$match" | head -1)
        if [ -z "$line" ]; then
            # echo "-----------------------match [$match]: not found in [$output]"
            echo "${title}: ${MISSING}"
            return
        fi
	line="${line#"$match"}"	# remove prefix
    else
        line=$(echo "$output" | head -1)
    fi

    # Extract version number from the line
    local version
    # version=$(echo "$line" | grep -oE '\d+\.\d+[\.\d]*' | head -1)
    version=$(echo "$line" | head -1)

    local result
    result="${version:-${MISSING}}"
    if [ "$result" != "${MISSING}" ]; then
	result='\033[7m${result}\033[0m\n'
    fi
    # echo "${title}: ${version:-${MISSING}}"
    # printf '%s: %s\n' "${title}" "${result}"
    # printf 'normal text \e[7minverted part\e[0m normal text again\n'
    printf '%s\e[7m%s\e[0m\n' "${title}" "${version}"
}

# First line, extract semver
get_version "git" "--version"	"git version "		"git        "
get_version "go" "version"	"go version "		"go         "
get_version "openssl" "version"	""			"OpenSSL    "
get_version "uname" "-v"	""			"kernel.ver "
get_version "uname" "-r"	""			"kernel.rel "

get_version "rustup" "-V"	"rustup "		"rustup     "
get_version "cargo" "-V"	"cargo "		"cargo      "
get_version "rustc" "-V"	"rustc "		"rustc      "

get_version "docker" "-v"	"Docker version "	"docker     "
get_version "node" "-v"		""			"node       "
get_version "npm" "-v"		""			"npm        "
get_version "python" "-V"	"Python "		"python     "

# App doesn't exist
get_version "nonexistent" "--version" "" "aaaa"

# Store in variable
GIT_VERSION=$(get_version "git" "--version")
