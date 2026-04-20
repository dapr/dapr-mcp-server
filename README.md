# dapr-mcp-server

A Model Context Protocol (MCP) server that exposes [Dapr's](https://dapr.io) building blocks as tools for AI agents.

<!-- TODO(badges): add CI / release / Go version / license badges once first tag is cut -->

---

## What it is

`dapr-mcp-server` is a Go MCP server that fronts a Dapr sidecar and hands AI agents a curated set of tools over every Dapr building block: state, pub/sub, secrets, service invocation, actors, bindings, distributed locks, cryptography, and conversation components. Tools register **dynamically** from the live component set — if your deployment has no pub/sub broker, the pub/sub tools aren't exposed.

Two transports are supported: **stdio** for local IDE integrations (Claude Desktop, Cursor, VS Code, Claude Code) and **streamable HTTP** for remote or shared deployments. Every tool call is annotated with OpenTelemetry spans, metrics, and optional log export; requests can be gated by OIDC, SPIFFE, Dapr Sentry, or a hybrid of the three.

Full documentation lives at **[docs.dapr.io / developing-ai / mcp](https://docs.dapr.io/developing-ai/mcp/)**.

## Quick start

```bash
dapr init
docker pull ghcr.io/dapr/dapr-mcp-server:latest
dapr run --app-id dapr-mcp-server \
         --resources-path resources \
         -- docker run --rm ghcr.io/dapr/dapr-mcp-server:latest --http :8080
```

Then connect any MCP client to `http://localhost:8080/`. For the complete walk-through (components, auth, OTEL, verification), see the [getting started guide](https://docs.dapr.io/developing-ai/mcp/mcp-server-getting-started/).

## Install

| Method | Command |
| --- | --- |
| Container | `docker pull ghcr.io/dapr/dapr-mcp-server:latest` |
| Binary | Download from [Releases](https://github.com/dapr/dapr-mcp-server/releases) and place on `PATH` |
| From source (Go 1.25+) | `go install github.com/dapr/dapr-mcp-server/cmd/dapr-mcp-server@latest` |

## Documentation

| Page | Purpose |
| --- | --- |
| [Overview](https://docs.dapr.io/developing-ai/mcp/mcp-server-overview/) | What the server is, architecture, capabilities, when to use it |
| [Getting started](https://docs.dapr.io/developing-ai/mcp/mcp-server-getting-started/) | Install, configure components, run, verify, full env var reference |
| [Tool reference](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/) | Schemas, inputs, outputs, and safety flags for every tool |
| [Integrations](https://docs.dapr.io/developing-ai/mcp/mcp-server-integrations/) | Claude Desktop, Claude Code, VS Code, Cursor, Dapr Agents, custom MCP clients |
| [Authentication](https://docs.dapr.io/developing-ai/mcp/mcp-authentication/) | OIDC, SPIFFE, Dapr Sentry, hybrid mode |

AI coding agents contributing to this repo: see [AGENTS.md](./AGENTS.md). For human contributors: see [CONTRIBUTING.md](./CONTRIBUTING.md).

## Tools at a glance

All tools, with links to their entries in the [tool reference](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/). Core tools are always registered; conditional tools register only when a matching Dapr component exists.

| Category | Tool | Registration | Notes |
| --- | --- | --- | --- |
| metadata | [`get_components`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#get_components) | Core | Always call first |
| invoke | [`invoke_service`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#invoke_service) | Core | Service-to-service HTTP invocation |
| actors | [`invoke_actor_method`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#invoke_actor_method) | Core | Virtual-actor method call |
| state | [`save_state`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#save_state) | `state.*` | Idempotent save |
| state | [`get_state`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#get_state) | `state.*` | Read-only |
| state | [`delete_state`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#delete_state) | `state.*` | Destructive, idempotent |
| state | [`execute_transaction`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#execute_transaction) | `state.*` | Atomic batch |
| pubsub | [`publish_event`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#publish_event) | `pubsub.*` | Not idempotent |
| pubsub | [`publish_event_with_metadata`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#publish_event_with_metadata) | `pubsub.*` | Adds headers/TTL |
| bindings | [`invoke_output_binding`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#invoke_output_binding) | `bindings.*` | External-system I/O |
| secrets | [`get_secret`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#get_secret) | `secretstores.*` | Single secret |
| secrets | [`get_bulk_secrets`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#get_bulk_secrets) | `secretstores.*` | High-risk; bulk fetch |
| conversation | [`converse_with_llm`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#converse_with_llm) | `conversation.*` | Delegate to a downstream LLM |
| crypto | [`encrypt_data`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#encrypt_data) | `crypto.*` | RSA encrypt |
| crypto | [`decrypt_data`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#decrypt_data) | `crypto.*` | RSA decrypt |
| lock | [`acquire_lock`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#acquire_lock) | `lock.*` | Distributed mutex |
| lock | [`release_lock`](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/#release_lock) | `lock.*` | Pair with `acquire_lock` |

## Development

```bash
go build -o dapr-mcp-server ./cmd/dapr-mcp-server
go test -race ./pkg/...
golangci-lint run ./...
```

See [AGENTS.md](./AGENTS.md) for the full contributor / AI-agent playbook (build commands, tool-addition pattern, testing strategy, DCO flow).

## Contributing

Contributions welcome — open issues via the [issue templates](./.github/ISSUE_TEMPLATE), sign your commits per the [DCO](./CONTRIBUTING.md#developer-certificate-of-origin-signing-your-work), and follow the [pull request template](./.github/pull_request_template.md). See [CONTRIBUTING.md](./CONTRIBUTING.md) for the full workflow.

Security issues: **do not** open a public issue — follow the [Dapr security disclosure process](https://docs.dapr.io/operations/support/support-security-issues/) instead.

## License

Apache 2.0 — see [LICENSE](./LICENSE).
