GO ?= go
BIN := golem
BIN_TUI := golem-tui

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: all build test test-race cover lint fmt vet tidy clean

all: lint test-race build

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/$(BIN) ./cmd/golem
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/$(BIN_TUI) ./cmd/golem-tui

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

cover:
	$(GO) test -race -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out | tail -1

lint:
	golangci-lint run

fmt:
	gofumpt -w -extra .
	goimports -w -local github.com/dev-hann/golem .

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin coverage.out
