# Tool versions, pinned so that every run gives the same result.
TFPLUGINDOCS_VERSION := v0.25.0
GOVULNCHECK_VERSION := v1.9.0

default: build

.PHONY: build
build: ## Compile every package.
	go build ./...

.PHONY: install
install: ## Install the provider binary into GOBIN.
	go install .

.PHONY: fmt
fmt: ## Format Go and Terraform files.
	gofmt -s -w .
	terraform fmt -recursive examples

.PHONY: lint
lint: ## Run the linters.
	golangci-lint run

.PHONY: vulncheck
vulncheck: ## Report known vulnerabilities in the code and its dependencies.
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: test
test: ## Run the unit tests. Acceptance tests are skipped.
	go test -race -count=1 ./...

.PHONY: testacc
testacc: ## Run every test, including the acceptance tests (against the mock unless VIYA_ENDPOINT is set).
	TF_ACC=1 go test -race -count=1 -timeout 30m ./...

.PHONY: docs
docs: ## Regenerate the registry documentation in docs/.
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@$(TFPLUGINDOCS_VERSION) generate --provider-name viya

.PHONY: mock
mock: ## Serve the fake Viya deployment on http://127.0.0.1:8080.
	go run ./cmd/viya-mock

.PHONY: help
help: ## List the targets.
	@grep -E '^[a-z]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'
