.PHONY: deps build test test-backend test-ui test-bridge bridge bridge-deps typecheck vet fmt check-format \
	bundle package package-host package-file clean

# BIN and VERSION must match manifest.yaml's id and version.
BIN := bin/kandev-plugin-mindmap
VERSION := 0.3.3
STAGE := .build/stage
PKG_OUT := $(notdir $(BIN))-$(VERSION).tar.gz

# Sibling checkout of the kandev monorepo at the .kandev-sdk-ref commit.
# plugin-pack runs inside it so its dependencies resolve against kandev's go.sum.
KANDEV_SDK := ../kandev-sdk/apps/backend

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

## Frontend dev deps (plugin-sdk, esbuild, typescript, vitest).
deps:
	@test -d node_modules || npm install --no-audit --no-fund

## The binary embeds server/web/dist (the standalone guest app), so the UI
## bundles are built first.
build: bundle
	mkdir -p bin
	CGO_ENABLED=0 go build -o $(BIN) ./server

test: test-backend typecheck test-ui test-bridge

## -race, plus the 1,000-node load test without -race (the transpiled SQLite
## is ~25x slower under the race detector, so its 1 s budget is enforced here).
test-backend:
	go test -race ./server/... ./contract/...
	go test -count=1 -run 'TestLoad' ./server/store/

test-ui: deps
	npm run test:ui

typecheck: deps
	npm run typecheck

vet:
	go vet ./server/... ./contract/...

fmt:
	gofmt -w server contract

check-format:
	@out="$$(gofmt -l server contract)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

## ui/bundle.js (in-Kandev) and, once Phase 1b adds `build:web`, the
## standalone guest app into server/web/dist/ (embedded by go:embed).
bundle: deps
	npm run build:ui
	npm run build:web --if-present

define stage_common
	rm -rf $(STAGE)
	mkdir -p $(STAGE)/server $(STAGE)/ui
	cp manifest.yaml $(STAGE)/manifest.yaml
	cp ui/bundle.js ui/plugin.css $(STAGE)/ui/
endef

## All-platform archive (release artifact). Pure Go (CGO_ENABLED=0) so it cross-compiles.
package: bundle
	$(stage_common)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=""; [ "$$os" = windows ] && ext=".exe"; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -o $(STAGE)/server/plugin-$$os-$$arch$$ext ./server; \
	done
	cd $(KANDEV_SDK) && go run ./cmd/plugin-pack -dir $(CURDIR)/$(STAGE) -out $(CURDIR)/$(PKG_OUT)
	rm -rf $(STAGE)
	@echo "Wrote $(PKG_OUT)"

## Host-platform-only archive (fast local install / CI smoke).
package-host: bundle
	$(stage_common)
	CGO_ENABLED=0 go build -trimpath -o $(STAGE)/server/plugin-$$(go env GOOS)-$$(go env GOARCH)$$(go env GOEXE) ./server
	cd $(KANDEV_SDK) && go run ./cmd/plugin-pack -dir $(CURDIR)/$(STAGE) -out $(CURDIR)/$(PKG_OUT) -platform-only
	rm -rf $(STAGE)
	@echo "Wrote $(PKG_OUT)"

package-file:
	@printf '%s\n' "$(PKG_OUT)"

clean:
	rm -rf bin .build $(notdir $(BIN))-*.tar.gz

## Agent bridge (bridge/): `bridge/dist/kandev-mindmap-mcp.mjs` (stdio MCP server) and
## `bridge/dist/pi-extension.mjs` (Pi extension). See bridge/README.md.
bridge-deps:
	@test -d bridge/node_modules || npm install --prefix bridge --no-audit --no-fund

bridge: bridge-deps
	npm run typecheck --prefix bridge
	npm run check:prompt --prefix bridge
	npm run build --prefix bridge

test-bridge: bridge-deps
	npm run typecheck --prefix bridge
	npm run check:prompt --prefix bridge
	npm test --prefix bridge
