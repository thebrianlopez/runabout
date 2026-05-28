VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  = -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"
INSTALL_DIR := $(shell go env GOPATH)/bin

# Core tools (root module)
CORE := mdq perfgate shellprof hookval effiscore castex chain-eval

# Separate-module tools (each has its own go.mod under cmd/)
SEPARATE := bmux fetchpage protonexport linkari linkari-labeler plaid-service wasend workctl ghwatch ts-go jira-poller runway

ALL := $(CORE) $(SEPARATE)

# Container image config (EPIC-038 M9)
IMAGE_REGISTRY ?= ghcr.io/blo-grindr/linkari
LIMA_VM        ?= lima-gvisor
# Lima stores VM state under ~/.lima/<name>/; portForwards exposes the
# containerd socket at {{.Dir}}/containerd.sock → ~/.lima/<name>/containerd.sock.
LIMA_SOCKET    ?= $(HOME)/.lima/$(LIMA_VM)/containerd.sock

.PHONY: all core build clean install test test-fish manifest-audit test-ts-go test-jira-poller linkari-serve linkari-serve-local linkari-logs-local setup-fetchpage linkari-labeler install-linkari-labeler \
	container-build container-push lima-start lima-test \
	install-bmux-completions install-linkari-completions \
	jira-poller install-jira-poller run-jira-poller lint-jira-poller \
	auth-bluesky auth-google \
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
	cd cmd/jira-poller && go test ./...

test-fish:
	@echo "Running Fish contract tests..."
	@fish fish/tests/ct-f001.fish; \
	 fish fish/tests/ct-f002.fish; \
	 fish fish/tests/ct-f003.fish

manifest-audit:
	@echo "Running manifest audit (F-003 static checks)..."
	@fish fish/tests/ct-f003.fish

# EPIC-112 F5 M2: Copy profile YAMLs from personal-docs to testdata/profiles snapshot.
# Run this whenever personal-docs profiles are updated to keep CI in sync.
.PHONY: update-profiles
update-profiles:
	cp ~/code/personal/docs/prompts/profiles/eng.yaml \
	   ~/code/personal/docs/prompts/profiles/life.yaml \
	   ~/code/personal/docs/prompts/profiles/travel.yaml \
	   ~/code/personal/docs/prompts/profiles/fashion.yaml \
	   ~/code/personal/docs/prompts/profiles/music.yaml \
	   ~/code/personal/docs/prompts/profiles/finance.yaml \
	   ~/code/personal/docs/prompts/profiles/dining.yaml \
	   cmd/linkari/testdata/profiles/
	@echo "Updated testdata/profiles with 7 profile snapshots"

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
	@echo "Installing $(subst install-,,$@) -> $(INSTALL_DIR)/$(subst install-,,$@)"
	@go install $(LDFLAGS) ./cmd/$(subst install-,,$@)

# --- Separate-module tools ---

setup-fetchpage:
	@python3 -c "import cloakbrowser" 2>/dev/null || python3 -m pip install cloakbrowser -q

fetchpage: setup-fetchpage
	@echo "Installing fetchpage script → bin/fetchpage"
	@cp cmd/fetchpage/fetchpage.py bin/fetchpage
	@chmod +x bin/fetchpage

install-fetchpage: setup-fetchpage
	@echo "Installing fetchpage → $(INSTALL_DIR)/fetchpage"
	@cp cmd/fetchpage/fetchpage.py $(INSTALL_DIR)/fetchpage
	@chmod +x $(INSTALL_DIR)/fetchpage

bmux:
	@echo "Building bmux..."
	@cd cmd/bmux && go build $(LDFLAGS) -o ../../bin/bmux .

install-bmux:
	@echo "Installing bmux -> $(INSTALL_DIR)/bmux"
	@cd cmd/bmux && go install $(LDFLAGS) .

install-bmux-completions: install-bmux
	@mkdir -p $(HOME)/.config/fish/completions
	@$(INSTALL_DIR)/bmux completion fish > $(HOME)/.config/fish/completions/bmux.fish
	@echo "Installed fish completions -> $(HOME)/.config/fish/completions/bmux.fish"

plaid-service:
	@echo "Building plaid-service..."
	@cd cmd/plaid-service && go build $(LDFLAGS) -o ../../bin/plaid-service .

install-plaid-service:
	@echo "Installing plaid-service -> $(INSTALL_DIR)/plaid-service"
	@cd cmd/plaid-service && go install $(LDFLAGS) .

protonexport:
	@echo "Building protonexport..."
	@cd cmd/protonexport && go build $(LDFLAGS) -o ../../bin/protonexport .

