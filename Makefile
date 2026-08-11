.PHONY: run test tidy migrate-up migrate-down migrate-status build

# Pull DATABASE_URL from .env for the migrate targets. Override with
#   make migrate-up DB_URL=postgres://…
# for one-off runs against staging / prod.
DB_URL ?= $(shell grep DATABASE_URL .env | cut -d= -f2-)

run:
	go run ./cmd/api

test:
	go test ./... -race

tidy:
	go mod tidy

migrate-up:
	migrate -database "$(DB_URL)" -path ./migrations up

migrate-down:
	migrate -database "$(DB_URL)" -path ./migrations down 1

migrate-status:
	migrate -database "$(DB_URL)" -path ./migrations version

build:
	go build -o bin/api ./cmd/api
