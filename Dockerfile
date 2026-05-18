# Build stage
FROM golang:latest AS builder

WORKDIR /app

ENV GOPROXY=https://goproxy.cn

RUN sed -i 's/deb.debian.org/mirrors.aliyun.com/g' /etc/apt/sources.list.d/debian.sources && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        gcc \
        libc6-dev && \
    rm -rf /var/lib/apt/lists/*

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=1 GOOS=linux go build \
    -ldflags '-linkmode external -extldflags "-static"' \
    -o chronos ./cmd/chronos

# Final stage
FROM alpine:latest

WORKDIR /app

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories && \
    apk add --no-cache ca-certificates tzdata

# Copy binary from builder
COPY --from=builder /app/chronos .

# Create directories
RUN mkdir -p /app/config /app/data /app/logs

# Copy config file
COPY config/config.yaml /app/config/config.yaml

# Expose port
EXPOSE 8080

# Run the application
CMD ["./chronos", "-config", "/app/config/config.yaml"]