install-protonexport:
	@echo "Installing protonexport -> $(INSTALL_DIR)/protonexport"
	@cd cmd/protonexport && go install $(LDFLAGS) .

linkari:
	@echo "Building linkari..."
	@cd cmd/linkari && go build $(LDFLAGS) -o ../../bin/linkari .

install-linkari:
	@echo "Installing linkari -> $(INSTALL_DIR)/linkari"
	@cd cmd/linkari && go install $(LDFLAGS) .

# Generate fish completions and install to ~/.config/fish/completions/.
# Fish auto-loads anything in this directory  -  no edit to config.fish.
install-linkari-completions: install-linkari
	@mkdir -p $(HOME)/.config/fish/completions
	@$(INSTALL_DIR)/linkari completion fish > $(HOME)/.config/fish/completions/linkari.fish
	@echo "Installed fish completions -> $(HOME)/.config/fish/completions/linkari.fish"

linkari-labeler:
	@echo "Building linkari-labeler..."
	@cd cmd/linkari-labeler && go build $(LDFLAGS) -o ../../bin/linkari-labeler .

install-linkari-labeler:
	@echo "Installing linkari-labeler -> $(INSTALL_DIR)/linkari-labeler"
	@cd cmd/linkari-labeler && go install $(LDFLAGS) .

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

# --- Auth targets -----------------------------------------------------------

LINKARI_PORT  ?= 8080
LINKARI_BASE   = http://localhost:$(LINKARI_PORT)
LINKARI_DB    := $(HOME)/.config/linkari/queue.db
BSKY_HANDLE   ?=
BSKY_PASSWORD ?=
BSKY_HOST     ?=

# Authenticate Bluesky via the running linkari server.
# Requires: linkari serve running, Google OAuth completed (session token in DB).
# Usage: make auth-bluesky BSKY_HANDLE=you.bsky.social BSKY_PASSWORD=xxxx-xxxx-xxxx-xxxx
# For a custom PDS: make auth-bluesky BSKY_HANDLE=... BSKY_PASSWORD=... BSKY_HOST=https://your-pds.example.com
auth-bluesky:
	@test -n "$(BSKY_HANDLE)"   || { echo "ERROR: BSKY_HANDLE unset  (e.g. make auth-bluesky BSKY_HANDLE=you.bsky.social BSKY_PASSWORD=xxxx-xxxx-xxxx-xxxx)"; exit 1; }
	@test -n "$(BSKY_PASSWORD)" || { echo "ERROR: BSKY_PASSWORD unset (e.g. make auth-bluesky BSKY_HANDLE=you.bsky.social BSKY_PASSWORD=xxxx-xxxx-xxxx-xxxx)"; exit 1; }
	@test -f "$(LINKARI_DB)"    || { echo "ERROR: DB not found at $(LINKARI_DB) - run linkari serve first"; exit 1; }
	@tok=$$(sqlite3 $(LINKARI_DB) "SELECT token FROM sessions ORDER BY created_at DESC LIMIT 1;"); \
	 test -n "$$tok" || { echo "ERROR: No session token in DB - complete Google OAuth first (make auth-google)"; exit 1; }; \
	 body="{\"handle\":\"$(BSKY_HANDLE)\",\"password\":\"$(BSKY_PASSWORD)\"}"; \
	 if [ -n "$(BSKY_HOST)" ]; then body="{\"handle\":\"$(BSKY_HANDLE)\",\"password\":\"$(BSKY_PASSWORD)\",\"host\":\"$(BSKY_HOST)\"}"; fi; \
	 echo "Authenticating Bluesky handle=$(BSKY_HANDLE)..."; \
	 curl -sf -X POST $(LINKARI_BASE)/auth/bluesky \
	   -H "Authorization: Bearer $$tok" \
	   -H "Content-Type: application/json" \
	   -d "$$body" | python3 -m json.tool
	@echo "OK: Bluesky auth submitted - restart linkari serve and watch for: source_start source=bsky_firehose"

# Exchange a Google ID token for a Linkari session token.
# The ID token comes from Google Sign-In on the Android app (or any Google Sign-In client).
# Usage: make auth-google GOOGLE_ID_TOKEN=<id_token_from_sign_in>
# On success, prints the Linkari session token  -  store it for use with auth-bluesky.
GOOGLE_ID_TOKEN ?=

