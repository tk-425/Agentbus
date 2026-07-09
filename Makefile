BINARY = agentbus
INSTALL_PATH = /usr/local/bin/$(BINARY)

# Development version derived from the current git state, injected into the
# version surface at link time. `--always` falls back to the abbreviated commit
# when no tag is reachable; `|| echo dev` covers a non-git checkout. Released
# builds override this via GoReleaser's own ldflags.
VERSION = $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
VERSION_PKG = github.com/tk-425/agentbus/internal/version
LDFLAGS = -X $(VERSION_PKG).String=$(VERSION)

.PHONY: build install uninstall

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

install: build
	mv $(BINARY) $(INSTALL_PATH)

uninstall:
	rm $(INSTALL_PATH)
