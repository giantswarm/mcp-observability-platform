##@ Development

.PHONY: lint-yaml
lint-yaml: ## Run YAML linter (hand-written workflows only — skips zz_generated.*)
	@echo "Running YAML linter..."
	@yamllint .github/workflows/ci.yaml

##@ Testing

.PHONY: helm-lint
helm-lint: ## Lint Helm chart
	@echo "Linting Helm chart..."
	@helm lint ./helm/mcp-observability-platform

.PHONY: helm-test
helm-test: ## Run Helm chart unit tests (requires helm-unittest plugin)
	@echo "Running Helm unit tests..."
	@helm unittest ./helm/mcp-observability-platform

.PHONY: test-vet
test-vet: ## Run go test with race detector and go vet
	@echo "Running Go tests..."
	@NO_COLOR=true go test -race ./...
	@echo "Running go vet..."
	@go vet ./...

.PHONY: check
check: lint-yaml test-vet ## Run YAML lint + Go tests + vet

.PHONY: govulncheck
govulncheck: ## Scan for known vulnerabilities
	@echo "Checking for known vulnerabilities..."
	@command -v govulncheck >/dev/null 2>&1 || { echo "Installing govulncheck..."; go install golang.org/x/vuln/cmd/govulncheck@latest; }
	@govulncheck ./...

##@ Development (convenience)

.PHONY: tidy
tidy: ## Run `go mod tidy`
	@go mod tidy

.PHONY: helm-template
helm-template: ## Render chart templates locally for inspection
	@helm template mcp-observability-platform helm/mcp-observability-platform --kube-version 1.30.0

# The Dockerfile COPYs a prebuilt mcp-observability-platform-<os>-<arch> binary
# (architect/go-build does this in CI). The generated `build-docker` target
# doesn't produce that name, so build the binary and image here instead.
DOCKER_GOARCH ?= $(shell go env GOARCH)

.PHONY: docker-build
docker-build: ## Build the container image locally (mirrors CI's prebuilt-binary Dockerfile)
	CGO_ENABLED=0 GOOS=linux GOARCH=$(DOCKER_GOARCH) go build -trimpath \
		-o mcp-observability-platform-linux-$(DOCKER_GOARCH) .
	docker build --build-arg TARGETOS=linux --build-arg TARGETARCH=$(DOCKER_GOARCH) \
		-t mcp-observability-platform:dev .
