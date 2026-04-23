VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  = -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"
INSTALL_DIR := $(shell go env GOPATH)/bin

# Core tools (root module)
CORE := mdq perfgate shellprof hookval effiscore

# Separate-module tools (each has its own go.mod under cmd/)
SEPARATE := bmux fetchpage protonexport linkari wasend workctl ghwatch ts-go

ALL := $(CORE) $(SEPARATE)

# Container image config (EPIC-038 M9)
IMAGE_REGISTRY ?= ghcr.io/blo-grindr/linkari
LIMA_VM        ?= lima-gvisor
# Lima stores VM state under ~/.lima/<name>/; portForwards exposes the
# containerd socket at {{.Dir}}/containerd.sock → ~/.lima/<name>/containerd.sock.
LIMA_SOCKET    ?= $(HOME)/.lima/$(LIMA_VM)/containerd.sock

.PHONY: all core build clean install test test-ts-go linkari-serve linkari-serve-local linkari-logs-local setup-fetchpage \
	container-build container-push lima-start lima-test \
	install-bmux-completions install-linkari-completions \
	$(ALL)

# --- Aggregate targets ---

all: $(ALL)

core: $(CORE)

build: all

install: $(addprefix install-,$(ALL))

clean:
	rm -rf bin/

test:
	go test ./...

# Validate claude CLI flag contract against the installed binary.
# Skips gracefully when claude is not on PATH.
.PHONY: test-claude-contract
test-claude-contract:
	@echo "Running claude CLI flag contract test..."
	@cd cmd/linkari && go test -run TestClaudeCLIFlagContract -count=1 -timeout 30s .

# Install git hooks for local development. Run once after clone.
.PHONY: install-hooks
install-hooks:
	@echo "Installing git hooks..."
	@cp scripts/hooks/pre-push .git/hooks/pre-push
	@chmod +x .git/hooks/pre-push
	@echo "Git hooks installed (pre-push -> claude flag contract)"

# --- Core tools (root module) ---

$(CORE):
	@echo "Building $@..."
	@go build $(LDFLAGS) -o bin/$@ ./cmd/$@

$(addprefix install-,$(CORE)):
	@echo "Installing $(subst install-,,$@) → $(INSTALL_DIR)/$(subst install-,,$@)"
	@go install $(LDFLAGS) ./cmd/$(subst install-,,$@)

# --- Separate-module tools ---

PLAYWRIGHT_MARKER := $(HOME)/Library/Caches/ms-playwright/.fetchpage-setup

setup-fetchpage: $(PLAYWRIGHT_MARKER)

$(PLAYWRIGHT_MARKER):
	@echo "Installing Playwright driver (one-time setup)..."
	@cd cmd/fetchpage && go run github.com/playwright-community/playwright-go/cmd/playwright install --with-deps
	@touch $(PLAYWRIGHT_MARKER)

fetchpage: setup-fetchpage
	@echo "Building fetchpage..."
	@cd cmd/fetchpage && go build $(LDFLAGS) -o ../../bin/fetchpage .

install-fetchpage: setup-fetchpage
	@echo "Installing fetchpage → $(INSTALL_DIR)/fetchpage"
	@cd cmd/fetchpage && go install $(LDFLAGS) .

bmux:
	@echo "Building bmux..."
	@cd cmd/bmux && go build $(LDFLAGS) -o ../../bin/bmux .

install-bmux:
	@echo "Installing bmux → $(INSTALL_DIR)/bmux"
	@cd cmd/bmux && go install $(LDFLAGS) .

install-bmux-completions: install-bmux
	@mkdir -p $(HOME)/.config/fish/completions
	@$(INSTALL_DIR)/bmux completion fish > $(HOME)/.config/fish/completions/bmux.fish
	@echo "Installed fish completions → $(HOME)/.config/fish/completions/bmux.fish"

protonexport:
	@echo "Building protonexport..."
	@cd cmd/protonexport && go build $(LDFLAGS) -o ../../bin/protonexport .

install-protonexport:
	@echo "Installing protonexport → $(INSTALL_DIR)/protonexport"
	@cd cmd/protonexport && go install $(LDFLAGS) .

linkari:
	@echo "Building linkari..."
	@cd cmd/linkari && go build $(LDFLAGS) -o ../../bin/linkari .

install-linkari:
	@echo "Installing linkari → $(INSTALL_DIR)/linkari"
	@cd cmd/linkari && go install $(LDFLAGS) .

