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
install-tools:
	@echo "Installing development tools..."
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.61.0
	@go install golang.org/x/tools/cmd/goimports@latest
	@go install mvdan.cc/gofumpt@latest
	@go install github.com/daixiang0/gci@latest
	@go install github.com/goreleaser/goreleaser@latest
	@echo "Tools installed successfully!"

.PHONY: deps
deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) verify
	$(GOMOD) tidy

.PHONY: clean
clean:
	@echo "Cleaning..."
	$(GOCLEAN)
	rm -rf ./bin
	rm -rf ./tmp
	rm -rf ./dist

.PHONY: build
build: deps
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p bin
	CGO_ENABLED=0 $(GOBUILD) $(LDFLAGS) -o $(BINARY_PATH) $(MAIN_PATH)
	@echo "Build complete: $(BINARY_PATH)"

.PHONY: build-linux
build-linux: deps
	@echo "Building $(BINARY_NAME) for Linux..."
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BINARY_PATH)-linux $(MAIN_PATH)

.PHONY: build-windows
build-windows: deps
	@echo "Building $(BINARY_NAME) for Windows..."
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o $(BINARY_PATH)-windows.exe $(MAIN_PATH)

.PHONY: goreleaser-snapshot
goreleaser-snapshot:
	@echo "Creating snapshot build with GoReleaser..."
	@which goreleaser > /dev/null || (echo "Installing GoReleaser..." && go install github.com/goreleaser/goreleaser@latest)
	GITHUB_REPOSITORY_OWNER=$(shell git config --get remote.origin.url | sed -n 's#.*/\([^/]*\)/[^/]*$$#\1#p') goreleaser release --snapshot --clean

.PHONY: goreleaser-release
goreleaser-release:
	@echo "Creating release with GoReleaser..."
	@which goreleaser > /dev/null || (echo "Installing GoReleaser..." && go install github.com/goreleaser/goreleaser@latest)
	GITHUB_REPOSITORY_OWNER=$(shell git config --get remote.origin.url | sed -n 's#.*/\([^/]*\)/[^/]*$$#\1#p') goreleaser release --clean

.PHONY: goreleaser-check
goreleaser-check:
	@echo "Checking GoReleaser configuration..."
	@which goreleaser > /dev/null || (echo "Installing GoReleaser..." && go install github.com/goreleaser/goreleaser@latest)
	goreleaser check

.PHONY: goreleaser-build
goreleaser-build:
	@echo "Building all platforms with GoReleaser..."
	@which goreleaser > /dev/null || (echo "Installing GoReleaser..." && go install github.com/goreleaser/goreleaser@latest)
	goreleaser build --clean

.PHONY: run
run: build
	@echo "Running $(BINARY_NAME)..."
	$(BINARY_PATH)

.PHONY: test
test: deps
	@echo "Running tests..."
	$(GOTEST) -v -race ./...

.PHONY: lint
lint: deps
	@echo "Running golangci-lint..."
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint v1.61.0..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.61.0)
	golangci-lint run --config=.golangci.yml

.PHONY: lint-fix
lint-fix: deps
	@echo "Running golangci-lint with auto-fix..."
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint v1.61.0..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.61.0)
	golangci-lint run --config=.golangci.yml --fix

.PHONY: fmt
fmt:
	@echo "Formatting Go code..."
	$(GOFMT) -s -w .

.PHONY: check
# NOTE: lint, vet and format checks
check: fmt vet lint
	@echo "All checks completed!"

.PHONY: vet
vet:
	@echo "Running go vet..."
	$(GOCMD) vet ./...

.PHONY: docker-build
docker-build:
	@echo "Building Docker image locally..."
	docker build -t $(BINARY_NAME):$(VERSION) .
	docker tag $(BINARY_NAME):$(VERSION) $(BINARY_NAME):latest

.PHONY: docker-run
docker-run: docker-build
	@echo "Running Docker container..."
	docker run --rm -p 8080:8080 $(BINARY_NAME):latest

.PHONY: docker-goreleaser
docker-goreleaser:
	@echo "Building Docker images with GoReleaser..."
	@which goreleaser > /dev/null || (echo "Installing GoReleaser..." && go install github.com/goreleaser/goreleaser@latest)
	goreleaser release --snapshot --clean --skip-publish