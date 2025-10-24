FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod ./
COPY go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /label-backup main.go

# Use alpine for the final image
FROM alpine:latest

# Install necessary CA certificates for HTTPS calls (e.g., to S3, webhooks)
RUN apk add --no-cache ca-certificates

# Install backup CLI tools
# mysql-client on Alpine is MariaDB's client tools
RUN apk add --no-cache \
    postgresql-client \
    mysql-client \
    mongodb-tools \
    redis \
    curl \
    bash

# Create non-root user for security
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Create backup directory with proper permissions
RUN mkdir -p /backups && \
    chown -R appuser:appgroup /backups

WORKDIR /
COPY --from=builder /label-backup /label-backup

# Switch to non-root user
USER appuser

ENTRYPOINT ["/label-backup"] 