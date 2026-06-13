.PHONY: test run help

help:
	@echo "Targets:"
	@echo "  make test      Run Go tests"
	@echo "  make run       Run the CMesh CLI"

test:
	go test ./...

run:
	go run ./cmd/cmesh

