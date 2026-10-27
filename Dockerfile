# Build stage
FROM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS builder

# Install ca-certificates for HTTPS and git for modules
RUN apk add --no-cache ca-certificates git

WORKDIR /build

# Copy go mod files first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.Version=${VERSION}" \
    -o dapr-mcp-server \
    ./cmd/dapr-mcp-server

# Runtime stage - using distroless for security hardening
FROM gcr.io/distroless/static-debian12:nonroot@sha256:26f9b99f2463f55f20db19feb4d96eb88b056e0f1be7016bb9296a464a89d772

# Proves ownership of the image to the MCP Registry; must match the name in server.json
LABEL io.modelcontextprotocol.server.name="io.github.dapr/dapr-mcp-server"

# Copy the binary from builder
COPY --from=builder /build/dapr-mcp-server /dapr-mcp-server

# Use non-root user (provided by distroless nonroot image)
USER nonroot:nonroot

# Expose default HTTP port
EXPOSE 8080

# Health check using built-in health endpoints
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/dapr-mcp-server", "--health-check"]

ENTRYPOINT ["/dapr-mcp-server"]

# Default to HTTP mode
CMD ["--http", "0.0.0.0:8080"]
