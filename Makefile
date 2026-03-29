.PHONY: proto build run test test-integration bench migrate migrate-down lint

PROTO_DIR := proto/kronos/v1
GEN_DIR   := gen/kronos/v1
BINARY    := bin/kronos

# golang-migrate CLI — no separate binary install needed
MIGRATE := go run -mod=mod github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.1

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
	$(MIGRATE) -path migrations -database "$(DATABASE_URL)" up

migrate-down:
	$(MIGRATE) -path migrations -database "$(DATABASE_URL)" down 1

test:
	go test -race -count=1 ./...

test-integration:
	go test -tags integration -race -count=1 -timeout 120s ./internal/integration/...

bench:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/bench $(BENCH_FLAGS)

bench-unit:
	go test -bench=BenchmarkPool_Throughput -benchtime=5s -count=1 ./internal/worker/

lint:
	golangci-lint run ./...
