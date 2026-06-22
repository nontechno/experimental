#!/usr/bin/env bash
unset_matching() {
    local word="${1:?Usage: unset_matching <word>}"
    local pattern="${word,,}"  # lowercase the search word

    local -a matches=()
    while IFS= read -r var; do
        if [[ "${var,,}" == *"$pattern"* ]]; then
            matches+=("$var")
        fi
    done < <(compgen -e)  # list only exported (env) variable names

    if [[ ${#matches[@]} -eq 0 ]]; then
        echo "No env variables matching '$word' found."
        return 0
    fi

    echo "Unsetting ${#matches[@]} variable(s):"
    for var in "${matches[@]}"; do
        echo "  $var"
        unset "$var"
    done
}

unset_matching "$@"
