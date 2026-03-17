VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -ldflags "-X github.com/blo-grindr/runabout/internal/version.Version=$(VERSION) \
	-X github.com/blo-grindr/runabout/internal/version.Commit=$(COMMIT) \
	-X github.com/blo-grindr/runabout/internal/version.Date=$(DATE)"

BINARIES := shellprof mdq perfgate
PROTON_BINARIES := protonexport
PROTON_LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"
INSTALL_DIR := $(shell go env GOPATH)/bin

.PHONY: build install clean

build:
	@for bin in $(BINARIES); do \
		echo "Building $$bin..."; \
		go build $(LDFLAGS) -o bin/$$bin ./cmd/$$bin; \
	done
	@for bin in $(PROTON_BINARIES); do \
		echo "Building $$bin (separate module)..."; \
		cd cmd/$$bin && go build $(PROTON_LDFLAGS) -o ../../bin/$$bin . && cd ../..; \
	done
	@echo "All binaries built in bin/"

install:
	@for bin in $(BINARIES); do \
		echo "Installing $$bin → $(INSTALL_DIR)/$$bin"; \
		go install $(LDFLAGS) ./cmd/$$bin; \
	done
	@for bin in $(PROTON_BINARIES); do \
		echo "Installing $$bin → $(INSTALL_DIR)/$$bin"; \
		cd cmd/$$bin && go install $(PROTON_LDFLAGS) . && cd ../..; \
	done
	@echo "Installed to $(INSTALL_DIR)"

clean:
	rm -rf bin/
