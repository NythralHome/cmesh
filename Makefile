VERSION ?= 0.0.0-dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/cmesh/cmesh/internal/version.Version=$(VERSION) -X github.com/cmesh/cmesh/internal/version.Commit=$(COMMIT) -X github.com/cmesh/cmesh/internal/version.Date=$(DATE)

.PHONY: test run build dist clean docker worker-desktop-run worker-desktop-test worker-desktop-build help

help:
	@echo "Targets:"
	@echo "  make test      Run Go tests"
	@echo "  make run       Run the CMesh CLI"
	@echo "  make build     Build local binary"
	@echo "  make dist      Cross-compile release binaries"
	@echo "  make docker    Build Docker image"
	@echo "  make worker-desktop-run    Run the Flutter worker desktop app"
	@echo "  make worker-desktop-test   Test/analyze the Flutter worker desktop app"
	@echo "  make worker-desktop-build  Build the Flutter worker desktop app for this OS"

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

worker-desktop-run: build
	cd apps/worker_desktop && CMESH_WORKER_CONTROL_BIN="$(CURDIR)/bin/cmesh" fvm flutter run

worker-desktop-test:
	cd apps/worker_desktop && fvm flutter analyze && fvm flutter test

worker-desktop-build: build
	cd apps/worker_desktop && if [ "$(shell uname -s)" = "Darwin" ]; then \
		fvm flutter build macos; \
		cp "$(CURDIR)/bin/cmesh" "build/macos/Build/Products/Release/CMesh Worker.app/Contents/Resources/cmesh"; \
	elif [ "$(shell uname -s)" = "Linux" ]; then \
		fvm flutter build linux; \
		cp "$(CURDIR)/bin/cmesh" build/linux/*/release/bundle/cmesh; \
	else \
		echo "Unsupported desktop build host: $(shell uname -s)" >&2; exit 1; \
	fi

clean:
	rm -rf dist
