# Build and install the docli command-line tool.
#
#   make              build ./docli
#   make test         vet and run the tests (a fake DigitalOcean API only; nothing touches the real one)
#   make install      build, then copy it to ~/.local/bin (no sudo)
#   make uninstall    remove the installed copy
#   make dist         release archives for six platforms, with SHA256SUMS, in dist/
#   make clean        remove ./docli and dist/
#
# Install somewhere else:  make install PREFIX=/usr/local   (probably needs sudo)
#                          make install BINDIR=$HOME/bin
# Packagers can stage with DESTDIR=/some/root.

BINARY  := docli
PREFIX  ?= $(HOME)/.local
BINDIR  ?= $(PREFIX)/bin
GO      ?= go
# The version comes from the nearest v* git tag (v0.1.0 -> 0.1.0), or "dev" outside a tagged checkout.
VERSION ?= $(shell git describe --tags --match 'v*' --always --dirty 2>/dev/null | sed 's/^v//' || true)
VERSION := $(or $(VERSION),dev)
# -trimpath drops the builder's file paths from the binary; -s -w drop debug tables (a smaller file).
GOFLAGS_BUILD := -trimpath -ldflags "-s -w -X main.version=$(VERSION)"

# What `make dist` builds: GOOS/GOARCH pairs. Every one is a static binary with no C dependencies.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
SHA256    := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo shasum -a 256)

.PHONY: all build test install uninstall clean help dist

all: build

help:
	@sed -n '3,12p' $(firstword $(MAKEFILE_LIST)) | sed 's/^# \{0,1\}//'

build:
	@command -v $(GO) >/dev/null 2>&1 || { echo "Go is required to build $(BINARY): https://go.dev/dl/" >&2; exit 1; }
	$(GO) build $(GOFLAGS_BUILD) -o $(BINARY) .

test:
	$(GO) vet ./...
	@test -z "$$(gofmt -l .)" || { echo "gofmt: these files need formatting:"; gofmt -l .; exit 1; }
	$(GO) test -race ./...

install: build
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 $(BINARY) "$(DESTDIR)$(BINDIR)/$(BINARY)"
	@echo "Installed $(DESTDIR)$(BINDIR)/$(BINARY)"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; *) \
		echo ""; \
		echo "Note: $(BINDIR) is not on your PATH. Add this to your shell profile:"; \
		echo "  export PATH=\"$(BINDIR):\$$PATH\"";; esac
	@echo ""
	@echo "Next: put your token in DIGITALOCEAN_TOKEN (or ~/.config/docli/token, mode 0600), then: $(BINARY) token check"

uninstall:
	rm -f "$(DESTDIR)$(BINDIR)/$(BINARY)"
	@echo "Removed $(DESTDIR)$(BINDIR)/$(BINARY)"

# One archive per platform (tar.gz, or zip for Windows) holding the binary, named docli_<version>_<os>_<arch>,
# plus SHA256SUMS. CGO is off so the binaries need nothing installed.
dist:
	@command -v $(GO) >/dev/null 2>&1 || { echo "Go is required: https://go.dev/dl/" >&2; exit 1; }
	rm -rf dist && mkdir -p dist
	@set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		name=$(BINARY)_$(VERSION)_$${os}_$${arch}; \
		bin=$(BINARY); [ "$$os" = windows ] && bin=$(BINARY).exe; \
		echo "building $$name"; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build $(GOFLAGS_BUILD) -o dist/$$name/$$bin .; \
		cp README.md dist/$$name/; \
		if [ "$$os" = windows ]; then (cd dist && zip -qr $$name.zip $$name); \
		else tar -C dist -czf dist/$$name.tar.gz $$name; fi; \
		rm -rf dist/$$name; \
	done
	cd dist && $(SHA256) $(BINARY)_* > SHA256SUMS
	@echo ""; ls -1 dist

clean:
	rm -rf $(BINARY) dist
