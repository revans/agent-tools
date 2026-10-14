# agent-tools — CLIs for agents: notif-slack, notif-tg (Rust) and wharf (Go).
#
#   make            build every binary (release)
#   make install    put them on your PATH
#   make check      fmt, clippy and tests, the way CI would
#   make help       every target, with a description

CARGO      ?= cargo
PREFIX     ?= $(HOME)/.cargo
SKILLS_DIR ?= $(HOME)/.claude/skills
ROOT       := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))

CRATES  := notif-slack notif-tg
GO      ?= go
BINS    := $(CRATES) wharf
TARGET  := $(ROOT)/target/release

.DEFAULT_GOAL := build
.PHONY: all build debug $(CRATES) wharf test-wharf install-wharf uninstall-wharf test lint fmt fmt-check check install uninstall skills unskills clean distclean help

all: build

build: ## Build every binary in release mode
	$(CARGO) build --release --workspace
	$(MAKE) wharf

debug: ## Build every binary in debug mode
	$(CARGO) build --workspace

notif-slack: ## Build only the Slack binary
	$(CARGO) build --release --package notif-slack

notif-tg: ## Build only the Telegram binary
	$(CARGO) build --release --package notif-tg

wharf: ## Build only the wharf (SFTP deploy) binary
	@mkdir -p "$(TARGET)"
	cd "$(ROOT)/wharf" && $(GO) build -o "$(TARGET)/wharf" .

test-wharf: ## Vet and test wharf
	cd "$(ROOT)/wharf" && $(GO) vet ./... && $(GO) test ./...

test: test-wharf ## Run the test suite
	$(CARGO) test --workspace

lint: ## Run clippy, treating warnings as errors
	$(CARGO) clippy --workspace --all-targets -- -D warnings

fmt: ## Format the source
	$(CARGO) fmt --all

fmt-check: ## Fail if the source is not formatted
	$(CARGO) fmt --all -- --check

check: fmt-check lint test ## Everything CI would run

install: install-wharf ## Install every binary into PREFIX/bin (default ~/.cargo/bin)
	@for c in $(CRATES); do \
		echo "installing $$c -> $(PREFIX)/bin/$$c"; \
		$(CARGO) install --path "$(ROOT)/$$c" --root "$(PREFIX)" --locked --force || exit 1; \
	done

install-wharf: wharf ## Install wharf into PREFIX/bin
	@mkdir -p "$(PREFIX)/bin"
	install -m 0755 "$(TARGET)/wharf" "$(PREFIX)/bin/wharf"
	@echo "installed wharf -> $(PREFIX)/bin/wharf"

uninstall-wharf: ## Remove wharf from PREFIX/bin
	@rm -f "$(PREFIX)/bin/wharf"

uninstall: ## Remove every binary from PREFIX/bin
	@for c in $(CRATES); do \
		$(CARGO) uninstall --root "$(PREFIX)" "$$c" || true; \
	done
	@$(MAKE) uninstall-wharf

skills: ## Symlink the Claude Code skills into SKILLS_DIR
	@mkdir -p "$(SKILLS_DIR)"
	@ln -sfn "$(ROOT)/notif-slack/skills/slack-post" "$(SKILLS_DIR)/slack-post"
	@ln -sfn "$(ROOT)/notif-tg/skills/telegram-messages" "$(SKILLS_DIR)/telegram-messages"
	@ln -sfn "$(ROOT)/wharf/skills/wharf-deploy" "$(SKILLS_DIR)/wharf-deploy"
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
	@echo "variables: CARGO=$(CARGO) PREFIX=$(PREFIX) SKILLS_DIR=$(SKILLS_DIR)"
