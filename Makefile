.PHONY: up down test eval lint tidy logs

up: .env
	docker compose up --build

down:
	docker compose down

test:
	docker compose run --rm test

# Agent eval against the real LLM from .env (costs API calls). Report: docs/eval-results.md
eval: .env
	docker compose run --rm test go test -tags=eval -run TestEval -count=1 -parallel 4 -timeout 30m ./internal/agent

lint:
	docker run --rm -v "$(CURDIR)":/app -w /app golangci/golangci-lint:v2.14.0 golangci-lint run ./...

# go mod tidy without a local Go install
tidy:
	docker run --rm -v "$(CURDIR)":/src -w /src golang:1.26 go mod tidy

logs:
	docker compose logs -f app

.env:
	cp .env.example .env
