.DEFAULT_GOAL := help
SHELL := /bin/bash
# One version for the app, the core and the engine; `make version V=X.Y.Z` changes it.
VERSION ?= $(shell cat VERSION)
EXE := $(if $(filter Windows_NT,$(OS)),.exe)

help: ## List targets
	@grep -E '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

setup: ## Install every toolchain dependency (uv, Go modules, pnpm)
	cd engine && uv sync
	cd core && go mod download
	cd apps/desktop && pnpm install

core: ## Build the forja binary into core/bin
	cd core && go build -ldflags "-X main.version=$(VERSION)" -o bin/forja$(EXE) ./cmd/forja

core-all: ## Cross-compile forja for six OS/arch targets
	cd core && for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do \
		os=$${t%/*}; arch=$${t#*/}; ext=$$([ $$os = windows ] && echo .exe); \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
			-o bin/forja-$$os-$$arch$$ext ./cmd/forja || exit 1; done

engine: ## Run a local engine on port 7860 against ./.forja/store
	cd engine && uv run forja-engine serve --port 7860 --store ../.forja/store

desktop: core ## Run the desktop app in development mode
	cd apps/desktop && pnpm dev

test: ## Run every test suite
	cd engine && uv run pytest -q
	cd core && go test ./...
	cd apps/desktop && pnpm test

check: ## Lint, type-check and test everything (what CI runs)
	cd engine && uv run ruff check . && uv run ruff format --check . && uv run mypy src families/birefnet/src families/triposg/src families/hunyuan3d2/src && uv run pytest -q
	cd core && gofmt -l . | (! grep .) && go vet ./... && go test $$([ "$$(go env CGO_ENABLED)" = 1 ] && echo -race) ./...
	cd apps/desktop && pnpm lint && pnpm typecheck && pnpm test

version: ## Print the version; `make version V=X.Y.Z` writes it into the app, the core and the engine
	@if [ -z "$(V)" ]; then cat VERSION; else uv run --no-project python scripts/version.py "$(V)" && cd engine && uv lock --quiet; fi

.PHONY: version
.PHONY: help setup core core-all engine desktop test check
