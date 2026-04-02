# Build stage
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /kronos ./cmd/kronos

# Runtime stage
FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /kronos /usr/local/bin/kronos
COPY migrations/ /migrations/
EXPOSE 50051 2112
ENTRYPOINT ["kronos"]
