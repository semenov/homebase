AGENT_FLAGS = CGO_ENABLED=0 GOOS=linux
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS = -s -w -X github.com/semenov/ship/internal/cli.Version=$(VERSION)
BINDIR ?= $(HOME)/.local/bin

.PHONY: build agent install test clean release

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

# release archives for GitHub releases / Homebrew: make release VERSION=0.1.0
PLATFORMS = darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

release: agent
	rm -rf dist/release && mkdir -p dist/release
	for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; dir=dist/release/ship_$${os}_$${arch}; \
		mkdir -p $$dir && \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags="$(LDFLAGS)" -o $$dir/ship ./cmd/ship && \
		cp LICENSE README.md $$dir/ && \
		tar -czf $$dir.tar.gz -C $$dir ship LICENSE README.md && rm -rf $$dir || exit 1; \
	done
	cd dist/release && shasum -a 256 *.tar.gz > checksums.txt
