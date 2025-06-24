GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=gofmt
BINARY_NAME=persisto
BINARY_PATH=./bin/$(BINARY_NAME)
MAIN_PATH=./src/main.go

VERSION ?= $(shell git describe --tags --always --dirty)
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
GIT_COMMIT := $(shell git rev-parse HEAD)
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)"

.DEFAULT_GOAL := help

.PHONY: help
help:
	@echo "Available targets:"
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' Makefile | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: install-tools
install-tools: ## Install development tools
	@echo "Installing development tools..."
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.61.0
	@go install golang.org/x/tools/cmd/goimports@latest
	@go install mvdan.cc/gofumpt@latest
	@go install github.com/daixiang0/gci@latest
	@go install github.com/goreleaser/goreleaser@latest
	@echo "Tools installed successfully!"

.PHONY: deps
deps: ## Download and verify Go module dependencies
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) verify
	$(GOMOD) tidy

.PHONY: clean
clean: ## Clean build artifacts and temporary files
	@echo "Cleaning..."
	$(GOCLEAN)
	rm -rf ./bin
	rm -rf ./tmp
	rm -rf ./dist

.PHONY: build
build: deps ## Build the binary for current platform
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p bin
	CGO_ENABLED=0 $(GOBUILD) $(LDFLAGS) -o $(BINARY_PATH) $(MAIN_PATH)
	@echo "Build complete: $(BINARY_PATH)"

.PHONY: build-linux
build-linux: deps ## Build the binary for Linux amd64
	@echo "Building $(BINARY_NAME) for Linux..."
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BINARY_PATH)-linux $(MAIN_PATH)

.PHONY: build-windows
build-windows: deps ## Build the binary for Windows amd64
	@echo "Building $(BINARY_NAME) for Windows..."
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BINARY_PATH)-windows.exe $(MAIN_PATH)

.PHONY: goreleaser-snapshot
goreleaser-snapshot: ## Create snapshot build with GoReleaser
	@echo "Creating snapshot build with GoReleaser..."
	@which goreleaser > /dev/null || (echo "Installing GoReleaser..." && go install github.com/goreleaser/goreleaser@latest)
	GITHUB_REPOSITORY_OWNER=$(shell git config --get remote.origin.url | sed -n 's#.*/\([^/]*\)/[^/]*$$#\1#p') goreleaser release --snapshot --clean

.PHONY: goreleaser-release
goreleaser-release: ## Create and publish release with GoReleaser
	@echo "Creating release with GoReleaser..."
	@which goreleaser > /dev/null || (echo "Installing GoReleaser..." && go install github.com/goreleaser/goreleaser@latest)
	GITHUB_REPOSITORY_OWNER=$(shell git config --get remote.origin.url | sed -n 's#.*/\([^/]*\)/[^/]*$$#\1#p') goreleaser release --clean

.PHONY: goreleaser-check
goreleaser-check: ## Check GoReleaser configuration
	@echo "Checking GoReleaser configuration..."
	@which goreleaser > /dev/null || (echo "Installing GoReleaser..." && go install github.com/goreleaser/goreleaser@latest)
	goreleaser check

.PHONY: goreleaser-build
goreleaser-build: ## Build for all platforms with GoReleaser
	@echo "Building all platforms with GoReleaser..."
	@which goreleaser > /dev/null || (echo "Installing GoReleaser..." && go install github.com/goreleaser/goreleaser@latest)
	goreleaser build --clean

.PHONY: run
run: build ## Build and run the application
	@echo "Running $(BINARY_NAME)..."
	$(BINARY_PATH)

.PHONY: test
test: deps ## Run all tests with race detection
	@echo "Running tests..."
	$(GOTEST) -v -race ./...

.PHONY: bench
bench: ## Run benchmarks with memory allocation stats
	@echo "Running benchmarks..."
	$(GOTEST) -bench=. -benchmem ./...

.PHONY: lint
lint: deps ## Run linter with golangci-lint
	@echo "Running golangci-lint..."
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint v1.61.0..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.61.0)
	golangci-lint run --config=.golangci.yml

.PHONY: lint-fix
lint-fix: deps ## Run linter with auto-fix enabled
	@echo "Running golangci-lint with auto-fix..."
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint v1.61.0..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.61.0)
	golangci-lint run --config=.golangci.yml --fix

.PHONY: fmt
fmt: ## Format Go code with gofmt
	@echo "Formatting Go code..."
	$(GOFMT) -s -w .

.PHONY: check
check: fmt vet lint ## Run all code quality checks (format, vet, lint)
	@echo "All checks completed!"

.PHONY: vet
vet: ## Run go vet to examine Go source code
	@echo "Running go vet..."
	$(GOCMD) vet ./...

.PHONY: docker-build
docker-build: ## Build Docker image locally
	@echo "Building Docker image locally..."
	docker build -t $(BINARY_NAME):$(VERSION) .
	docker tag $(BINARY_NAME):$(VERSION) $(BINARY_NAME):latest

.PHONY: docker-run
docker-run: docker-build ## Build and run Docker container
	@echo "Running Docker container..."
	docker run --rm -p 8080:8080 $(BINARY_NAME):latest

.PHONY: docker-goreleaser
docker-goreleaser: ## Build Docker images with GoReleaser
	@echo "Building Docker images with GoReleaser..."
	@which goreleaser > /dev/null || (echo "Installing GoReleaser..." && go install github.com/goreleaser/goreleaser@latest)
	goreleaser release --snapshot --clean --skip-publish