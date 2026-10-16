.PHONY: up down test tidy logs

up: .env
	docker compose up --build

down:
	docker compose down

test:
	docker compose run --rm test

# go mod tidy without a local Go install
tidy:
	docker run --rm -v "$(CURDIR)":/src -w /src golang:1.26 go mod tidy

logs:
	docker compose logs -f app

.env:
	cp .env.example .env
