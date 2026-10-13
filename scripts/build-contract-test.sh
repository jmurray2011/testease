#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"

makefile=$(sed -n '1,120p' Makefile)

printf '%s\n' "$makefile" | grep -F 'TOOLS_DIR' >/dev/null
printf '%s\n' "$makefile" | grep -F 'GOFUMPT_VERSION' >/dev/null
printf '%s\n' "$makefile" | grep -F 'GOLANGCI_LINT_VERSION' >/dev/null
printf '%s\n' "$makefile" | grep -F 'GOVULNCHECK_VERSION' >/dev/null
printf '%s\n' "$makefile" | grep -F 'validate: test-contract tools' >/dev/null
printf '%s\n' "$makefile" | grep -E 'GOBIN=.*\$\(TOOLS_DIR\).*go install' >/dev/null

test -f .github/workflows/validate.yml
grep -F 'go-version-file: go.mod' .github/workflows/validate.yml >/dev/null
grep -F 'make validate' .github/workflows/validate.yml >/dev/null

printf '%s\n' 'build contract: pass'
