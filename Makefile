.PHONY: up down logs migrate migrate-down build test lint load

up:
	docker compose up --build -d postgres redis
	docker compose up --build migrate
	docker compose up --build -d api

down:
	docker compose down -v

logs:
	docker compose logs -f api

migrate:
	docker compose run --rm migrate

migrate-down:
	docker compose run --rm --entrypoint /usr/local/bin/migrate migrate down

build:
	cd backend && go build ./...

test:
	cd backend && go test ./...

lint:
	cd backend && golangci-lint run ./...

load:
	k6 run load/vote.js