auth-google:
	@test -n "$(GOOGLE_ID_TOKEN)" || { \
	  echo "ERROR: GOOGLE_ID_TOKEN unset"; \
	  echo "   Get it from the Android app's Google Sign-In flow, then:"; \
	  echo "   make auth-google GOOGLE_ID_TOKEN=<token>"; \
	  exit 1; }
	@echo "Exchanging Google ID token for Linkari session..."
	@curl -sf -X POST $(LINKARI_BASE)/auth/google \
	  -H "Content-Type: application/json" \
	  -d "{\"id_token\":\"$(GOOGLE_ID_TOKEN)\"}" | python3 -m json.tool
	@echo "OK: Copy session_token from above - use it with auth-bluesky"

serve-linkari:
	@echo "Starting linkari on :8080..."
	@test -n "$(AWS_PROFILE)" || { echo "ERROR: AWS_PROFILE unset (required to fetch $(AWS_BEARER_SECRET_ID))"; exit 1; }
	@tok=$$($(FETCH_TOKEN)) && test -n "$$tok" && \
		LINKARI_TOKEN=$$tok LINKARI_FIREBASE_SA=$(HOME)/.config/linkari/firebase-sa.json bin/linkari serve

serve-linkari-tls:
	@echo "Starting linkari on :8080 (TLS)..."
	@test -n "$(AWS_PROFILE)" || { echo "ERROR: AWS_PROFILE unset (required to fetch $(AWS_BEARER_SECRET_ID))"; exit 1; }
	@tok=$$($(FETCH_TOKEN)) && test -n "$$tok" && \
		LINKARI_TOKEN=$$tok LINKARI_FIREBASE_SA=$(HOME)/.config/linkari/firebase-sa.json bin/linkari serve --tls

logs-linkari:
	@test -n "$(AWS_PROFILE)" || { echo "ERROR: AWS_PROFILE unset"; exit 1; }
	@tok=$$($(FETCH_TOKEN)) && curl -sN "http://localhost:8080/logs/stream?token=$$tok"

logs-linkari-tls:
	@test -n "$(AWS_PROFILE)" || { echo "ERROR: AWS_PROFILE unset"; exit 1; }
	@tok=$$($(FETCH_TOKEN)) && curl -sN "https://localhost:8080/logs/stream?token=$$tok"

ts-go:
	@echo "Building ts-go..."
	@cd cmd/ts-go && go build $(LDFLAGS) -o ../../bin/ts-go .

install-ts-go:
	@echo "Installing ts-go -> $(INSTALL_DIR)/ts-go"
	@cd cmd/ts-go && go install $(LDFLAGS) .

test-ts-go:
	@echo "Running ts-go tests (requires C compiler for CGo)..."
	@cd cmd/ts-go && go test -count=1 ./...

# --- jira-poller targets ----------------------------------------------------

jira-poller:
	@echo "Building jira-poller..."
	@cd cmd/jira-poller && go build $(LDFLAGS) -o ../../bin/jira-poller .

install-jira-poller:
	@echo "Installing jira-poller -> $(INSTALL_DIR)/jira-poller"
	@cd cmd/jira-poller && go install $(LDFLAGS) .

run-jira-poller: jira-poller
	@echo "Running jira-poller..."
	@bin/jira-poller

lint-jira-poller:
	@echo "Linting jira-poller..."
	@cd cmd/jira-poller && golangci-lint run ./...

test-jira-poller:
	@echo "Testing jira-poller..."
	@cd cmd/jira-poller && go test -count=1 ./...

wasend:
	@echo "Building wasend..."
	@cd cmd/wasend && go build $(LDFLAGS) -o ../../bin/wasend .

install-wasend:
	@echo "Installing wasend -> $(INSTALL_DIR)/wasend"
	@cd cmd/wasend && go install $(LDFLAGS) .

workctl:
	@echo "Building workctl..."
	@cd cmd/workctl && go build $(LDFLAGS) -o ../../bin/workctl ./cmd/workctl

install-workctl:
	@echo "Installing workctl -> $(INSTALL_DIR)/workctl"
	@cd cmd/workctl && go install $(LDFLAGS) ./cmd/workctl

ghwatch:
	@echo "Building ghwatch..."
	@cd cmd/workctl && go build $(LDFLAGS) -o ../../bin/ghwatch ./cmd/ghwatch

install-ghwatch:
	@echo "Installing ghwatch -> $(INSTALL_DIR)/ghwatch"
	@cd cmd/workctl && go install $(LDFLAGS) ./cmd/ghwatch

runway:
	@echo "Building runway..."
	@cd cmd/runway && go build $(LDFLAGS) -o ../../bin/runway .

install-runway:
	@echo "Installing runway -> $(INSTALL_DIR)/runway"
	@cd cmd/runway && go install $(LDFLAGS) .

