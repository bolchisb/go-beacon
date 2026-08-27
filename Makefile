# No Go toolchain is required on the host: everything runs in containers.
# Either engine works; podman wins if both are installed. Override with
# `make ENGINE=docker <target>`.
ENGINE ?= $(shell for e in podman docker; do command -v $$e >/dev/null 2>&1 && { echo $$e; break; }; done)
ifeq ($(ENGINE),)
$(error no container engine found: install podman or docker, or pass ENGINE=...)
endif

# `podman compose` / `docker compose` on modern installs, the standalone
# `podman-compose` / `docker-compose` binary otherwise.
COMPOSE ?= $(shell \
	if $(ENGINE) compose version >/dev/null 2>&1; then echo "$(ENGINE) compose"; \
	elif command -v $(ENGINE)-compose >/dev/null 2>&1; then echo "$(ENGINE)-compose"; \
	else echo "$(ENGINE) compose"; fi)
GO_IMAGE ?= golang:1.26.6-alpine3.23
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Named volumes keep the module and build caches between runs; without them
# every cross-compile starts from zero.
GO_RUN = $(ENGINE) run --rm \
	-v $(CURDIR):/src:z \
	-v go-beacon-modcache:/go/pkg/mod \
	-v go-beacon-buildcache:/root/.cache/go-build \
	-w /src -e GOFLAGS=-buildvcs=false $(GO_IMAGE)

# The agent ships as plain binaries: it runs on developer and target machines,
# not in a container. Pure Go and CGO_ENABLED=0 means all six come out of one
# linux container.
PLATFORMS ?= windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

GH_REPO ?= bolchisb/go-beacon

.PHONY: up down logs tidy vet test build client release clean vault-init vault-status vault-unseal-log vault-agent-log

## up: build and start the relay
up:
	$(COMPOSE) up --build -d

## down: stop the relay and drop volumes
down:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f

## vault-init: create the transit key, policy and approle. Idempotent.
##   Run once after the first `make up`. Vault Agent picks the credentials up
##   on its own from there -- nothing has to be copied into .env.
vault-init:
	@$(COMPOSE) exec -T vault-unseal sh -c '\
	  VAULT_ADDR=http://vault:8200 \
	  VAULT_TOKEN=$$(jq -r .root_token /unseal/unseal.json) \
	  beacon-vault-bootstrap'

## vault-status: is Vault up and unsealed?
vault-status:
	@$(COMPOSE) exec -T vault vault status || true

## vault-unseal-log: what the unseal supervisor has been doing
vault-unseal-log:
	@$(COMPOSE) logs --tail=30 vault-unseal

## vault-agent-log: has Vault Agent got a token, and is it renewing it?
vault-agent-log:
	@$(COMPOSE) logs --tail=30 vault-agent

## tidy: resolve dependencies and write go.sum
tidy:
	$(GO_RUN) go mod tidy

vet:
	$(GO_RUN) sh -c "go vet ./... && gofmt -l ."

test:
	$(GO_RUN) go test ./...

build:
	$(COMPOSE) build

## client: cross-compile the agent for every supported platform into dist/
client:
	@$(GO_RUN) sh -c 'set -e; \
	for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=""; \
	  if [ "$$os" = windows ]; then ext=".exe"; fi; \
	  echo "building $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
	    -ldflags "-s -w -X main.version=$(VERSION) -X main.updateRepo=$(GH_REPO)" \
	    -o dist/beacon-$$os-$$arch$$ext ./client; \
	done'
	@ls -lh dist/

## release: build and publish the binaries to a GitHub release
##   make release TAG=v0.1.0
##   make release TAG=dev-07 PLATFORMS=linux/amd64      one platform, fast loop
##   make release TAG=v0.1.0 DRY=1                      print, publish nothing
release:
	@test -n "$(TAG)" || { \
	  echo "TAG is required:  make release TAG=v0.1.0 [PLATFORMS=linux/amd64] [DRY=1]"; exit 1; }
	@if [ -z "$(ALLOW_DIRTY)" ] && ! git diff --quiet HEAD; then \
	  echo "working tree is dirty - the binary would not match the tag."; \
	  echo "commit first, or pass ALLOW_DIRTY=1 if you know what you are doing."; exit 1; fi
	@git fetch -q origin
	@git branch -r --contains HEAD 2>/dev/null | grep -q . || { \
	  echo "HEAD is not on any remote branch - push before releasing,"; \
	  echo "otherwise the release would point at a commit GitHub cannot see."; exit 1; }
	@rm -rf dist
	@$(MAKE) --no-print-directory client VERSION=$(TAG)
	@$(GO_RUN) sh -c 'cd dist && sha256sum beacon-* > SHA256SUMS'
	@set -e; \
	RUN='$(if $(DRY),echo [dry],)'; \
	SHA=$$(git rev-parse HEAD); \
	if gh release view "$(TAG)" --repo $(GH_REPO) >/dev/null 2>&1; then \
	  echo "release $(TAG) exists, replacing its assets"; \
	  $$RUN gh release upload "$(TAG)" dist/* --repo $(GH_REPO) --clobber; \
	else \
	  $$RUN gh release create "$(TAG)" dist/* --repo $(GH_REPO) \
	    --target "$$SHA" --title "$(TAG)" \
	    --notes "commit $$SHA"$(if $(findstring dev,$(TAG)), --prerelease,); \
	fi
	@echo; echo "https://github.com/$(GH_REPO)/releases/tag/$(TAG)"

clean:
	rm -rf dist
