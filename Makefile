.PHONY: validate test-contract tools fmt vet lint test vuln build install

# Keep the validation toolchain beside the checkout. A clean clone should not
# depend on whichever versions happen to be installed globally.
TOOLS_DIR ?= $(CURDIR)/.tools
GOFUMPT_VERSION ?= v0.10.0
GOLANGCI_LINT_VERSION ?= v2.13.2
GOVULNCHECK_VERSION ?= v1.6.0

GOFUMPT := $(TOOLS_DIR)/gofumpt
GOLANGCI_LINT := $(TOOLS_DIR)/golangci-lint
GOVULNCHECK := $(TOOLS_DIR)/govulncheck
TOOLS := $(GOFUMPT) $(GOLANGCI_LINT) $(GOVULNCHECK)

# STAMPED, because the record names the binary that took the measurement and
# an unstamped one names "dev". --dirty is not decoration: a reading taken with
# an uncommitted binary is not reproducible from any commit, and the record has
# to be able to say so.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
STAMP   := -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# The gate. Nothing is done until this is green.
validate: test-contract tools fmt vet lint test vuln

test-contract:
	sh scripts/build-contract-test.sh

tools: $(TOOLS)

$(TOOLS_DIR):
	mkdir -p "$@"

$(GOFUMPT): | $(TOOLS_DIR)
	GOBIN="$(TOOLS_DIR)" go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)

$(GOLANGCI_LINT): | $(TOOLS_DIR)
	GOBIN="$(TOOLS_DIR)" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOVULNCHECK): | $(TOOLS_DIR)
	GOBIN="$(TOOLS_DIR)" go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

fmt: tools
	$(GOFUMPT) -l -d .
	@test -z "$$($(GOFUMPT) -l .)" || { echo "gofumpt: files need formatting"; exit 1; }

vet:
	go vet ./...

lint: tools
	$(GOLANGCI_LINT) run

test:
	go test -race ./...

vuln: tools
	$(GOVULNCHECK) ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(STAMP)" -o testease ./cmd/testease

# Onto PATH, so that `testease` works from the directory a scenario tree or a
# runs directory is in rather than only from this one.
install:
	CGO_ENABLED=0 go install -trimpath -ldflags "$(STAMP)" ./cmd/testease
