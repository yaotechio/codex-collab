# Default target builds for the current host (GOEXE auto-adds .exe on Windows).
BIN    := codexmcp$(shell go env GOEXE)
GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)

.PHONY: build test clean dist

build:
	go build -o $(BIN) .
	@echo "built $(BIN) for $(GOOS)/$(GOARCH)"

test:
	go test ./...

clean:
	rm -rf $(BIN) dist

# Cross-compile release binaries for all supported platforms into ./dist
dist:
	GOOS=linux   GOARCH=amd64 go build -o dist/codexmcp-linux-amd64       .
	GOOS=linux   GOARCH=arm64 go build -o dist/codexmcp-linux-arm64       .
	GOOS=darwin  GOARCH=amd64 go build -o dist/codexmcp-darwin-amd64      .
	GOOS=darwin  GOARCH=arm64 go build -o dist/codexmcp-darwin-arm64      .
	GOOS=windows GOARCH=amd64 go build -o dist/codexmcp-windows-amd64.exe .
	@echo "dist/ built"
