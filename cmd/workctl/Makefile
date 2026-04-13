# workctl Makefile
# Build and manage the workctl CLI tool

# Variables
BINARY_NAME=workctl
CMD_DIR=./cmd/workctl
BUILD_DIR=./bin
MAIN_FILE=$(CMD_DIR)

# ghwatch binary
GHWATCH_NAME=ghwatch
GHWATCH_CMD=./cmd/ghwatch

# Go commands
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=$(GOCMD) fmt
GOVET=$(GOCMD) vet

# --- Version injection ---
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
MODULE     := $(shell go list -m)

LDFLAGS := -s -w \
    -X '$(MODULE)/internal/version.Version=$(VERSION)' \
    -X '$(MODULE)/internal/version.Commit=$(COMMIT)' \
    -X '$(MODULE)/internal/version.BuildDate=$(BUILD_DATE)'

.PHONY: all build clean test fmt vet install run help build-ghwatch run-ghwatch ws-init ws-init-dry ws-test version standup-publish standup-dry

# Default target
all: fmt vet build

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_FILE)
	@echo "✅ Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Build for multiple platforms
build-all: build-linux build-darwin build-windows
	@echo "✅ Built for all platforms"

build-linux:
	@echo "Building for Linux..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 $(GOBUILD) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_FILE)

build-darwin:
	@echo "Building for macOS (Intel)..."
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_FILE)
	@echo "Building for macOS (Apple Silicon)..."
	GOOS=darwin GOARCH=arm64 $(GOBUILD) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_FILE)

build-windows:
	@echo "Building for Windows..."
	@mkdir -p $(BUILD_DIR)
	GOOS=windows GOARCH=amd64 $(GOBUILD) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(MAIN_FILE)

# Build ghwatch binary
build-ghwatch:
	@echo "Building $(GHWATCH_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(GHWATCH_NAME) $(GHWATCH_CMD)
	@echo "Build complete: $(BUILD_DIR)/$(GHWATCH_NAME)"

# Run ghwatch (build first)
run-ghwatch: build-ghwatch
	@$(BUILD_DIR)/$(GHWATCH_NAME) $(ARGS)

