VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  = -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"
INSTALL_DIR := $(shell go env GOPATH)/bin

# Core tools (root module)
CORE := mdq perfgate shellprof

# Separate-module tools (each has its own go.mod under cmd/)
SEPARATE := wasend

ALL := $(CORE) $(SEPARATE)

.PHONY: all core build clean install test $(ALL)

# --- Aggregate targets ---

all: $(ALL)

core: $(CORE)

build: all

install: $(addprefix install-,$(ALL))

clean:
	rm -rf bin/

test:
	go test ./...

# --- Core tools (root module) ---

$(CORE):
	@echo "Building $@..."
	@go build $(LDFLAGS) -o bin/$@ ./cmd/$@

$(addprefix install-,$(CORE)):
	@echo "Installing $(subst install-,,$@) → $(INSTALL_DIR)/$(subst install-,,$@)"
	@go install $(LDFLAGS) ./cmd/$(subst install-,,$@)

# --- Separate-module tools ---

wasend:
	@echo "Building wasend..."
	@cd cmd/wasend && go build $(LDFLAGS) -o ../../bin/wasend .

install-wasend:
	@echo "Installing wasend → $(INSTALL_DIR)/wasend"
	@cd cmd/wasend && go install $(LDFLAGS) .
