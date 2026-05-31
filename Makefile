.PHONY: build run test test-race test-integration lint tidy migrate-up migrate-down sqlc

build:
	go build -o bin/listener ./cmd/listener

run:
	go run ./cmd/listener

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

# Docker daemon 필요 — testcontainers로 임시 Postgres 컨테이너 기동.
# 단위 테스트도 함께 실행 (-tags=integration이 추가 케이스만 활성화)
test-integration:
	go test -race -count=1 -tags=integration ./...

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
