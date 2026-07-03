#!/bin/sh

is_oracle_linux() {
    if [ -f /etc/os-release ]; then
        . /etc/os-release
        [ "$ID" = "ol" ]
        return $?
    fi
    return 1
}

if is_oracle_linux; then
    echo "This is Oracle Linux"
else
    echo "Not Oracle Linux"
fi
