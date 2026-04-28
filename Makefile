.PHONY: help test lint ci snapshot tools lsctltest integ

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test: ## Run unit tests
	go test ./...

integ: snapshot ## Run integration tests
	go run cmd/lsctltest/main.go --user-data test/data/dockerize-remote.sh --verbose

lint: ## Run golangci-lint
	golangci-lint run --timeout 5m0s --fix ./...

ci: lint test ## Run lint + test (used by CI)

snapshot: ## Build a local release snapshot via goreleaser
	goreleaser release --snapshot --clean --skip=publish

lsctltest: ## Build the integration test CLI
	go build -o dist/lsctltest ./cmd/lsctltest

tools: ## Install goreleaser and golangci-lint
	go install github.com/goreleaser/goreleaser/v2@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
