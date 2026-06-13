# Portfolio site — common tasks. Run `make help` for the list.
# Go tasks run inside a golang container so a local Go install is optional.

GO_IMAGE := golang:1.25-alpine
GO_RUN   := docker run --rm -v "$(CURDIR)/backend":/src -w /src $(GO_IMAGE)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# --- setup ---

.PHONY: setup
setup: ## First-time setup: .env + frontend deps + codegen
	@test -f .env || cp .env.example .env
	cd frontend && npm install
	$(MAKE) codegen

# --- local dev ---

.PHONY: up
up: ## Start the backend stack (Postgres, Redis, MinIO, API)
	docker compose up --build

.PHONY: up-async
up-async: ## Start the stack plus ElasticMQ + worker
	docker compose --profile async up --build

.PHONY: up-full
up-full: ## Start everything including the Next.js frontend container
	docker compose --profile async --profile fullstack up --build

.PHONY: down
down: ## Stop the stack
	docker compose --profile async --profile fullstack down

.PHONY: clean
clean: ## Stop and remove volumes (DESTROYS local data)
	docker compose --profile async --profile fullstack down -v

.PHONY: logs
logs: ## Tail API logs
	docker compose logs -f api

.PHONY: seed
seed: ## Seed starter projects into the database
	docker compose run --rm api seed

.PHONY: fe-dev
fe-dev: ## Run the Next.js dev server locally (fast HMR)
	cd frontend && npm run dev

# --- code generation ---

.PHONY: generate
generate: generate-ent generate-spec codegen ## Regenerate everything (Ent, OpenAPI, frontend)

.PHONY: generate-ent
generate-ent: ## Regenerate the Ent client from schemas
	$(GO_RUN) sh -c "go generate ./ent"

.PHONY: generate-spec
generate-spec: ## Regenerate backend/openapi.yaml from the Go handlers
	$(GO_RUN) sh -c "go run ./cmd/spec" > backend/openapi.yaml
	@echo "wrote backend/openapi.yaml"

.PHONY: codegen
codegen: ## Regenerate frontend React Query hooks from the spec
	cd frontend && npm run codegen

# --- quality ---

.PHONY: fmt
fmt: ## Format Go code
	$(GO_RUN) sh -c "gofmt -w ."

.PHONY: test
test: ## Run Go tests
	$(GO_RUN) sh -c "go test ./..."

.PHONY: tidy
tidy: ## go mod tidy
	$(GO_RUN) sh -c "go mod tidy"
