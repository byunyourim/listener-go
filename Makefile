.PHONY: build run test lint tidy migrate-up migrate-down sqlc

build:
	go build -o bin/listener ./cmd/listener

run:
	go run ./cmd/listener

test:
	go test ./...

lint:
	golangci-lint run

tidy:
	go mod tidy

# golang-migrate 필요: brew install golang-migrate
migrate-up:
	migrate -path migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path migrations -database "$(DATABASE_URL)" down 1

# sqlc 필요: brew install sqlc
sqlc:
	sqlc generate
