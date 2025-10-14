.PHONY: proto build run test migrate migrate-down lint

PROTO_DIR := proto/kronos/v1
GEN_DIR   := gen/kronos/v1
BINARY    := bin/kronos

proto:
	mkdir -p $(GEN_DIR)
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		$(PROTO_DIR)/kronos.proto

build:
	go build -o $(BINARY) ./cmd/kronos

run: build
	./$(BINARY)

migrate:
	go run ./cmd/kronos migrate-up

migrate-down:
	go run ./cmd/kronos migrate-down

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...
