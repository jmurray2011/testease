#!/bin/sh
set -eu
file=/tmp/testease-smoke-large-artifact.bin
head -c 70000 /dev/zero > "$file"
size=$(wc -c < "$file" | tr -d ' ')
digest=$(sha256sum "$file" | cut -d ' ' -f1)
printf 'fact\tartifact.size\t%s\n' "$size" >&3
printf 'fact\tartifact.sha256\t%s\n' "$digest" >&3
