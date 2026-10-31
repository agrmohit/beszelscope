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
ACTIONLINT := go tool -modfile=tools/actionlint.mod actionlint
# zizmor is a Python package; CI runners have pipx but not uv.
ZIZMOR_VERSION := 1.31.1
ZIZMOR := $(if $(shell command -v uvx),uvx zizmor@$(ZIZMOR_VERSION),pipx run zizmor==$(ZIZMOR_VERSION))

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN := bin/beszelscope
PLATFORMS := darwin/arm64 linux/arm64 linux/amd64 windows/arm64 windows/amd64

.DEFAULT_GOAL := help
.PHONY: help check lint fmt test coverage vuln tidy-check secrets workflows build install dist release-check fixtures clean

help: ## Show this help
	@awk 'BEGIN { FS = ":.*## " } \
		/^[a-z-]+:.*## / { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST) \
		| { printf "\033[1mUsage:\033[0m make \033[36m<target>\033[0m [\033[33mVAR\033[0m=value]\n\n\033[1mTargets:\033[0m\n"; cat; }
	@awk 'BEGIN { FS = " \\?= ?" } \
		/^## / { doc = substr($$0, 4); next } \
		/^[A-Z_]+ \?=/ && doc { printf "  \033[33m%-14s\033[0m %s \033[2m(default: %s)\033[0m\n", $$1, doc, ($$2 == "" ? "none" : $$2) } \
		{ doc = "" }' $(MAKEFILE_LIST) \
		| { printf "\n\033[1mVariables:\033[0m\n"; cat; }

check: tidy-check lint test vuln secrets workflows ## The gate: tidy, lint, race tests, govulncheck, gitleaks, workflow lint

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

workflows: ## Lint GitHub workflows with actionlint and zizmor
	@# actionlint v1.7.12 predates `uses: $$/...` and `cache-mode`; drop the
	@# ignores once a release accepts them.
	$(ACTIONLINT) -ignore 'reusable workflow call "\$$/' -ignore 'unexpected key "cache-mode"'
	@# zizmor's online audits need a token, and it rejects an empty one.
	token="$${ZIZMOR_GITHUB_TOKEN:-$$(gh auth token 2>/dev/null || true)}"; \
		if [ -n "$$token" ]; then export ZIZMOR_GITHUB_TOKEN="$$token"; fi; \
		$(ZIZMOR) .github/workflows

build: ## Build bin/beszelscope with the git version stamped in
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/beszelscope

install: build ## Install the binary to $(PREFIX)/bin
	install -d "$(PREFIX)/bin"
	install -m 0755 $(BIN) "$(PREFIX)/bin/beszelscope"

dist: ## Build the release binaries and checksums.txt into dist/
	rm -rf dist
	mkdir -p dist
	for platform in $(PLATFORMS); do \
		os=$${platform%/*} arch=$${platform#*/}; \
		case $$os in windows) ext=.exe ;; *) ext= ;; esac; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
			-o "dist/beszelscope_$(VERSION)_$${os}_$${arch}$$ext" ./cmd/beszelscope; \
	done
	cd dist && shasum -a 256 beszelscope_* > checksums.txt

release-check: ## Check TAG is vX.Y.Z, sits on HEAD and has release notes
	@test -n "$(TAG)" || { echo "set TAG, e.g. make release-check TAG=v0.1.0" >&2; exit 1; }
	@[[ "$(TAG)" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$$ ]] || { echo "$(TAG) is not vX.Y.Z" >&2; exit 1; }
	@test "$$(git rev-list -n1 "$(TAG)" 2>/dev/null)" = "$$(git rev-parse HEAD)" || { echo "$(TAG) does not point at HEAD" >&2; exit 1; }
	@test -f "docs/releases/$(TAG).md" || { echo "docs/releases/$(TAG).md is missing" >&2; exit 1; }
	@echo "$(TAG) is on HEAD and has notes"

fixtures: ## Capture hub responses, pseudonymise them, write internal/hub/testdata/hub
	go run ./cmd/fixtures

clean: ## Remove build and coverage output
	rm -rf bin dist coverage.out