# Clean build artifacts
clean:
	@echo "Cleaning..."
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)
	rm -f $(BINARY_NAME)
	rm -f debug.log
	rm -f output_*.csv
	rm -f output/*.csv
	@echo "✅ Clean complete"

# Run tests
test:
	@echo "Running tests..."
	$(GOTEST) -v ./...

# Format code
fmt:
	@echo "Formatting code..."
	$(GOFMT) ./...

# Run go vet
vet:
	@echo "Running go vet..."
	$(GOVET) ./...

# Install dependencies
deps:
	@echo "Installing dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy

# Install binary to ~/.local/bin (XDG-compliant, symlink for easy rebuild)
install: build
	@mkdir -p $(HOME)/.local/bin
	@ln -sf $(CURDIR)/bin/$(BINARY_NAME) $(HOME)/.local/bin/$(BINARY_NAME)
	@echo "✅ Installed $(BINARY_NAME) → ~/.local/bin/$(BINARY_NAME)"

# Print the current version string (no build required)
version:
	@echo $(VERSION)

# Quick run (build and execute)
run: build
	@$(BUILD_DIR)/$(BINARY_NAME) $(ARGS)

# Development run with common flags
dev-run: build
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--email $(EMAIL) \
		--start $(START) \
		--end $(END) \
		--debug

# Example: Run user mode
example-user: build
	@echo "Example: Fetching user data..."
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--email user@example.com \
		--start 2025-01-01 \
		--end 2025-01-31 \
		--debug

# Example: Run project mode
example-project: build
	@echo "Example: Fetching project data..."
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "SR,ISRE" \
		--start 2025-01-01 \
		--end 2025-01-31 \
		--summary \
		--debug

# ========================================
# Common workctl shortcuts
# ========================================

# Variables for common date ranges
CURRENT_MONTH_START := $(shell date -u +%Y-%m-01)
CURRENT_MONTH_END := $(shell date -u +%Y-%m-%d)
LAST_MONTH_START := $(shell date -u -v-1m +%Y-%m-01)
LAST_MONTH_END := $(shell date -u -v-1m +%Y-%m-%d)
CURRENT_YEAR_START := $(shell date -u +%Y-01-01)
CURRENT_YEAR_END := $(shell date -u +%Y-12-31)

# Default project keys (override with PROJECTS="SR,ISRE,DATA")
PROJECTS ?= SR,ISRE
START ?= $(CURRENT_MONTH_START)
END ?= $(CURRENT_MONTH_END)

# Default user settings (override with EMAIL="user@example.com" GITHUB_USER="username")
EMAIL ?= $(shell git config user.email 2>/dev/null || echo "")
GITHUB_USER ?= $(shell git config github.user 2>/dev/null || echo "")

# Project mode: Fetch Jira issues by project keys
projects: build
	@echo "Fetching Jira issues for projects: $(PROJECTS)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--start $(START) \
		--end $(END) \
		--confluence=false \
		--summary

# Project mode with Confluence pages
projects-full: build
	@echo "Fetching Jira + Confluence for projects: $(PROJECTS)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--start $(START) \
		--end $(END) \
		--confluence=true \
		--summary

# Current month data (like your example)
current-month: build
	@echo "Fetching current month data for projects: $(PROJECTS)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--start $(CURRENT_MONTH_START) \
		--end $(CURRENT_MONTH_END) \
		--confluence=true \
		--summary

# Last month data
last-month: build
	@echo "Fetching last month data for projects: $(PROJECTS)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--start $(LAST_MONTH_START) \
		--end $(LAST_MONTH_END) \
		--confluence=true \
		--summary

# Current year data
current-year: build
	@echo "Fetching current year data for projects: $(PROJECTS)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--start $(CURRENT_YEAR_START) \
		--end $(CURRENT_YEAR_END) \
		--confluence=true \
		--summary

# User mode: Fetch by email
user: build
	@echo "Fetching user data for: $(EMAIL)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--email $(EMAIL) \
		--start $(START) \
		--end $(END) \
		--summary

# Space mode: Fetch Confluence pages by space keys
spaces: build
	@echo "Fetching Confluence pages for spaces: $(SPACES)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--space-keys "$(SPACES)" \
		--start $(START) \
		--end $(END) \
		--jira=false \
		--summary

# Mixed mode: Both projects and spaces
mixed: build
	@echo "Fetching projects: $(PROJECTS), spaces: $(SPACES)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--space-keys "$(SPACES)" \
		--start $(START) \
		--end $(END) \
		--summary

# Filtered queries
projects-done: build
	@echo "Fetching done issues for projects: $(PROJECTS)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--start $(START) \
		--end $(END) \
		--jira-status "Done" \
		--confluence=false \
		--summary

projects-in-progress: build
	@echo "Fetching in-progress issues for projects: $(PROJECTS)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--start $(START) \
		--end $(END) \
		--jira-status "In Progress" \
		--confluence=false \
		--summary

projects-high-priority: build
	@echo "Fetching high priority issues for projects: $(PROJECTS)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--start $(START) \
		--end $(END) \
		--jira-priority "High,Critical" \
		--confluence=false \
		--summary

projects-bugs: build
	@echo "Fetching bugs for projects: $(PROJECTS)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--start $(START) \
		--end $(END) \
		--jira-type "Bug" \
		--confluence=false \
		--summary

# Debug mode with verbose output
projects-debug: build
	@echo "Fetching projects (debug mode): $(PROJECTS)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--start $(START) \
		--end $(END) \
		--confluence=true \
		--summary \
		--debug

# Quick query (no summary)
quick: build
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--start $(START) \
		--end $(END) \
		--confluence=false

# ========================================
# 🎯 Common Use Cases (Summary Only)
# ========================================

# 1. Weekly Status - Last 7 days activity (all sources)
status: build
	@echo "📊 Weekly Status (Last 7 Days)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--space-keys "$(PROJECTS)" \
		--github-user "$(GITHUB_USER)" \
		--start $(shell fish -c 'daysago 7') \
		--end $(shell fish -c 'nowdate') \
		--jiraoutput /dev/null \
		--confluenceoutput /dev/null \
		--githuboutput /dev/null \
		--summary

# 2. Daily Standup - Yesterday + today (quick view)
standup: build
	@echo "🗓️  Daily Standup"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--github-user "$(GITHUB_USER)" \
		--confluence=false \
		--start $(shell fish -c 'daysago 1') \
		--end $(shell fish -c 'nowdate') \
		--jiraoutput /dev/null \
		--githuboutput /dev/null \
		--summary

# 3. Sprint Review - Last 2 weeks (typical sprint length)
sprint: build
	@echo "🏃 Sprint Review (Last 2 Weeks)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--space-keys "$(PROJECTS)" \
		--github-user "$(GITHUB_USER)" \
		--start $(shell fish -c 'daysago 14') \
		--end $(shell fish -c 'nowdate') \
		--jiraoutput /dev/null \
		--confluenceoutput /dev/null \
		--githuboutput /dev/null \
		--summary

# 4. Monthly Review - Current month activity
monthly: build
	@echo "📅 Monthly Review ($(shell fish -c 'date +%B %Y'))"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--project-keys "$(PROJECTS)" \
		--space-keys "$(PROJECTS)" \
		--github-user "$(GITHUB_USER)" \
		--start $(CURRENT_MONTH_START) \
		--end $(CURRENT_MONTH_END) \
		--jiraoutput /dev/null \
		--confluenceoutput /dev/null \
		--githuboutput /dev/null \
		--summary

# 5. My Work - Personal activity (requires EMAIL and GITHUB_USER)
my-work: build
	@echo "👤 My Work: $(EMAIL)"
	@$(BUILD_DIR)/$(BINARY_NAME) \
		--email "$(EMAIL)" \
		--github-user "$(GITHUB_USER)" \
		--start $(START) \
		--end $(END) \
		--jiraoutput /dev/null \
		--confluenceoutput /dev/null \
		--githuboutput /dev/null \
		--summary

# ========================================
# Standup publisher (EPIC-014)
# ========================================

STANDUP_SPACE_KEY ?=
STANDUP_FOLDER_ID ?=
STANDUP_AUTHOR    ?=
STANDUP_NOTES     ?=

# Publish weekly standup to Confluence.
# Requires STANDUP_SPACE_KEY and STANDUP_FOLDER_ID (or set in config file defaults).
# Example: make standup-publish STANDUP_SPACE_KEY=~429790461 STANDUP_FOLDER_ID=4379148291
standup-publish: build
	@echo "Publishing standup to Confluence..."
	@$(BUILD_DIR)/$(BINARY_NAME) weekly --publish \
		$(if $(STANDUP_SPACE_KEY),--confluence-space-key "$(STANDUP_SPACE_KEY)") \
		$(if $(STANDUP_FOLDER_ID),--confluence-folder-id "$(STANDUP_FOLDER_ID)") \
		$(if $(STANDUP_AUTHOR),--standup-author "$(STANDUP_AUTHOR)") \
		$(if $(STANDUP_NOTES),--standup-notes "$(STANDUP_NOTES)")

# Dry-run: render standup HTML to stdout without publishing.
standup-dry: build
	@echo "Rendering standup HTML (dry-run)..."
	@$(BUILD_DIR)/$(BINARY_NAME) weekly --dry-run \
		$(if $(STANDUP_AUTHOR),--standup-author "$(STANDUP_AUTHOR)") \
		$(if $(STANDUP_NOTES),--standup-notes "$(STANDUP_NOTES)")

# ========================================
# Workspace commands
# ========================================

# Default Jira key (override with JIRA_KEY="ISRE-1234")
JIRA_KEY ?=

# Initialize a workspace for a Jira issue
ws-init: build
	@if [ -z "$(JIRA_KEY)" ]; then echo "Error: JIRA_KEY is required (e.g., make ws-init JIRA_KEY=ISRE-1234)"; exit 1; fi
	@echo "Initializing workspace for $(JIRA_KEY)..."
	@$(BUILD_DIR)/$(BINARY_NAME) workspace init $(JIRA_KEY)

# Dry-run workspace init (preview without side effects)
ws-init-dry: build
	@if [ -z "$(JIRA_KEY)" ]; then echo "Error: JIRA_KEY is required (e.g., make ws-init-dry JIRA_KEY=ISRE-1234)"; exit 1; fi
	@echo "Dry-run workspace init for $(JIRA_KEY)..."
	@$(BUILD_DIR)/$(BINARY_NAME) workspace init $(JIRA_KEY) --dry-run

# Run workspace package tests
ws-test:
	@echo "Running workspace tests..."
	$(GOTEST) ./internal/workspace/ -v -count=1

# Check code quality
lint: fmt vet
	@echo "✅ Linting complete"

# Display help
help:
	@echo "Makefile for $(BINARY_NAME)"
	@echo ""
	@echo "Usage:"
	@echo "  make [target] [VARIABLES]"
	@echo ""
	@echo "Build Targets:"
	@echo "  all              Format, vet, and build (default)"
	@echo "  build            Build the binary"
	@echo "  build-all        Build for Linux, macOS, and Windows"
	@echo "  clean            Remove build artifacts"
	@echo "  test             Run tests"
	@echo "  fmt              Format Go code"
	@echo "  vet              Run go vet"
	@echo "  deps             Install/update dependencies"
	@echo "  install          Install binary to \$$GOPATH/bin"
	@echo "  run              Build and run (use ARGS=\"--flag value\")"
	@echo "  lint             Run fmt and vet"
	@echo ""
	@echo "🎯 Common Use Cases (Summary Only - No CSV):"
	@echo "  status           Weekly status (last 7 days, all sources)"
	@echo "  standup          Daily standup (yesterday + today)"
	@echo "  sprint           Sprint review (last 2 weeks)"
	@echo "  monthly          Monthly review (current month)"
	@echo "  my-work          Personal work (requires EMAIL, GITHUB_USER)"
	@echo ""
	@echo "Query Targets (With CSV Export):"
	@echo "  projects         Fetch Jira issues by project keys"
	@echo "  projects-full    Fetch Jira + Confluence data"
	@echo "  current-month    Fetch current month data"
	@echo "  last-month       Fetch last month data"
	@echo "  current-year     Fetch current year data"
	@echo "  user             Fetch by user email"
	@echo "  spaces           Fetch Confluence pages by space keys"
	@echo "  mixed            Fetch both projects and spaces"
	@echo "  quick            Quick query without summary"
	@echo ""
	@echo "Filtered Query Targets:"
	@echo "  projects-done            Fetch done issues"
	@echo "  projects-in-progress     Fetch in-progress issues"
	@echo "  projects-high-priority   Fetch high/critical priority issues"
	@echo "  projects-bugs            Fetch bugs only"
	@echo "  projects-debug           Run with debug logging"
	@echo ""
	@echo "Workspace Targets:"
	@echo "  ws-init          Initialize workspace for a Jira issue (requires JIRA_KEY)"
	@echo "  ws-init-dry      Dry-run workspace init (preview only)"
	@echo "  ws-test          Run workspace package tests"
	@echo ""
	@echo "Variables:"
	@echo "  PROJECTS         Project keys (default: SR,ISRE)"
	@echo "  SPACES           Space keys (no default)"
	@echo "  EMAIL            User email (default: from git config)"
	@echo "  GITHUB_USER      GitHub username (default: from git config)"
	@echo "  START            Start date (default: current month start)"
	@echo "  END              End date (default: today)"
	@echo ""
	@echo "Examples (Common Use Cases):"
	@echo "  make status                              # Weekly status"
	@echo "  make standup                             # Daily standup"
	@echo "  make sprint                              # Sprint review"
	@echo "  make monthly                             # Monthly review"
	@echo "  make my-work EMAIL=user@example.com GITHUB_USER=username"
	@echo ""
	@echo "Examples (Advanced Queries):"
	@echo "  make projects PROJECTS=\"SR,ISRE,DATA\""
	@echo "  make current-month PROJECTS=\"SR,ISRE\""
	@echo "  make projects START=2025-12-01 END=2025-12-31"
	@echo "  make user EMAIL=user@example.com"
	@echo "  make mixed PROJECTS=\"SR,ISRE\" SPACES=\"ENG,INFRA\""
	@echo "  make projects-done START=2025-01-01 END=2025-01-31"
	@echo ""
	@echo "Examples (Workspace):"
	@echo "  make ws-init JIRA_KEY=ISRE-1234            # Init workspace"
	@echo "  make ws-init-dry JIRA_KEY=ISRE-1234        # Preview workspace init"
	@echo "  make ws-test                               # Run workspace tests"
