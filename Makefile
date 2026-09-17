.PHONY: up down logs build run test test-race vet fmt migrate-up migrate-down

up:
docker compose up --build

down:
docker compose down

logs:
docker compose logs -f

build:
go build ./...

run:
go run ./cmd/api

test:
go test ./...

test-race:
go test -race ./...

vet:
go vet ./...

fmt:
gofmt -w .

migrate-up:
migrate -path migrations -database "$$DATABASE_URL" up

migrate-down:
migrate -path migrations -database "$$DATABASE_URL" down 1
