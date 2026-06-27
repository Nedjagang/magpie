.PHONY: help build build-control-plane build-agent fetch-collector release-bundle \
        release-bundle-docker test test-pg lint tidy clean quickstart docker docker-up docker-down

GO              := go
GOLANGCI_LINT   := golangci-lint
PNPM            := pnpm
VERSION         ?= 1.0.0
# Upstream otelcol-contrib version shipped alongside the agent (see ADR 0008).
# Keep in sync with .github/workflows/release.yml.
OTELCOL_VERSION ?= 0.116.0

help: ## show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ── build ──────────────────────────────────────────────────────────────
build: build-control-plane build-agent ## build magpied + magpie-agent

build-control-plane: ## build the control plane binary
	cd control-plane && $(GO) build -ldflags="-X main.version=$(VERSION)" -o ../bin/magpied ./cmd/magpied

build-agent: ## build the agent supervisor binary
	cd agent && $(GO) build -ldflags="-X main.version=$(VERSION)" -o ../bin/magpie-agent ./cmd/magpie-agent

fetch-collector: ## download + verify upstream otelcol-contrib into ./bin (see ADR 0008)
	@mkdir -p bin
	@# Platform detection is done in-recipe (not via Make `ifneq`) because
	@# some Make builds on Windows don't carry conditionally-set variables
	@# into recipe expansion. Override by exporting OTELCOL_OS/ARCH/EXT.
	@V="$(OTELCOL_VERSION)"; \
	 os="$${OTELCOL_OS:-}"; arch="$${OTELCOL_ARCH:-}"; ext="$${OTELCOL_EXT-__unset__}"; \
	 if [ -z "$$os" ]; then \
	   u=$$(uname -s); \
	   case "$$u" in MINGW*|MSYS*|CYGWIN*|Windows_NT) os=windows ;; Darwin) os=darwin ;; *) os=$$(echo "$$u" | tr '[:upper:]' '[:lower:]') ;; esac; \
	 fi; \
	 if [ -z "$$arch" ]; then arch=$$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/'); fi; \
	 if [ "$$ext" = "__unset__" ]; then [ "$$os" = "windows" ] && ext=".exe" || ext=""; fi; \
	 base="otelcol-contrib_$${V}_$${os}_$${arch}"; \
	 url="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v$${V}"; \
	 sums="opentelemetry-collector-releases_otelcol-contrib_checksums.txt"; \
	 tmp=$$(mktemp -d); trap "rm -rf $$tmp" EXIT; \
	 echo "→ downloading $${base}.tar.gz"; \
	 curl -fsSL -o "$$tmp/$${base}.tar.gz" "$${url}/$${base}.tar.gz"; \
	 curl -fsSL -o "$$tmp/checksums.txt"   "$${url}/$${sums}"; \
	 expected=$$(grep "  $${base}.tar.gz$$" "$$tmp/checksums.txt" | awk '{print $$1}'); \
	 actual=$$(cd "$$tmp" && sha256sum "$${base}.tar.gz" | awk '{print $$1}'); \
	 if [ "$$expected" != "$$actual" ]; then echo "checksum mismatch: expected=$$expected actual=$$actual"; exit 1; fi; \
	 echo "→ verified sha256=$$actual"; \
	 tar -xzf "$$tmp/$${base}.tar.gz" -C "$$tmp"; \
	 cp "$$tmp/otelcol-contrib$${ext}" bin/; \
	 chmod +x "bin/otelcol-contrib$${ext}" 2>/dev/null || true; \
	 echo "→ placed bin/otelcol-contrib$${ext} (v$${V} $${os}/$${arch})"

