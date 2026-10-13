GO ?= go
BIN := golem
BIN_TUI := golem-tui

.PHONY: all build test test-race cover lint fmt vet tidy clean

all: lint test-race build

build:
	$(GO) build -o bin/$(BIN) ./cmd/golem
	$(GO) build -o bin/$(BIN_TUI) ./cmd/golem-tui

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
