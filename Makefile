VERSION ?= 0.0.0-dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/cmesh/cmesh/internal/version.Version=$(VERSION) -X github.com/cmesh/cmesh/internal/version.Commit=$(COMMIT) -X github.com/cmesh/cmesh/internal/version.Date=$(DATE)

.PHONY: test run build dist clean docker help

help:
	@echo "Targets:"
	@echo "  make test      Run Go tests"
	@echo "  make run       Run the CMesh CLI"
	@echo "  make build     Build local binary"
	@echo "  make dist      Cross-compile release binaries"
	@echo "  make docker    Build Docker image"

test:
	go test ./...

run:
	go run ./cmd/cmesh

build:
	mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/cmesh ./cmd/cmesh

dist: clean
	mkdir -p dist
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/cmesh-darwin-arm64 ./cmd/cmesh
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/cmesh-darwin-amd64 ./cmd/cmesh
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/cmesh-linux-amd64 ./cmd/cmesh
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/cmesh-linux-arm64 ./cmd/cmesh
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/cmesh-windows-amd64.exe ./cmd/cmesh

docker:
	docker build -t cmesh:$(VERSION) .

clean:
	rm -rf dist
