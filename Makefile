# Variable docs go on the line above: an inline comment adds a trailing space.
## Tag `make release-check` validates, e.g. v0.1.0
TAG ?=
## Where `make install` puts the binary
PREFIX ?= $(HOME)/.local

# Without pipefail a failing command piped into another reports success.
SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
export TZ := UTC
export LANG := C.UTF-8

# Tools live in their own modfiles to keep their deps out of go.mod.
LINT := go tool -modfile=tools/golangci-lint.mod golangci-lint
VULN := go tool -modfile=tools/govulncheck.mod govulncheck
LEAKS := go tool -modfile=tools/gitleaks.mod gitleaks

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN := bin/beszelscope

.DEFAULT_GOAL := help
.PHONY: help check lint fmt test coverage vuln tidy-check secrets build install release-check fixtures clean

help: ## Show this help
	@awk 'BEGIN { FS = ":.*## " } \
		/^[a-z-]+:.*## / { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST) \
		| { printf "\033[1mUsage:\033[0m make \033[36m<target>\033[0m [\033[33mVAR\033[0m=value]\n\n\033[1mTargets:\033[0m\n"; cat; }
	@awk 'BEGIN { FS = " \\?= ?" } \
		/^## / { doc = substr($$0, 4); next } \
		/^[A-Z_]+ \?=/ && doc { printf "  \033[33m%-14s\033[0m %s \033[2m(default: %s)\033[0m\n", $$1, doc, ($$2 == "" ? "none" : $$2) } \
		{ doc = "" }' $(MAKEFILE_LIST) \
		| { printf "\n\033[1mVariables:\033[0m\n"; cat; }

check: tidy-check lint test vuln secrets ## The gate: tidy, lint, race tests, govulncheck, gitleaks

lint: ## Lint and format-check with golangci-lint
	$(LINT) run ./...

fmt: ## Apply gofumpt and goimports
	$(LINT) fmt ./...

test: ## Race tests in shuffled order, never cached
	go test -race -shuffle=on -count=1 ./...

coverage: ## Per-function coverage; writes coverage.out (report only, no gate)
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

vuln: ## Reachable vulnerabilities in code and dependencies
	$(VULN) ./...

tidy-check: ## Fail if go.mod or go.sum would change under go mod tidy
	go mod tidy -diff

secrets: ## Scan git history and the working tree for secrets
	$(LEAKS) git --redact --no-banner .
	$(LEAKS) dir --redact --no-banner .

build: ## Build bin/beszelscope with the git version stamped in
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/beszelscope

install: build ## Install the binary to $(PREFIX)/bin
	install -d "$(PREFIX)/bin"
	install -m 0755 $(BIN) "$(PREFIX)/bin/beszelscope"

release-check: ## Check TAG is vX.Y.Z, sits on HEAD and has release notes
	@test -n "$(TAG)" || { echo "set TAG, e.g. make release-check TAG=v0.1.0" >&2; exit 1; }
	@[[ "$(TAG)" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$$ ]] || { echo "$(TAG) is not vX.Y.Z" >&2; exit 1; }
	@test "$$(git rev-list -n1 "$(TAG)" 2>/dev/null)" = "$$(git rev-parse HEAD)" || { echo "$(TAG) does not point at HEAD" >&2; exit 1; }
	@test -f "docs/releases/$(TAG).md" || { echo "docs/releases/$(TAG).md is missing" >&2; exit 1; }
	@echo "$(TAG) is on HEAD and has notes"

fixtures: ## Capture and sanitise hub responses into testdata (arrives in step 3)
	@echo "not implemented yet" >&2; exit 1

clean: ## Remove build and coverage output
	rm -rf bin coverage.out
