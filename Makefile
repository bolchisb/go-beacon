# No Go toolchain is required on the host: everything runs in containers.
COMPOSE ?= podman compose
GO_IMAGE ?= golang:1.26.6-alpine3.23
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Named volumes keep the module and build caches between runs; without them
# every cross-compile starts from zero.
GO_RUN = podman run --rm \
	-v $(CURDIR):/src:z \
	-v go-beacon-modcache:/go/pkg/mod \
	-v go-beacon-buildcache:/root/.cache/go-build \
	-w /src -e GOFLAGS=-buildvcs=false $(GO_IMAGE)

# The agent ships as plain binaries: it runs on developer and target machines,
# not in podman. Pure Go and CGO_ENABLED=0 means all six come out of one
# linux container.
PLATFORMS ?= windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: up down logs tidy vet build client clean

## up: build and start the relay
up:
	$(COMPOSE) up --build -d

## down: stop the relay and drop volumes
down:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f

## tidy: resolve dependencies and write go.sum
tidy:
	$(GO_RUN) go mod tidy

vet:
	$(GO_RUN) sh -c "go vet ./... && gofmt -l ."

build:
	$(COMPOSE) build

## client: cross-compile the agent for every supported platform into dist/
client:
	$(GO_RUN) sh -c 'set -e; \
	for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=""; \
	  if [ "$$os" = windows ]; then ext=".exe"; fi; \
	  echo "building $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
	    -ldflags "-s -w -X main.version=$(VERSION)" \
	    -o dist/beacon-$$os-$$arch$$ext ./client; \
	done'
	@ls -lh dist/

clean:
	rm -rf dist
