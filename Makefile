.PHONY: help test lint ci snapshot tools lsctltest integ regions-snapshot

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

regions-snapshot: ## Refresh pkg/lightsail/regions_snapshot.json from the live Lightsail API
	@command -v aws >/dev/null 2>&1 || { echo "aws CLI not found; install it first" >&2; exit 1; }
	@command -v python3 >/dev/null 2>&1 || { echo "python3 not found" >&2; exit 1; }
	@aws lightsail get-regions --region us-east-1 --output json | python3 -c 'import json, sys, datetime; d=json.load(sys.stdin); out={"version":1,"fetched_at":datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),"source_region":"us-east-1","regions":sorted([{"id":r["name"],"display_name":r["displayName"],"continent":r["continentCode"],"description":r["description"]} for r in d["regions"]], key=lambda r: r["id"])}; print(json.dumps(out, indent=2))' > pkg/lightsail/regions_snapshot.json
	@echo "Wrote pkg/lightsail/regions_snapshot.json"
