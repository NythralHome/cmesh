VERSION ?= 0.0.0-dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/cmesh/cmesh/internal/version.Version=$(VERSION) -X github.com/cmesh/cmesh/internal/version.Commit=$(COMMIT) -X github.com/cmesh/cmesh/internal/version.Date=$(DATE)

.PHONY: test run build dist runtime-llamacpp runtime-llamacpp-stage runtime-llamacpp-stage-current clean docker worker-desktop-run worker-desktop-test worker-desktop-build release-dry-run release-candidate linux-production-release linux-sign-production-release linux-manager-install-smoke linux-worker-install-smoke linux-runtime-smoke linux-runbook-smoke linux-reliability-smoke linux-security-doc-smoke linux-observability-doc-smoke linux-backup-restore-smoke linux-production-docs-smoke linux-stable-release-smoke linux-fresh-user-validation-smoke linux-beta-deployment-e2e linux-production-launch-gate release-governance-smoke production-readiness deploy-alpha help

help:
	@echo "Targets:"
	@echo "  make test      Run Go tests"
	@echo "  make run       Run the CMesh CLI"
	@echo "  make build     Build local binary"
	@echo "  make dist      Cross-compile release binaries"
	@echo "  make runtime-llamacpp        Build pinned llama.cpp RPC runtime archive for this host"
	@echo "  make runtime-llamacpp-stage  Build pinned llama.cpp RPC + CMesh stage runtime archive for this host"
	@echo "  make runtime-llamacpp-stage-current  Prepare verified current Linux amd64 stage runtime artifact"
	@echo "  make docker    Build Docker image"
	@echo "  make worker-desktop-run    Run the Flutter worker desktop app"
	@echo "  make worker-desktop-test   Test/analyze the Flutter worker desktop app"
	@echo "  make worker-desktop-build  Build the Flutter worker desktop app for this OS"
	@echo "  make release-dry-run       Run local pre-release tests, builds, and smoke checks"
	@echo "  make release-candidate VERSION=v...  Prepare local release notes/checksums metadata"
	@echo "  make linux-production-release VERSION=v...  Prepare Linux-only production package"
	@echo "  make linux-sign-production-release CMESH_LINUX_PACKAGE_DIR=...  Sign Linux package artifacts"
	@echo "  make linux-manager-install-smoke  Verify packaged Linux manager install dry-run"
	@echo "  make linux-worker-install-smoke   Verify packaged Linux worker install dry-run"
	@echo "  make linux-runtime-smoke          Verify packaged Linux stage runtime artifact"
	@echo "  make linux-runbook-smoke          Verify Linux sliced production runbook"
	@echo "  make linux-reliability-smoke      Verify repeated local sliced reliability checks"
	@echo "  make linux-security-doc-smoke     Verify Linux public VPS security hardening doc"
	@echo "  make linux-observability-doc-smoke Verify Linux operator observability doc"
	@echo "  make linux-backup-restore-smoke   Verify Linux manager file-state backup/restore"
	@echo "  make linux-production-docs-smoke  Verify public Linux production guide"
	@echo "  make linux-stable-release-smoke CMESH_LINUX_PACKAGE_DIR=...  Verify signed Linux release"
	@echo "  make linux-fresh-user-validation-smoke CMESH_LINUX_PACKAGE_DIR=...  Verify fresh-user signed package flow"
	@echo "  make linux-beta-deployment-e2e CMESH_LINUX_PACKAGE_DIR=...  Run package-based AWS beta deployment proof"
	@echo "  make linux-production-launch-gate CMESH_LINUX_PACKAGE_DIR=...  Run final Linux production launch gate"
	@echo "  make release-governance-smoke Verify release license/governance docs"
	@echo "  make production-readiness  Run production readiness gate; set CMESH_RUN_AWS_E2E=true for AWS"
	@echo "  make deploy-alpha VERSION=v...  Deploy alpha after release assets are published"

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

runtime-llamacpp:
	scripts/build-llamacpp-runtime.sh

runtime-llamacpp-stage:
	CMESH_LLAMA_CPP_STAGE_RUNNER=true scripts/build-llamacpp-runtime.sh

runtime-llamacpp-stage-current:
	scripts/prepare-current-stage-runtime-artifact.sh

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

release-dry-run:
	CMESH_DRY_RUN_VERSION=$(VERSION) scripts/release-dry-run.sh

release-candidate:
	CMESH_RC_VERSION=$(VERSION) scripts/release-candidate.sh

linux-production-release:
	CMESH_LINUX_RELEASE_VERSION=$(VERSION) scripts/release-linux-production.sh

linux-sign-production-release:
	scripts/sign-linux-production-release.sh

linux-manager-install-smoke:
	scripts/linux-production-manager-install-smoke.sh

linux-worker-install-smoke:
	scripts/linux-production-worker-install-smoke.sh

linux-runtime-smoke:
	scripts/linux-production-runtime-smoke.sh

linux-runbook-smoke:
	scripts/linux-production-runbook-smoke.sh

linux-reliability-smoke:
	scripts/linux-production-reliability-smoke.sh

linux-security-doc-smoke:
	scripts/linux-production-security-doc-smoke.sh

linux-observability-doc-smoke:
	scripts/linux-production-observability-doc-smoke.sh

linux-backup-restore-smoke:
	scripts/linux-manager-backup-restore-smoke.sh

linux-production-docs-smoke:
	scripts/linux-production-docs-smoke.sh

linux-stable-release-smoke:
	scripts/linux-stable-release-smoke.sh

linux-fresh-user-validation-smoke:
	scripts/linux-fresh-user-validation-smoke.sh

linux-beta-deployment-e2e:
	scripts/linux-beta-deployment-e2e.sh

linux-production-launch-gate:
	scripts/linux-production-launch-gate.sh

release-governance-smoke:
	scripts/release-governance-smoke.sh

production-readiness:
	CMESH_READINESS_DIR=dist/production-readiness scripts/production-readiness-gate.sh

deploy-alpha:
	CMESH_VERSION=$(VERSION) scripts/deploy-alpha.sh

clean:
	rm -rf dist
