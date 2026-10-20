#!/bin/sh
set -eu
file=/tmp/testease-smoke-payload.bin
digest=$(sha256sum "$file" | cut -d ' ' -f1)
size=$(wc -c < "$file" | tr -d ' ')
printf 'fact\tmedia.sha256\t%s\n' "$digest" >&3
printf 'fact\tmedia.bytes\t%s\n' "$size" >&3
printf '%s\n' "$digest" > /tmp/testease-smoke-media-echo.txt
