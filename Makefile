.PHONY: help build run fmt vet tidy \
	test test-unit test-integration test-up test-down \
	compose-up compose-down docker-build clean

BINARY := qi-index
TEST_DATABASE_URL ?= postgres://qi_index:qi_index@localhost:5433/qi_index_test?sslmode=disable

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: ## Build the qi-index binary into ./bin
	go build -o bin/$(BINARY) ./cmd/qi-index

run: ## Run `qi-index follow` locally (go run)
	go run ./cmd/qi-index follow

fmt: ## Format all Go source
	gofmt -l -w .

vet: ## go vet ./...
	go vet ./...

tidy: ## go mod tidy
	go mod tidy

test: test-unit ## Alias for test-unit

test-unit: ## Run the test suite without Postgres (Postgres-backed tests self-skip)
	go test ./...

test-up: ## Start a throwaway Postgres for integration tests (docker compose)
	docker compose --profile test up -d --wait postgres-test

test-down: ## Stop and remove the throwaway test Postgres
	docker compose --profile test down -v

test-integration: test-up ## Run the full suite against a real Postgres, then tear it down
	@TEST_DATABASE_URL=$(TEST_DATABASE_URL) go test ./... ; \
	status=$$?; \
	$(MAKE) test-down; \
	exit $$status

compose-up: ## Start the full app stack (postgres + qi-index) via docker compose
	docker compose up --build

compose-down: ## Stop the app stack and remove its volumes
	docker compose down -v

docker-build: ## Build the qi-index docker image
	docker build -t qi-index:latest .

clean: ## Remove build artifacts
	rm -rf bin