# --- Container image targets (EPIC-038 M9) ----------------------------------

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
	@echo "OK: Container images built (native platform)"

# Push multi-arch (linux/amd64 + linux/arm64) images to the registry.
# Requires: docker buildx with a builder that supports multi-arch (docker buildx create --use).
# Images are pushed directly  -  docker load does not support multi-arch manifests.
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
	@echo "OK: Multi-arch container images pushed to $(IMAGE_REGISTRY)"

# Start the Lima gVisor VM. First run takes ~5 minutes (Ubuntu + gVisor download).
# Uses infra/lima-gvisor.yaml for VM configuration.
lima-start:
	@echo "Starting Lima VM '$(LIMA_VM)'..."
	@limactl start infra/lima-gvisor.yaml --name $(LIMA_VM) || limactl start $(LIMA_VM)
	@echo "Waiting for containerd socket..."
	@limactl shell $(LIMA_VM) -- bash -c 'for i in $$(seq 30); do systemctl is-active containerd >/dev/null 2>&1 && break; sleep 2; done'
	@echo "OK: Lima VM '$(LIMA_VM)' running with gVisor"
	@limactl shell $(LIMA_VM) -- /opt/gvisor/runsc --version

# Run a smoke test inside the Lima VM: start a gVisor container and verify it runs.
# Skips automatically when the Lima socket is absent (CI without Lima installed).
lima-test:
	@if ! limactl list 2>/dev/null | grep -q $(LIMA_VM); then \
		echo "WARN: Lima VM '$(LIMA_VM)' not running - skipping lima-test"; \
		exit 0; \
	fi
	@echo "Running gVisor smoke test inside $(LIMA_VM)..."
	@limactl shell $(LIMA_VM) -- sudo nerdctl run --rm \
		--snapshotter=overlayfs \
		--runtime=runsc \
		alpine sh -c 'echo "gVisor OK: $$(uname -r)"'
	@echo "OK: gVisor smoke test passed"

# Run integration tests (requires Lima VM running with containers loaded).
# Use: make integration-test
integration-test: lima-test
	@echo "Running integration tests against Lima VM..."
	@cd cmd/linkari && LINKARI_RUNTIME_SOCKET=$(LIMA_SOCKET) \
		go test -v -tags=integration -run TestContainer ./...
	@echo "OK: Integration tests passed"

# -----------------------------------------------------------------------------
# docs-core S3 publish
# Pushes the local docs-core tree to the git-remote-s3 bundle repo at
# s3://REDACTED/repos/docs-core so CI can clone it via git-remote-s3.
#
# Usage:
#   make push-docs-artifact                   # push from ~/code/personal/docs
#   DOCS_CORE_PATH=/other/path make push-docs-artifact
#
# Requires:
#   - git-remote-s3 installed (pipx install git-remote-s3)
#   - AWS_PROFILE=brianonpoint (or default credentials with s3:PutObject on REDACTED-BUCKET)
# -----------------------------------------------------------------------------

DOCS_CORE_PATH  ?= $(HOME)/code/personal/docs
DOCS_CORE_S3    := s3://REDACTED/repos/docs-core
DOCS_PUSH_TMPDIR := /tmp/docs-core-push-$(shell date +%s)

.PHONY: push-docs-artifact
push-docs-artifact:
	@echo "→ Pushing docs-core to $(DOCS_CORE_S3)"
	@echo "  Source: $(DOCS_CORE_PATH)"
	@if [ ! -d "$(DOCS_CORE_PATH)/.git" ]; then \
		echo "ERROR: $(DOCS_CORE_PATH) is not a git repo"; exit 1; \
	fi
	@if ! command -v git-remote-s3 >/dev/null 2>&1; then \
		echo "ERROR: git-remote-s3 not found. Run: pipx install git-remote-s3"; exit 1; \
	fi
	@# Clone local docs-core, add S3 remote, push main
	@rm -rf "$(DOCS_PUSH_TMPDIR)" && git clone "$(DOCS_CORE_PATH)" "$(DOCS_PUSH_TMPDIR)" 2>&1
	@cd "$(DOCS_PUSH_TMPDIR)" && \
		git remote add s3 "$(DOCS_CORE_S3)" && \
		AWS_PROFILE=brianonpoint git push --force s3 main 2>&1
	@rm -rf "$(DOCS_PUSH_TMPDIR)"
	@echo "✓ docs-core pushed to $(DOCS_CORE_S3)"
	@echo "  SHA: $$(AWS_PROFILE=brianonpoint aws s3 ls $(DOCS_CORE_S3)/refs/heads/main/ | awk '{print $$4}' | sed 's/.bundle//')"
