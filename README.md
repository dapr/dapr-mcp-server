# dapr-mcp

A Model Context Protocol (MCP) server that exposes Dapr's microservices building blocks as tools for AI agents.

## Overview

dapr-mcp bridges AI agents with Dapr's powerful microservices APIs, enabling:
- **State Management**: Save, retrieve, and delete state with transactional support
- **Pub/Sub Messaging**: Publish events to message brokers
- **Service Invocation**: Call other Dapr-enabled services
- **Secrets Management**: Securely access secrets from configured stores
- **Distributed Locking**: Coordinate access to shared resources
- **Bindings**: Interact with external systems (databases, queues, etc.)
- **Conversation AI**: Delegate tasks to external LLMs via Dapr
- **Cryptography**: Encrypt and decrypt sensitive data

## Features

- Full OpenTelemetry observability (traces, metrics, logs)
- OAuth2.0/OIDC and SPIFFE authentication support
- Kubernetes-ready health endpoints
- Dynamic component discovery
- Comprehensive safety rules for AI agents

## Installation

### From Source

```bash
go install github.com/dapr/dapr-mcp-server/cmd/dapr-mcp-server@latest
```

### From Release

Download pre-built binaries from the [Releases](https://github.com/dapr/dapr-mcp-server/releases) page.

### Docker

```bash
docker pull ghcr.io/dapr/dapr-mcp-server:latest
docker run -p 8080:8080 ghcr.io/dapr/dapr-mcp-server:latest
```

## Quick Start

1. Initialize Dapr:
```bash
dapr init
```

2. Run the MCP server:
```bash
dapr run --app-id dapr-mcp-server --resources-path components -- dapr-mcp-server --http localhost:8080
```

3. Connect your AI agent to `http://localhost:8080`

## Tool Status

| Category | Tool | Status | Notes |
|----------|------|--------|-------|
| actors | invoke_actor_method | Beta | Virtual actor method invocation |
| bindings | invoke_output_binding | Stable | External system interactions |
| conversation | converse_with_llm | Stable | Delegate to external LLMs |
| crypto | encrypt_data | Experimental | May be blocked by some models |
| crypto | decrypt_data | Experimental | May be blocked by some models |
| invoke | invoke_service | Beta | Service-to-service calls |
| lock | acquire_lock | Stable | Distributed locking |
| lock | release_lock | Stable | Distributed locking |
| metadata | get_components | Stable | Component discovery |
| pubsub | publish_event | Stable | Event publishing |
| pubsub | publish_event_with_metadata | Stable | Event publishing with headers |
| secrets | get_secret | Stable | Single secret retrieval |
| secrets | get_bulk_secrets | Stable | Bulk secret retrieval |
| state | save_state | Stable | State persistence |
| state | get_state | Stable | State retrieval |
| state | delete_state | Stable | State deletion |
| state | execute_transaction | Stable | Atomic state operations |

## Configuration

### Environment Variables

#### Core Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `DAPR_MCP_SERVER_LOG_LEVEL` | Log level: DEBUG, INFO, WARN, ERROR | `INFO` |

#### OpenTelemetry Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP endpoint for all signals | (none - disabled) |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Override for traces endpoint | uses `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | Override for metrics endpoint | uses `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | Override for logs endpoint | uses `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | Protocol: grpc, http/protobuf | `grpc` |
| `OTEL_EXPORTER_OTLP_HEADERS` | Headers (key=value,key2=value2) | (none) |
| `OTEL_SERVICE_NAME` | Service name for telemetry | `dapr-mcp-server` |
| `OTEL_SERVICE_VERSION` | Service version | `v1.0.0` |
| `DAPR_MCP_SERVER_METRICS_ENABLED` | Enable metrics export | `true` |
| `DAPR_MCP_SERVER_LOGS_OTEL_ENABLED` | Export logs via OTEL | `true` |

#### Authentication Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `AUTH_ENABLED` | Enable authentication | `false` |
| `AUTH_MODE` | Mode: disabled, oidc, spiffe, dapr-sentry, hybrid | `disabled` |
| `AUTH_SKIP_PATHS` | Paths to skip auth (comma-separated) | `/livez,/readyz,/startupz` |

#### OIDC Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `OIDC_ENABLED` | Enable OIDC authentication | `false` |
| `OIDC_ISSUER_URL` | OIDC provider URL | (required if OIDC enabled) |
| `OIDC_CLIENT_ID` | Expected audience (aud claim) | (required if OIDC enabled) |
| `OIDC_ALLOWED_ALGORITHMS` | Allowed signing algorithms | `RS256,ES256` |
| `OIDC_SKIP_ISSUER_CHECK` | Skip issuer validation (dev only) | `false` |

#### SPIFFE Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `SPIFFE_ENABLED` | Enable SPIFFE authentication | `false` |
| `SPIFFE_TRUST_DOMAIN` | SPIFFE trust domain | (required if SPIFFE enabled) |
| `SPIFFE_SERVER_ID` | This server's SPIFFE ID | (required if SPIFFE enabled) |
| `SPIFFE_ENDPOINT_SOCKET` | Workload API socket path | `SPIFFE_ENDPOINT_SOCKET` env |
| `SPIFFE_ALLOWED_CLIENTS` | Allowed client SPIFFE IDs | (none - all allowed) |

#### Dapr Sentry Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `DAPR_SENTRY_ENABLED` | Enable Dapr Sentry authentication | `false` |
| `DAPR_SENTRY_JWKS_URL` | Dapr Sentry JWKS endpoint | (required if enabled) |
| `DAPR_SENTRY_TRUST_DOMAIN` | Expected SPIFFE trust domain | (required if enabled) |
| `DAPR_SENTRY_AUDIENCE` | Expected audience claim | (none - not validated) |
| `DAPR_SENTRY_TOKEN_HEADER` | Header containing the JWT | `Authorization` |
| `DAPR_SENTRY_JWKS_REFRESH_INTERVAL` | JWKS cache refresh interval | `5m` |

## Health Endpoints

Kubernetes-compatible health endpoints:

| Endpoint | Purpose |
|----------|---------|
| `GET /livez` | Liveness probe - server is running |
| `GET /readyz` | Readiness probe - server can accept traffic |
| `GET /startupz` | Startup probe - initialization complete |

## OpenTelemetry

### Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `dapr-mcp-server.tool.invocations` | Counter | Total tool invocations |
| `dapr-mcp-server.tool.errors` | Counter | Failed tool invocations |
| `dapr-mcp-server.tool.duration` | Histogram | Execution time (ms) |
| `dapr-mcp-server.tool.in_progress` | UpDownCounter | Currently executing tools |

### Span Attributes

Each tool span includes:
- `tool.name`: The tool being invoked
- `tool.package`: The package containing the tool
- `dapr.operation`: Dapr operation type
- `dapr.component.type`: Component type being used
- `outcome`: success or error

### Example: Jaeger Setup

```bash
# Start Jaeger
docker run -d --name jaeger \
  -p 16686:16686 \
  -p 4317:4317 \
  jaegertracing/all-in-one:latest

# Configure dapr-mcp
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc

# Run server
dapr run --app-id dapr-mcp-server --resources-path components -- dapr-mcp-server --http localhost:8080
```

## Authentication Setup

### OIDC (OAuth2.0)

```bash
export AUTH_ENABLED=true
export AUTH_MODE=oidc
export OIDC_ENABLED=true
export OIDC_ISSUER_URL=https://accounts.google.com
export OIDC_CLIENT_ID=your-client-id
```

### SPIFFE

```bash
export AUTH_ENABLED=true
export AUTH_MODE=spiffe
export SPIFFE_ENABLED=true
export SPIFFE_TRUST_DOMAIN=example.org
export SPIFFE_SERVER_ID=spiffe://example.org/dapr-mcp
export SPIFFE_ENDPOINT_SOCKET=unix:///tmp/spire-agent/public/api.sock
```

### Dapr Sentry

For Dapr environments using Sentry without full SPIRE infrastructure:

```bash
export AUTH_ENABLED=true
export AUTH_MODE=dapr-sentry
export DAPR_SENTRY_ENABLED=true
export DAPR_SENTRY_JWKS_URL=http://dapr-sentry:8080/jwks.json
export DAPR_SENTRY_TRUST_DOMAIN=public
export DAPR_SENTRY_AUDIENCE=public  # Optional
```

**Note:** Dapr Sentry JWTs have SPIFFE IDs in the `sub` claim but no `iss` claim,
which is why they require this dedicated authenticator instead of OIDC.

### Hybrid Mode

Accept multiple authentication methods (OIDC, SPIFFE, and/or Dapr Sentry):

```bash
export AUTH_ENABLED=true
export AUTH_MODE=hybrid
export OIDC_ENABLED=true
export OIDC_ISSUER_URL=https://accounts.google.com
export OIDC_CLIENT_ID=your-client-id
export SPIFFE_ENABLED=true
export SPIFFE_TRUST_DOMAIN=example.org
export SPIFFE_SERVER_ID=spiffe://example.org/dapr-mcp
# Optionally add Dapr Sentry as well:
# export DAPR_SENTRY_ENABLED=true
# export DAPR_SENTRY_JWKS_URL=http://dapr-sentry:8080/jwks.json
# export DAPR_SENTRY_TRUST_DOMAIN=public
```

## IDE Integration

### VS Code

Add `.vscode/mcp.json`:

```json
{
  "servers": {
    "dapr-mcp": {
      "type": "http",
      "url": "http://localhost:8080"
    }
  }
}
```

### Claude Desktop

Add to your Claude Desktop configuration:

```json
{
  "mcpServers": {
    "dapr-mcp": {
      "url": "http://localhost:8080"
    }
  }
}
```

## Development

### Prerequisites

- Go 1.25+
- Dapr CLI
- Docker (optional, for local testing)

### Building

```bash
go build -o dapr-mcp-server ./cmd/dapr-mcp-server
```

### Testing

```bash
go test -v -race ./...
```

### Linting

```bash
golangci-lint run
```

## Cryptography Setup

The cryptography tools require an RSA key pair. Generate one with:

```bash
mkdir -p keys
openssl genpkey -algorithm RSA -out keys/rsa-private-key.pem -pkeyopt rsa_keygen_bits:4096
```

Then update `components/cryptography.yaml` to point to your key directory.

## Architecture

```
cmd/dapr-mcp-server/          # Main entry point
pkg/
  actors/             # Virtual actor tools
  auth/               # Authentication (OIDC, SPIFFE)
  bindings/           # Output binding tools
  conversation/       # LLM conversation tools
  crypto/             # Encryption/decryption tools
  health/             # Health check endpoints
  invoke/             # Service invocation tools
  lock/               # Distributed lock tools
  metadata/           # Component discovery
  mocks/              # Test mocks
  pubsub/             # Pub/Sub tools
  secrets/            # Secret management tools
  state/              # State management tools
  telemetry/          # OTEL instrumentation
```

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Make your changes with tests
4. Run `golangci-lint run` and `go test ./...`
5. Submit a pull request

## License

Apache 2.0 - See [LICENSE](LICENSE) for details.
