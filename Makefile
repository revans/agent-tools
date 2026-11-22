# agent-tools — CLIs for agents. Rust: notif-slack, notif-tg. Go: wharf, docli,
# reddit (and the other API tools), plus agent-tools-mcp, the MCP server.
#
#   make            build every binary (release)
#   make install    put them on your PATH
#   make check      fmt, clippy and tests, the way CI would
#   make help       every target, with a description
#
# To add a Go tool: put it in tools/NAME with a go.mod and add NAME to GO_TOOLS.
# Build, test, install and uninstall targets (wharf, test-wharf, install-wharf,
# uninstall-wharf, ...) then exist for it. A tool that needs special build
# flags sets GOFLAGS_NAME.

CARGO      ?= cargo
PREFIX     ?= $(HOME)/.cargo
SKILLS_DIR ?= $(HOME)/.claude/skills
ROOT       := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))
TOOLS      := $(ROOT)/tools
LIBS       := $(ROOT)/libs

CRATES   := notif-slack notif-tg
GO       ?= go
GO_TOOLS := wharf docli reddit youtube linkedin instagram
TARGET   := $(ROOT)/target/release

DOCLI_VERSION ?= $(or $(shell git describe --tags --match 'v*' --always --dirty 2>/dev/null | sed 's/^v//'),dev)
GOFLAGS_docli := -trimpath -ldflags "-s -w -X main.version=$(DOCLI_VERSION)"

.DEFAULT_GOAL := build

.PHONY: all build debug go-tools $(CRATES) $(GO_TOOLS) \
        $(addprefix test-,$(GO_TOOLS)) $(addprefix install-,$(GO_TOOLS)) $(addprefix uninstall-,$(GO_TOOLS)) \
        test-libs mcp test-mcp install-mcp uninstall-mcp mcp-register mcp-unregister \
        test lint fmt fmt-check check install uninstall skills unskills clean distclean help

all: build

build: ## Build every binary in release mode
	$(CARGO) build --release --workspace
	$(MAKE) go-tools mcp

go-tools: $(GO_TOOLS) ## Build every Go tool (wharf, docli, reddit, ...)

debug: ## Build every binary in debug mode
	$(CARGO) build --workspace

notif-slack: ## Build only the Slack binary
	$(CARGO) build --release --package notif-slack

notif-tg: ## Build only the Telegram binary
	$(CARGO) build --release --package notif-tg

# One Go tool: make NAME builds it, test-NAME vets and tests it,
# install-NAME and uninstall-NAME manage it in PREFIX/bin.
$(GO_TOOLS):
	@mkdir -p "$(TARGET)"
	cd "$(TOOLS)/$@" && $(GO) build $(GOFLAGS_$@) -o "$(TARGET)/$@" .

$(addprefix test-,$(GO_TOOLS)): test-%:
	cd "$(TOOLS)/$*" && $(GO) vet ./... && $(GO) test ./...

$(addprefix install-,$(GO_TOOLS)): install-%: %
	@mkdir -p "$(PREFIX)/bin"
	install -m 0755 "$(TARGET)/$*" "$(PREFIX)/bin/$*"
	@echo "installed $* -> $(PREFIX)/bin/$*"

$(addprefix uninstall-,$(GO_TOOLS)): uninstall-%:
	@rm -f "$(PREFIX)/bin/$*"

