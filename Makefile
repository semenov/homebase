AGENT_FLAGS = CGO_ENABLED=0 GOOS=linux
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS = -s -w -X github.com/semenov/ship/internal/cli.Version=$(VERSION)
BINDIR ?= $(HOME)/.local/bin

.PHONY: build agent install test clean

build: agent
	go build -ldflags="$(LDFLAGS)" -o dist/ship ./cmd/ship

agent:
	$(AGENT_FLAGS) GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o internal/agentbin/bin/shipd-linux-amd64 ./cmd/shipd
	$(AGENT_FLAGS) GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o internal/agentbin/bin/shipd-linux-arm64 ./cmd/shipd

install: build
	install -d $(BINDIR) && install -m 755 dist/ship $(BINDIR)/ship

test:
	go test ./...

clean:
	rm -rf dist internal/agentbin/bin/shipd-*
