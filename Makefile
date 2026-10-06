# notif — Markdown notifications for Slack and Telegram.
#
#   make            build both binaries (release)
#   make install    put them on your PATH
#   make check      fmt, clippy and tests, the way CI would
#   make help       every target, with a description

CARGO      ?= cargo
PREFIX     ?= $(HOME)/.cargo
SKILLS_DIR ?= $(HOME)/.claude/skills
ROOT       := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))

CRATES  := notif-slack notif-tg
BINS    := $(CRATES)
TARGET  := $(ROOT)/target/release

.DEFAULT_GOAL := build
.PHONY: all build debug $(CRATES) test lint fmt fmt-check check install uninstall skills unskills clean distclean help

all: build

build: ## Build both binaries in release mode
	$(CARGO) build --release --workspace

debug: ## Build both binaries in debug mode
	$(CARGO) build --workspace

notif-slack: ## Build only the Slack binary
	$(CARGO) build --release --package notif-slack

notif-tg: ## Build only the Telegram binary
	$(CARGO) build --release --package notif-tg

test: ## Run the test suite
	$(CARGO) test --workspace

lint: ## Run clippy, treating warnings as errors
	$(CARGO) clippy --workspace --all-targets -- -D warnings

fmt: ## Format the source
	$(CARGO) fmt --all

fmt-check: ## Fail if the source is not formatted
	$(CARGO) fmt --all -- --check

check: fmt-check lint test ## Everything CI would run

install: ## Install both binaries into PREFIX/bin (default ~/.cargo/bin)
	@for c in $(CRATES); do \
		echo "installing $$c -> $(PREFIX)/bin/$$c"; \
		$(CARGO) install --path "$(ROOT)/$$c" --root "$(PREFIX)" --locked --force || exit 1; \
	done

uninstall: ## Remove both binaries from PREFIX/bin
	@for c in $(CRATES); do \
		$(CARGO) uninstall --root "$(PREFIX)" "$$c" || true; \
	done

skills: ## Symlink the Claude Code skills into SKILLS_DIR
	@mkdir -p "$(SKILLS_DIR)"
	@ln -sfn "$(ROOT)/notif-slack/skills/slack-post" "$(SKILLS_DIR)/slack-post"
	@ln -sfn "$(ROOT)/notif-tg/skills/telegram-messages" "$(SKILLS_DIR)/telegram-messages"
	@echo "linked slack-post and telegram-messages into $(SKILLS_DIR)"

unskills: ## Remove the skill symlinks
	@rm -f "$(SKILLS_DIR)/slack-post" "$(SKILLS_DIR)/telegram-messages"
	@echo "removed slack-post and telegram-messages from $(SKILLS_DIR)"

clean: ## Remove build artifacts
	$(CARGO) clean

distclean: clean uninstall unskills ## Remove artifacts, installed binaries and skill links

help: ## Show this help
	@echo "notif — targets:"
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[1m%-12s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "variables: CARGO=$(CARGO) PREFIX=$(PREFIX) SKILLS_DIR=$(SKILLS_DIR)"
