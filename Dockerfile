FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o /api cmd/server/main.go

# Install goose for migrations
RUN go install github.com/pressly/goose/v3/cmd/goose@latest

FROM alpine:latest

WORKDIR /app
COPY --from=builder /api /api
COPY --from=builder /go/bin/goose /usr/local/bin/goose
COPY migrations ./migrations
COPY .env.example .env

# Expose the API port
EXPOSE 8080

# The entrypoint script waits for DB, runs migrations, then starts the server
COPY <<-"EOF" /entrypoint.sh
#!/bin/sh
set -e
echo "Running database migrations..."
goose -dir ./migrations postgres "$DATABASE_URL" up
echo "Starting application server..."
exec /api
EOF

RUN chmod +x /entrypoint.sh

CMD ["/entrypoint.sh"]
