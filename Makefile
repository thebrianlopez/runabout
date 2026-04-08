VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  = -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"
INSTALL_DIR := $(shell go env GOPATH)/bin

# Core tools (root module)
CORE := mdq perfgate shellprof hookval effiscore

# Separate-module tools (each has its own go.mod under cmd/)
SEPARATE := fetchpage protonexport linkari wasend

ALL := $(CORE) $(SEPARATE)

.PHONY: all core build clean install test linkari-serve linkari-serve-local linkari-logs-local setup-fetchpage $(ALL)

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

wasend:
	@echo "Building wasend..."
	@cd cmd/wasend && go build $(LDFLAGS) -o ../../bin/wasend .

install-wasend:
	@echo "Installing wasend → $(INSTALL_DIR)/wasend"
	@cd cmd/wasend && go install $(LDFLAGS) .