# Generate fish completions and install to ~/.config/fish/completions/.
# Fish auto-loads anything in this directory — no edit to config.fish.
install-linkari-completions: install-linkari
	@mkdir -p $(HOME)/.config/fish/completions
	@$(INSTALL_DIR)/linkari completion fish > $(HOME)/.config/fish/completions/linkari.fish
	@echo "Installed fish completions → $(HOME)/.config/fish/completions/linkari.fish"

AWS_PROFILE ?= brianonpoint
AWS_REGION ?= us-east-2
AWS_BEARER_SECRET_ID := linkari/bearer-token
FETCH_TOKEN = AWS_PROFILE=$(AWS_PROFILE) AWS_REGION=$(AWS_REGION) aws secretsmanager get-secret-value --secret-id $(AWS_BEARER_SECRET_ID) --query SecretString --output text | tr -d '\r\n[:space:]'

linkari-serve: linkari
	@echo "Starting linkari (profile=$(AWS_PROFILE) region=$(AWS_REGION), debug)..."
	@AWS_PROFILE=$(AWS_PROFILE) AWS_REGION=$(AWS_REGION) bin/linkari serve --debug

linkari-serve-local: linkari
	@echo "Starting linkari on :8080 (token=mytoken, debug)..."
	@bin/linkari serve --port 8080 --token mytoken --debug

linkari-logs-local:
	@curl -sN "http://localhost:8080/logs/stream?token=mytoken"

linkari-serve-local-tls: linkari
	@echo "Starting linkari on :8080 (TLS, token=mytoken, debug)..."
	@bin/linkari serve --port 8080 --token mytoken --debug --tls

linkari-logs-local-tls:
	@curl -sN "https://localhost:8080/logs/stream?token=mytoken"

serve-linkari:
	@echo "Starting linkari on :8080..."
	@test -n "$(AWS_PROFILE)" || { echo "❌ AWS_PROFILE unset (required to fetch $(AWS_BEARER_SECRET_ID))"; exit 1; }
	@tok=$$($(FETCH_TOKEN)) && test -n "$$tok" && \
		LINKARI_TOKEN=$$tok LINKARI_FIREBASE_SA=$(HOME)/.config/linkari/firebase-sa.json bin/linkari serve

serve-linkari-tls:
	@echo "Starting linkari on :8080 (TLS)..."
	@test -n "$(AWS_PROFILE)" || { echo "❌ AWS_PROFILE unset (required to fetch $(AWS_BEARER_SECRET_ID))"; exit 1; }
	@tok=$$($(FETCH_TOKEN)) && test -n "$$tok" && \
		LINKARI_TOKEN=$$tok LINKARI_FIREBASE_SA=$(HOME)/.config/linkari/firebase-sa.json bin/linkari serve --tls

logs-linkari:
	@test -n "$(AWS_PROFILE)" || { echo "❌ AWS_PROFILE unset"; exit 1; }
	@tok=$$($(FETCH_TOKEN)) && curl -sN "http://localhost:8080/logs/stream?token=$$tok"

logs-linkari-tls:
	@test -n "$(AWS_PROFILE)" || { echo "❌ AWS_PROFILE unset"; exit 1; }
	@tok=$$($(FETCH_TOKEN)) && curl -sN "https://localhost:8080/logs/stream?token=$$tok"

ts-go:
	@echo "Building ts-go..."
	@cd cmd/ts-go && go build $(LDFLAGS) -o ../../bin/ts-go .

install-ts-go:
	@echo "Installing ts-go → $(INSTALL_DIR)/ts-go"
	@cd cmd/ts-go && go install $(LDFLAGS) .

test-ts-go:
	@echo "Running ts-go tests (requires C compiler for CGo)..."
	@cd cmd/ts-go && go test -count=1 ./...

wasend:
	@echo "Building wasend..."
	@cd cmd/wasend && go build $(LDFLAGS) -o ../../bin/wasend .

install-wasend:
	@echo "Installing wasend → $(INSTALL_DIR)/wasend"
	@cd cmd/wasend && go install $(LDFLAGS) .

workctl:
	@echo "Building workctl..."
	@cd cmd/workctl && go build $(LDFLAGS) -o ../../bin/workctl ./cmd/workctl

install-workctl:
	@echo "Installing workctl → $(INSTALL_DIR)/workctl"
	@cd cmd/workctl && go install $(LDFLAGS) ./cmd/workctl