test-libs: ## Vet and test the shared Go libraries (libs/; Rust crates there are covered by cargo test)
	@for d in $(LIBS)/*/; do \
		[ -f "$$d/go.mod" ] || continue; \
		echo "testing $$d"; \
		(cd "$$d" && $(GO) vet ./... && $(GO) test ./...) || exit 1; \
	done

mcp: ## Build only the MCP server (agent-tools-mcp)
	@mkdir -p "$(TARGET)"
	cd "$(ROOT)/mcp" && $(GO) build -o "$(TARGET)/agent-tools-mcp" .

test-mcp: ## Vet and test the MCP server
	cd "$(ROOT)/mcp" && $(GO) vet ./... && $(GO) test ./...

install-mcp: mcp ## Install agent-tools-mcp into PREFIX/bin
	@mkdir -p "$(PREFIX)/bin"
	install -m 0755 "$(TARGET)/agent-tools-mcp" "$(PREFIX)/bin/agent-tools-mcp"
	@echo "installed agent-tools-mcp -> $(PREFIX)/bin/agent-tools-mcp"

uninstall-mcp: ## Remove agent-tools-mcp from PREFIX/bin
	@rm -f "$(PREFIX)/bin/agent-tools-mcp"

mcp-register: install ## Register the MCP server with whichever of claude, codex and grok are installed
	@for c in claude codex grok; do \
		if command -v $$c >/dev/null 2>&1; then \
			if [ $$c = claude ]; then $$c mcp add --scope user agent-tools -- "$(PREFIX)/bin/agent-tools-mcp"; \
			else $$c mcp add agent-tools -- "$(PREFIX)/bin/agent-tools-mcp"; fi \
			&& echo "registered with $$c" || echo "could not register with $$c (already added? try make mcp-unregister)"; \
		else echo "$$c not installed, skipped"; fi; \
	done

mcp-unregister: ## Remove the MCP server from claude, codex and grok
	@for c in claude codex grok; do \
		if command -v $$c >/dev/null 2>&1; then $$c mcp remove agent-tools >/dev/null 2>&1 && echo "removed from $$c" || echo "not registered with $$c"; fi; \
	done

test: $(addprefix test-,$(GO_TOOLS)) test-libs test-mcp ## Run the test suite
	$(CARGO) test --workspace

lint: ## Run clippy, treating warnings as errors
	$(CARGO) clippy --workspace --all-targets -- -D warnings

fmt: ## Format the source
	$(CARGO) fmt --all

fmt-check: ## Fail if the source is not formatted
	$(CARGO) fmt --all -- --check

check: fmt-check lint test ## Everything CI would run

install: $(addprefix install-,$(GO_TOOLS)) install-mcp ## Install every binary into PREFIX/bin (default ~/.cargo/bin)
	@for c in $(CRATES); do \
		echo "installing $$c -> $(PREFIX)/bin/$$c"; \
		$(CARGO) install --path "$(TOOLS)/$$c" --root "$(PREFIX)" --locked --force || exit 1; \
	done

uninstall: $(addprefix uninstall-,$(GO_TOOLS)) uninstall-mcp ## Remove every binary from PREFIX/bin
	@for c in $(CRATES); do \
		$(CARGO) uninstall --root "$(PREFIX)" "$$c" || true; \
	done

skills: ## Symlink the Claude Code skills into SKILLS_DIR
	@mkdir -p "$(SKILLS_DIR)"
	@ln -sfn "$(TOOLS)/notif-slack/skills/slack-post" "$(SKILLS_DIR)/slack-post"
	@ln -sfn "$(TOOLS)/notif-tg/skills/telegram-messages" "$(SKILLS_DIR)/telegram-messages"
	@ln -sfn "$(TOOLS)/wharf/skills/wharf-deploy" "$(SKILLS_DIR)/wharf-deploy"
	@echo "linked slack-post, telegram-messages and wharf-deploy into $(SKILLS_DIR)"

unskills: ## Remove the skill symlinks
	@rm -f "$(SKILLS_DIR)/slack-post" "$(SKILLS_DIR)/telegram-messages" "$(SKILLS_DIR)/wharf-deploy"
	@echo "removed slack-post, telegram-messages and wharf-deploy from $(SKILLS_DIR)"

clean: ## Remove build artifacts
	$(CARGO) clean

distclean: clean uninstall unskills ## Remove artifacts, installed binaries and skill links

help: ## Show this help
	@echo "agent-tools — targets:"
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[1m%-12s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "Go tools: $(GO_TOOLS)  (make NAME, test-NAME, install-NAME, uninstall-NAME)"
	@echo "variables: CARGO=$(CARGO) PREFIX=$(PREFIX) SKILLS_DIR=$(SKILLS_DIR)"
