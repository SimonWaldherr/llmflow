BINARY     := llmflow
BUILD_DIR  := bin
DOCS_DIR   ?= docs/generated
MODULE     := github.com/SimonWaldherr/llmflow
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS    := -ldflags "-X main.version=$(VERSION)"
ACT        ?= act
ACT_WORKFLOW ?= .github/workflows/ci.yml
ACT_EVENT   ?= push
ACT_ARGS    ?=

.PHONY: all build test test-verbose test-cover lint clean tidy validate run ci act docs godoc swagger

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

docs: godoc swagger

godoc:
	mkdir -p $(DOCS_DIR)/godoc
	python3 scripts/generate_godoc.py $(DOCS_DIR)/godoc $(MODULE)

swagger: build
	mkdir -p $(DOCS_DIR)
	@set -e; \
	server_pid=""; \
	cleanup() { \
		if [ -n "$$server_pid" ]; then \
			kill "$$server_pid" >/dev/null 2>&1 || true; \
			wait "$$server_pid" >/dev/null 2>&1 || true; \
		fi; \
	}; \
	trap cleanup EXIT INT TERM; \
	$(BUILD_DIR)/$(BINARY) web --addr 127.0.0.1:18080 >/tmp/llmflow-swagger.log 2>&1 & \
	server_pid="$$!"; \
	for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do \
		if curl -fsS http://127.0.0.1:18080/health >/dev/null 2>&1; then \
			break; \
		fi; \
		sleep 1; \
	done; \
	curl -fsS http://127.0.0.1:18080/openapi.json -o $(DOCS_DIR)/openapi.json

act:
	$(ACT) $(ACT_EVENT) -W $(ACT_WORKFLOW) $(ACT_ARGS)

tidy:
	go mod tidy

clean:
	rm -rf $(BUILD_DIR) coverage.out coverage.html $(DOCS_DIR)

validate: build
	$(BUILD_DIR)/$(BINARY) validate --config examples/config.yaml

run: build
	$(BUILD_DIR)/$(BINARY) run --config examples/config.yaml
