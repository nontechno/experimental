#!/bin/bash

TIMEOUT=10

read -t "$TIMEOUT" -n 1 -p "Continue? (y/n): " answer

if [ $? -gt 128 ]; then
    echo "Timed out waiting for input. Exiting."
    exit 1
fi

case "$answer" in
    [Yy]*) echo "Confirmed, proceeding..." ;;
    [Nn]*) echo "Cancelled."; exit 0 ;;
    *) echo "Unrecognized input. Exiting."; exit 1 ;;
esac
