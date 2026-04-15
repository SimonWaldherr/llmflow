BINARY     := llmflow
BUILD_DIR  := bin
MODULE     := github.com/SimonWaldherr/llmflow
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS    := -ldflags "-X main.version=$(VERSION)"
ACT        ?= act
ACT_WORKFLOW ?= .github/workflows/ci.yml
ACT_EVENT   ?= push
ACT_ARGS    ?=

.PHONY: all build test test-verbose test-cover lint clean tidy validate run ci act

all: build

build:
	mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/llmflow

test:
	go test ./...

test-verbose:
	go test -v ./...

test-cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

lint:
	golangci-lint run ./...

ci: test build

act:
	$(ACT) $(ACT_EVENT) -W $(ACT_WORKFLOW) $(ACT_ARGS)

tidy:
	go mod tidy

clean:
	rm -rf $(BUILD_DIR) coverage.out coverage.html

validate: build
	$(BUILD_DIR)/$(BINARY) validate --config examples/config.yaml

run: build
	$(BUILD_DIR)/$(BINARY) run --config examples/config.yaml
