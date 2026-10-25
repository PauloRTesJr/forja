.DEFAULT_GOAL := help
SHELL := /bin/bash
# One version for the app, the core and the engine; `make version V=X.Y.Z` changes it.
VERSION ?= $(shell cat VERSION)
EXE := $(if $(filter Windows_NT,$(OS)),.exe)
GIT_CLIFF := 2.14.2

help: ## List targets
	@grep -E '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

setup: ## Install every toolchain dependency (uv, Go modules, pnpm)
	cd engine && uv sync
	cd core && go mod download
	cd apps/desktop && pnpm install

core: ## Build the forja binary into core/bin
	cd core && go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/forja$(EXE) ./cmd/forja

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
	cd engine && uv run ruff check . ../templates && uv run ruff format --check . ../templates && uv run mypy src families/birefnet/src families/triposg/src families/hunyuan3d2/src sdk/src ../templates/adapter/src && uv run pytest -q
	cd core && gofmt -l . | (! grep .) && go vet ./... && go test $$([ "$$(go env CGO_ENABLED)" = 1 ] && echo -race) ./...
	cd apps/desktop && pnpm lint && pnpm typecheck && pnpm test

integration: core ## Generate a GLB end to end with the stub models on the CPU (the CI integration test)
	bash scripts/e2e.sh generate

licenses: ## Fail on strong copyleft in what ships: the core's Go modules, the engine's and the app's packages
	cd core && GOBIN="$(CURDIR)/core/bin/tools" go install github.com/google/go-licenses/v2@v2.0.1
	cd core && for os in linux darwin windows; do echo "go-licenses: GOOS=$$os"; \
		GOOS=$$os GOROOT="$$(go env GOROOT)" bin/tools/go-licenses check ./... --ignore forja \
			--disallowed_types=forbidden,restricted,unknown || exit 1; done
	cd engine && uv run python ../scripts/licenses.py python
	cd apps/desktop && pnpm licenses list --prod --json | uv run --project ../../engine python ../../scripts/licenses.py npm

version: ## Print the version; `make version V=X.Y.Z` writes it into the app, the core and the engine
	@if [ -z "$(V)" ]; then cat VERSION; else uv run --no-project python scripts/version.py "$(V)" && cd engine && uv lock --quiet; fi

changelog: ## Regenerate CHANGELOG.md from the Conventional Commits (cliff.toml)
	uv tool run --from git-cliff==$(GIT_CLIFF) git-cliff --output CHANGELOG.md

notes: ## Print the newest tag's release notes; a stable release also lists its release candidates' changes
	@uv tool run --from git-cliff==$(GIT_CLIFF) git-cliff --latest --strip all \
		$(if $(findstring -,$(VERSION)),,--ignore-tags 'v.*-(alpha|beta|rc)\.')

dist: core ## Build this OS's desktop installers into apps/desktop/release (unsigned without a certificate)
ifeq ($(shell uname -s),Darwin)
	$(MAKE) core-all
	cd apps/desktop && pnpm build && pnpm dist:mac
else
	cd apps/desktop && pnpm build && pnpm exec electron-builder --publish never
endif

hooks: ## Optional: run the fast checks before each commit (.pre-commit-config.yaml)
	uv tool run pre-commit install

.PHONY: integration licenses version changelog notes dist hooks
.PHONY: help setup core core-all engine desktop test check