# release-bundle produces the same ./releases/<os>-<arch>/ tree the CI
# release workflow generates, but locally — for ops who want to onboard
# hosts without waiting on a tag-driven build. Cross-builds magpie-agent
# for each target with `go build` (pure Go, no cgo) and downloads +
# verifies the matching upstream otelcol-contrib for each.
#
# The output path matches what the docker-compose mounts at /releases,
# so magpied picks up populated platforms immediately on its next
# Catalog() call (~5s, no restart needed).
release-bundle: ## build all-platforms release tree into ./releases (mounted into magpied)
	@rm -rf releases && mkdir -p releases
	@for spec in windows-amd64 linux-amd64 linux-arm64 darwin-arm64; do \
	   os=$${spec%-*}; arch=$${spec#*-}; ext=""; \
	   if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
	   dir="releases/$${spec}"; \
	   mkdir -p "$$dir"; \
	   echo "→ building magpie-agent for $${os}/$${arch}"; \
	   ( cd agent && GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
	       $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
	         -o "../$${dir}/magpie-agent$${ext}" ./cmd/magpie-agent ) || exit 1; \
	   echo "→ fetching otelcol-contrib v$(OTELCOL_VERSION) for $${os}/$${arch}"; \
	   base="otelcol-contrib_$(OTELCOL_VERSION)_$${os}_$${arch}"; \
	   url="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v$(OTELCOL_VERSION)"; \
	   sums="opentelemetry-collector-releases_otelcol-contrib_checksums.txt"; \
	   tmp=$$(mktemp -d); \
	   ( curl -fsSL -o "$$tmp/$${base}.tar.gz" "$${url}/$${base}.tar.gz" \
	     && curl -fsSL -o "$$tmp/checksums.txt" "$${url}/$${sums}" \
	     && expected=$$(grep "  $${base}.tar.gz$$" "$$tmp/checksums.txt" | awk '{print $$1}') \
	     && actual=$$(cd "$$tmp" && sha256sum "$${base}.tar.gz" | awk '{print $$1}') \
	     && [ "$$expected" = "$$actual" ] \
	     && echo "  verified sha256=$$actual" \
	     && tar -xzf "$$tmp/$${base}.tar.gz" -C "$$tmp" \
	     && cp "$$tmp/otelcol-contrib$${ext}" "$${dir}/" \
	   ) || { rm -rf "$$tmp"; echo "  failed for $${spec}"; exit 1; }; \
	   rm -rf "$$tmp"; \
	 done
	@echo
	@echo "→ ./releases populated. magpied picks it up on the next /api/v1/releases call:"
	@ls releases/

# release-bundle-docker is the same as release-bundle but runs the cross-
# build inside a Go container, so prod hosts that have Docker but not Go
# (the typical Aptean deploy box) can populate the releases dir without
# installing a Go toolchain. Volume-mounts the repo + ./releases so the
# output lands directly where docker-compose expects it; magpied picks
# it up on its next Catalog() call without a restart.
release-bundle-docker: ## same as release-bundle, run inside golang:1.25-alpine (no Go on host)
	@mkdir -p releases
	docker run --rm \
	  -v "$$(pwd):/src" \
	  -v "$$(pwd)/releases:/out" \
	  -w /src \
	  -e OTELCOL_VERSION=$(OTELCOL_VERSION) \
	  -e VERSION=$(VERSION) \
	  golang:1.25-alpine \
	  sh -c 'set -eu; \
	    apk add --no-cache curl >/dev/null; \
	    SUMS=opentelemetry-collector-releases_otelcol-contrib_checksums.txt; \
	    URL=https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v$${OTELCOL_VERSION}; \
	    for spec in windows-amd64 linux-amd64 linux-arm64 darwin-arm64; do \
	      os=$${spec%-*}; arch=$${spec#*-}; ext=""; \
	      [ "$$os" = "windows" ] && ext=".exe"; \
	      d=/out/$$spec; mkdir -p $$d; \
	      echo "→ building magpie-agent for $$os/$$arch"; \
	      ( cd agent && GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
	          go build -trimpath -ldflags "-s -w -X main.version=$${VERSION}" \
	          -o $$d/magpie-agent$$ext ./cmd/magpie-agent ); \
	      base=otelcol-contrib_$${OTELCOL_VERSION}_$${os}_$${arch}; \
	      echo "→ fetching $$base"; \
	      t=$$(mktemp -d); \
	      curl -fsSL -o $$t/$$base.tar.gz $$URL/$$base.tar.gz; \
	      curl -fsSL -o $$t/sums $$URL/$$SUMS; \
	      e=$$(grep "  $$base.tar.gz$$" $$t/sums | awk "{print \$$1}"); \
	      a=$$(cd $$t && sha256sum $$base.tar.gz | awk "{print \$$1}"); \
	      [ "$$e" = "$$a" ] || { echo "checksum mismatch for $$spec"; exit 1; }; \
	      echo "  verified sha256=$$a"; \
	      tar -xzf $$t/$$base.tar.gz -C $$t; \
	      cp $$t/otelcol-contrib$$ext $$d/; \
	      rm -rf $$t; \
	    done; \
	    echo; echo "→ /out populated:"; ls /out/'

# ── test & lint ────────────────────────────────────────────────────────
test: ## run all Go tests with race detector
	cd control-plane && $(GO) test -race ./...
	cd agent         && $(GO) test -race ./...

test-pg: ## run Postgres-backed integration tests (downloads embedded PG ~30MB on first run)
	@echo "→ Running Postgres validation suite. First run downloads PostgreSQL"
	@echo "  binaries (~30 MB) into \$$HOME/.embedded-postgres-go/. Subsequent runs"
	@echo "  reuse the cache. Default port: 56789 (override in postgres_test.go)."
	cd control-plane && $(GO) test -tags pg -count=1 -timeout 8m ./internal/db/...

lint: ## run Go linters
	cd control-plane && $(GOLANGCI_LINT) run
	cd agent         && $(GOLANGCI_LINT) run

tidy: ## tidy Go modules
	cd control-plane && $(GO) mod tidy
	cd agent         && $(GO) mod tidy

# ── run locally ────────────────────────────────────────────────────────
quickstart: build ## build + launch magpied and the UI for local development
	@echo "→ starting magpied on :12002 (SQLite at ./magpie.db)"
	@echo "→ starting UI on :12001"
	@echo "→ Ctrl+C stops both"
	@( cd ui && $(PNPM) install --frozen-lockfile 2>/dev/null || $(PNPM) install ) >/dev/null
	@( ./bin/magpied & echo $$! > .magpied.pid; \
	   cd ui && $(PNPM) dev; \
	   kill $$(cat .magpied.pid) 2>/dev/null; rm -f .magpied.pid )

# ── docker ─────────────────────────────────────────────────────────────
docker: ## build the magpied + ui docker images
	docker build -f packaging/docker/Dockerfile    -t magpie/magpied:$(VERSION) .
	docker build -f packaging/docker/Dockerfile.ui -t magpie/ui:$(VERSION)      .

docker-up: ## start magpied + ui via docker compose
	docker compose -f packaging/docker/docker-compose.yml up -d
	@echo "→ UI      : http://localhost:12001"
	@echo "→ magpied : http://localhost:12002"

docker-down: ## stop compose stack
	docker compose -f packaging/docker/docker-compose.yml down

# ── housekeeping ───────────────────────────────────────────────────────
clean: ## remove build artifacts
	rm -rf bin/ dist/ coverage.txt coverage.html .magpied.pid
probe:
