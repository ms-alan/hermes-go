.PHONY: all build build-gateway test vet lint clean install run run-gateway help

GO          := go
BINARY      := hermes
BINARY_GW   := hermes-gateway
CMD_DIR     := ./cmd/hermes
CMD_GW      := ./cmd/gateway
BUILD_DIR   := .
GOTEST_FLAGS := -v -race

all: build build-gateway

build:
	@echo "Building $(BINARY)..."
	$(GO) build -o $(BUILD_DIR)/$(BINARY) $(CMD_DIR)

build-gateway:
	@echo "Building $(BINARY_GW)..."
	$(GO) build -o $(BUILD_DIR)/$(BINARY_GW) $(CMD_GW)

test:
	$(GO) test $(GOTEST_FLAGS) ./...

vet:
	$(GO) vet ./...

lint: vet

clean:
	@echo "Cleaning..."
	@rm -f $(BUILD_DIR)/$(BINARY) $(BUILD_DIR)/$(BINARY_GW)
	@rm -rf dist/

install: build build-gateway
	@echo "Installing to /usr/local/bin/..."
	@sudo cp $(BUILD_DIR)/$(BINARY) /usr/local/bin/
	@sudo cp $(BUILD_DIR)/$(BINARY_GW) /usr/local/bin/
	@echo "Installed $(BINARY) and $(BINARY_GW) to /usr/local/bin/"

run: build
	@echo "Running $(BINARY)..."
	./$(BINARY)

run-gateway: build-gateway
	@echo "Running $(BINARY_GW)..."
	./$(BINARY_GW)

help:
	@echo "Hermes Go Makefile"
	@echo ""
	@echo "Targets:"
	@echo "  all           Build both hermes and hermes-gateway (default)"
	@echo "  build         Build hermes CLI binary"
	@echo "  build-gateway Build hermes-gateway binary"
	@echo "  test          Run tests with race detector"
	@echo "  vet           Run go vet"
	@echo "  lint          Alias for vet"
	@echo "  clean         Remove built binaries and dist/"
	@echo "  install       Build and copy binaries to /usr/local/bin/"
	@echo "  run           Build and run hermes"
	@echo "  run-gateway   Build and run hermes-gateway"
	@echo "  help          Show this help message"
