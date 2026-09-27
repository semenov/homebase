AGENT_FLAGS = CGO_ENABLED=0 GOOS=linux
LDFLAGS = -s -w
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
