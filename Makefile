VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS = -s -w -X main.version=$(VERSION)

.PHONY: build install test clean release

build:
	go build -ldflags="$(LDFLAGS)" -o dist/homebase .

install:
	go install -ldflags="$(LDFLAGS)" .

test:
	go test ./...

clean:
	rm -rf dist

# release archives for GitHub releases / Homebrew: make release VERSION=0.1.0
PLATFORMS = darwin/arm64 darwin/amd64

release:
	rm -rf dist/release && mkdir -p dist/release
	for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; dir=dist/release/homebase_$${os}_$${arch}; \
		mkdir -p $$dir && \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags="$(LDFLAGS)" -o $$dir/homebase . && \
		cp LICENSE README.md $$dir/ && \
		tar -czf $$dir.tar.gz -C $$dir homebase LICENSE README.md && rm -rf $$dir || exit 1; \
	done
	cd dist/release && shasum -a 256 *.tar.gz > checksums.txt
