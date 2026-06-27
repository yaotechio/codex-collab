# Default target builds for the current host (GOEXE auto-adds .exe on Windows).
BIN     := codex-collab$(shell go env GOEXE)
GOOS    := $(shell go env GOOS)
GOARCH  := $(shell go env GOARCH)
VERSION ?= dev
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build test vet fmt clean dist npm

build:
	go build $(LDFLAGS) -o $(BIN) .
	@echo "built $(BIN) ($(VERSION)) for $(GOOS)/$(GOARCH)"

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l .

clean:
	rm -rf $(BIN) dist

# Cross-compile release binaries for all supported platforms into ./dist
dist:
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o dist/codex-collab-linux-amd64       .
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o dist/codex-collab-linux-arm64       .
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o dist/codex-collab-darwin-amd64      .
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o dist/codex-collab-darwin-arm64      .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/codex-collab-windows-amd64.exe .
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build $(LDFLAGS) -o dist/codex-collab-windows-arm64.exe .
	@echo "dist/ built ($(VERSION))"

# Build the npm packages (main + per-platform binaries) into ./dist/npm.
# Pass VERSION=x.y.z; add PUBLISH=1 to also `npm publish` (needs npm auth).
npm:
	node scripts/build-npm.mjs --version $(VERSION) $(if $(filter 1,$(PUBLISH)),--publish,)
