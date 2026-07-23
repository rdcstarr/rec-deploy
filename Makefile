BINARY    := rec-deploy
PKG       := github.com/rdcstarr/rec-deploy
BUILDINFO := $(PKG)/internal/buildinfo

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(BUILDINFO).Version=$(VERSION) \
	-X $(BUILDINFO).Commit=$(COMMIT) \
	-X $(BUILDINFO).Date=$(DATE)

GO := go

export CGO_ENABLED := 0

.PHONY: build run fmt vet lint test tidy snapshot clean help

## build: compile a static binary into ./rec-deploy
build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) .

## run: go run the CLI (pass ARGS="version")
run:
	$(GO) run . $(ARGS)

## fmt: format the code with goimports
fmt:
	$(GO) run golang.org/x/tools/cmd/goimports@latest -w -local $(PKG) .

## vet: run go vet
vet:
	$(GO) vet ./...

## lint: run golangci-lint
lint:
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run

## test: run the test suite
test:
	$(GO) test ./...

## tidy: tidy go.mod / go.sum
tidy:
	$(GO) mod tidy

## snapshot: build a local GoReleaser snapshot (no publish)
snapshot:
	$(GO) run github.com/goreleaser/goreleaser/v2@latest build --snapshot --clean

## clean: remove build artifacts
clean:
	rm -rf $(BINARY) dist/

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
