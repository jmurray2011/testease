#!/bin/sh
set -eu

fact() {
    printf 'fact\t%s\t%s\n' "$1" "$2" >&3
}

no_reading() {
    printf 'no-reading\t%s\t%s\n' "$1" "$2" >&3
}

fact example.value "replace-me"
printf '%s\n' 'probe report goes to stdout'
exit 0