ghwatch:
	@echo "Building ghwatch..."
	@cd cmd/workctl && go build $(LDFLAGS) -o ../../bin/ghwatch ./cmd/ghwatch

install-ghwatch:
	@echo "Installing ghwatch → $(INSTALL_DIR)/ghwatch"
	@cd cmd/workctl && go install $(LDFLAGS) ./cmd/ghwatch

# ─── Container image targets (EPIC-038 M9) ─────────────────────────────────

# Build container images for the local native platform only (fast, for dev iteration).
# Requires Docker on PATH. IMAGE_REGISTRY can be overridden:
#   make container-build IMAGE_REGISTRY=myregistry.io/linkari
container-build:
	@echo "Building container images for native platform (registry=$(IMAGE_REGISTRY))..."
	@docker build -f container/Dockerfile.ffmpeg -t $(IMAGE_REGISTRY)/ffmpeg:latest container/
	@docker build -f container/Dockerfile.whisper -t $(IMAGE_REGISTRY)/whisper:latest container/
	@docker build -f container/Dockerfile.claude-sandbox \
		--build-arg CLAUDE_BIN_PATH="$(shell which claude)" \
		-t $(IMAGE_REGISTRY)/claude-sandbox:latest container/
	@echo "✅ Container images built (native platform)"

# Push multi-arch (linux/amd64 + linux/arm64) images to the registry.
# Requires: docker buildx with a builder that supports multi-arch (docker buildx create --use).
# Images are pushed directly — docker load does not support multi-arch manifests.
#   make container-push IMAGE_REGISTRY=myregistry.io/linkari
container-push:
	@echo "Building and pushing multi-arch container images (registry=$(IMAGE_REGISTRY))..."
	@docker buildx build --platform linux/amd64,linux/arm64 --push \
		-f container/Dockerfile.ffmpeg -t $(IMAGE_REGISTRY)/ffmpeg:latest container/
	@docker buildx build --platform linux/amd64,linux/arm64 --push \
		-f container/Dockerfile.whisper -t $(IMAGE_REGISTRY)/whisper:latest container/
	@docker buildx build --platform linux/amd64,linux/arm64 --push \
		--build-arg CLAUDE_BIN_PATH="$(shell which claude)" \
		-f container/Dockerfile.claude-sandbox -t $(IMAGE_REGISTRY)/claude-sandbox:latest container/
	@echo "✅ Multi-arch container images pushed to $(IMAGE_REGISTRY)"

# Start the Lima gVisor VM. First run takes ~5 minutes (Ubuntu + gVisor download).
# Uses infra/lima-gvisor.yaml for VM configuration.
lima-start:
	@echo "Starting Lima VM '$(LIMA_VM)'..."
	@limactl start infra/lima-gvisor.yaml --name $(LIMA_VM) || limactl start $(LIMA_VM)
	@echo "Waiting for containerd socket..."
	@limactl shell $(LIMA_VM) -- bash -c 'for i in $$(seq 30); do systemctl is-active containerd >/dev/null 2>&1 && break; sleep 2; done'
	@echo "✅ Lima VM '$(LIMA_VM)' running with gVisor"
	@limactl shell $(LIMA_VM) -- /opt/gvisor/runsc --version

# Run a smoke test inside the Lima VM: start a gVisor container and verify it runs.
# Skips automatically when the Lima socket is absent (CI without Lima installed).
lima-test:
	@if ! limactl list 2>/dev/null | grep -q $(LIMA_VM); then \
		echo "⚠️  Lima VM '$(LIMA_VM)' not running — skipping lima-test"; \
		exit 0; \
	fi
	@echo "Running gVisor smoke test inside $(LIMA_VM)..."
	@limactl shell $(LIMA_VM) -- sudo nerdctl run --rm \
		--snapshotter=overlayfs \
		--runtime=runsc \
		alpine sh -c 'echo "gVisor OK: $$(uname -r)"'
	@echo "✅ gVisor smoke test passed"

# Run integration tests (requires Lima VM running with containers loaded).
# Use: make integration-test
integration-test: lima-test
	@echo "Running integration tests against Lima VM..."
	@cd cmd/linkari && LINKARI_RUNTIME_SOCKET=$(LIMA_SOCKET) \
		go test -v -tags=integration -run TestContainer ./...
	@echo "✅ Integration tests passed"
