# Lernen Makefile

BINARY := lernen
CMD_DIR := ./cmd/lernen
PKGS := ./...

.PHONY: all build test lint fmt clean run install help

all: fmt lint test build

build: ## Build the lernen binary
	go build -o $(BINARY) $(CMD_DIR)

test: ## Run all tests
	go test -v -race $(PKGS)

test-short: ## Run tests without race detector (faster)
	go test $(PKGS)

lint: ## Run golangci-lint
	golangci-lint run $(PKGS)

fmt: ## Format code with gofmt and goimports
	gofmt -w .
	@command -v goimports >/dev/null && goimports -w . || echo "goimports not installed; skipping"

clean: ## Remove build artifacts
	rm -f $(BINARY)
	go clean

run: build ## Build and run lernen
	./$(BINARY)

install: ## Install lernen to $GOPATH/bin
	go install $(CMD_DIR)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-12s\033[0m %s\n", $$1, $$2}'
