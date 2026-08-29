.PHONY: run build test test-race test-integration lint fmt proto proto-check swagger swagger-check migrate-up migrate-down dev-up dev-down dev-logs

VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -s -w -X github.com/lihongjie0209/go-api-template/internal/buildinfo.Version=$(VERSION) -X github.com/lihongjie0209/go-api-template/internal/buildinfo.Commit=$(COMMIT) -X github.com/lihongjie0209/go-api-template/internal/buildinfo.BuildTime=$(BUILD_TIME)

run:
	go run ./cmd/api -config config/config.yaml

build:
	go build -ldflags="$(LDFLAGS)" -o bin/api ./cmd/api
	go build -o bin/migrate ./cmd/migrate

test:
	go test ./...

test-race:
	go test -race ./...

test-integration:
	go test -tags=integration -count=1 -timeout=15m ./integration/...

dev-up:
	docker compose up --build -d --wait

dev-down:
	docker compose down --remove-orphans

dev-logs:
	docker compose logs -f api

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .

swagger:
	go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/api/main.go -o docs --parseInternal

swagger-check: swagger
	git diff --exit-code -- docs

proto:
	protoc --go_out=. --go_opt=module=github.com/lihongjie0209/go-api-template --go-grpc_out=. --go-grpc_opt=module=github.com/lihongjie0209/go-api-template proto/hello/v1/hello.proto

proto-check: proto
	git diff --exit-code -- gen

migrate-up:
	go run ./cmd/migrate -direction up

migrate-down:
	go run ./cmd/migrate -direction down
