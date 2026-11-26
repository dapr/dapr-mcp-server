# dapr-mcp-server

A Model Context Protocol (MCP) server that exposes [Dapr's](https://dapr.io) building blocks as tools for AI agents.

<!-- TODO(badges): add CI / release / Go version / license badges once first tag is cut -->
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/dapr/dapr-mcp-server/badge)](https://scorecard.dev/viewer/?uri=github.com/dapr/dapr-mcp-server)

---

## What it is

`dapr-mcp-server` is a Go MCP server that fronts a Dapr sidecar and hands AI agents a curated set of tools over every Dapr building block: state, pub/sub, secrets, service invocation, actors, bindings, distributed locks, cryptography, and conversation components. Tools register **dynamically** from the live component set — if your deployment has no pub/sub broker, the pub/sub tools aren't exposed.

Two transports are supported: **stdio** for local IDE integrations (Claude Desktop, Cursor, VS Code, Claude Code) and **streamable HTTP** for remote or shared deployments. Every tool call is annotated with OpenTelemetry spans, metrics, and optional log export; requests can be gated by OIDC, SPIFFE, Dapr Sentry, or a hybrid of the three.

Full documentation lives at **[docs.dapr.io / developing-ai / mcp](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/)**.

## Quick start

Run the binary next to a Dapr sidecar with `dapr run` (install it from [Install](#install)):

```bash
dapr init

# stdio, for local MCP clients that launch the server themselves
dapr run --app-id dapr-mcp-server --resources-path resources -- dapr-mcp-server

# streamable HTTP, for remote clients
dapr run --app-id dapr-mcp-server --resources-path resources -- dapr-mcp-server --http :8080
```

With `--http`, connect any MCP client to `http://localhost:8080/`. The container image is meant for Kubernetes, where the Dapr sidecar injector adds the sidecar through the `dapr.io/enabled: "true"` and `dapr.io/app-id` pod annotations. For the complete walk-through (components, auth, OTEL, verification), see the [getting started guide](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-getting-started/).

## Install

| Method | Command |
| --- | --- |
| Container | `docker pull ghcr.io/dapr/dapr-mcp-server:latest` |
| Binary | Download from [Releases](https://github.com/dapr/dapr-mcp-server/releases) and place on `PATH` |
| From source (Go 1.26.6+) | `go install github.com/dapr/dapr-mcp-server/cmd/dapr-mcp-server@latest` |

The server is listed in the official [MCP Registry](https://registry.modelcontextprotocol.io/v0.1/servers?search=io.github.dapr/dapr-mcp-server) as `io.github.dapr/dapr-mcp-server`, so MCP clients that browse the registry can install it as a container.

## Configuration

The [configuration guide](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-configuration/) has the full environment variable reference.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--http <addr>` | unset (stdio) | Serve streamable HTTP on this address instead of stdin/stdout. Health probes are served on `/livez`, `/readyz` and `/startupz`. |
| `--health-check` | `false` | Probe `/livez` of a running server and exit 0 if it answers 200, 1 otherwise. Used by the container `HEALTHCHECK`. |
| `--health-check-addr <host:port>` | from `--http`, else `localhost:8080` | Address `--health-check` probes. A wildcard host such as `0.0.0.0` is probed as `localhost`. |
| `--version` | `false` | Print the version and exit. |
| `--stateless` | `false` | HTTP only. Serve streamable HTTP without sessions: no `Mcp-Session-Id`, every request stands alone, and server-to-client requests (elicitation, sampling) are refused. Clients on protocol `2026-07-28` reach the server over HTTP only in this mode. Ignored, with a warning, over stdio. |
| `--event-store <state store>` | unset | HTTP only. Keep server-sent events in this Dapr state store so a client whose stream drops can resume it with `Last-Event-ID` while the server process that holds its session keeps running. Needs sessions, so it cannot be combined with `--stateless`. Also closes any session that sends no request for one hour; a client that only listens on its GET stream must initialize again after that. At startup the server writes and deletes a probe key, with the same `ttlInSeconds` metadata as an event, and exits if the state store refuses either. The state store then becomes a hard dependency of every session-mode request: the server records each POST's stream before handling it, so while the state store is unavailable every POST, `initialize` included, fails with HTTP 500. The stored events hold complete tool results, including secret values and decrypted plaintext, unencrypted; see below. Ignored, with a warning, over stdio. |
| `--spec-version <date>` | newest the SDK supports (`2026-07-28`) | Newest MCP protocol version the server negotiates. Older versions stay accepted, newer ones are refused. One of `2026-07-28`, `2025-11-25`, `2025-06-18`, `2025-03-26`, `2024-11-05` with the current Go MCP SDK; the list comes from the SDK, and `--help` prints the one your build supports. |

Logs are written as JSON to stderr, so they never mix with the stdio transport on stdout. `/readyz` returns 503 while the Dapr sidecar is unreachable.

### Protocol versions and transports

The server speaks two generations of the MCP protocol. Up to `2025-11-25` a client opens a session with `initialize`. From `2026-07-28` there are no sessions: a client calls `server/discover` and sends its protocol version and capabilities with every request.

| Transport | Protocols served |
| --- | --- |
| stdio | All supported versions, including `server/discover`. |
| `--http` | Up to `2025-11-25`, with sessions. A newer client falls back to `initialize`. |
| `--http --stateless` | All supported versions, without sessions. `server/discover` answers `2026-07-28` clients. |

Stream resumption (`--event-store`) is part of the session protocols only; `2026-07-28` removed it. Resumption does not survive a server restart: the Go MCP SDK keeps sessions in memory and rejects a session ID the process did not create, and it has no hook to restore one, so the events left in the state store can no longer be reached. Keeping them in a state store bounds the server's memory, and the TTL reclaims them. Each event expires after one hour, and the small records that hold each stream's event count expire after 24 hours, so storage is reclaimed even when a session is never closed, while a stream that stays silent for longer than an hour (a slow tool call, or the stream a client opens with GET) keeps its event numbering. Use a state store that supports the `ttlInSeconds` metadata, such as Redis. Because abandoned sessions would otherwise never release their events, turning on `--event-store` also closes sessions that send no request for one hour (the SDK's session timeout counts only the time between requests, so a stream held open with GET does not keep a session alive); without `--event-store` idle sessions are never closed.

A client should open its listening GET stream when the session starts and, when it drops, resume it with `Last-Event-ID`. The server stores the session's standalone notifications from the start whether or not a GET is attached, and a GET without `Last-Event-ID` asks for all of them. Once the first of them has expired (after one hour), such a GET is refused with HTTP 400, because the events can no longer be numbered as the client would expect. The SDK's in-memory event store does the same once it has dropped events. A cleaner fix needs the SDK's `After` to report where a replay starts.

The event store keeps every server-sent event as sent, which includes the full result of every tool call: values returned by `get_secret`, `get_bulk_secrets` and `get_state`, and plaintext returned by `decrypt_data`. Anyone who can read that state store can read them for as long as the records live. Point `--event-store` at a state store used only by this server, scope it to this app, and turn on [Dapr state encryption](https://docs.dapr.io/developing-applications/building-blocks/state-management/howto-encrypt-state/) for it.

Settings specific to the HTTP transport:

| Environment variable | Default | Purpose |
| --- | --- | --- |
| `DAPR_MCP_CORS_ORIGIN` | unset (no CORS headers) | Origin allowed to call the server from a browser, for example `https://app.example.com`. Set it only when a browser-based MCP client on another origin needs access. |

## Documentation

| Page | Purpose |
| --- | --- |
| [Overview](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/) | What the server is, architecture, capabilities, when to use it |
| [Getting started](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-getting-started/) | Install, configure components, run, verify |
| [Tool reference](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/) | Schemas, inputs, outputs, and safety flags for every tool |
| [Configuration](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-configuration/) | Environment variables, flags, and transport settings |
| [Authentication](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-authentication/) | OIDC, SPIFFE, Dapr Sentry, hybrid mode |
| [Observability](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-observability/) | Traces, metrics, and logs |

AI coding agents contributing to this repo: see [AGENTS.md](./AGENTS.md). For human contributors: see [CONTRIBUTING.md](./CONTRIBUTING.md).

## Tools at a glance

All tools, with links to their entries in the [tool reference](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/). Core tools are always registered; conditional tools register only when a matching Dapr component exists. The server follows components that Dapr hot-reloads: each time a client lists tools or calls `get_components`, it checks the sidecar, adds a building block's tools when its first component appears and removes them when its last one goes, and tells connected clients the tool list changed.

The workflow tools need Dapr 1.18.2 or later. On older runtimes, `get_workflow_history` against a sidecar without an actor state store makes daprd exit ([dapr/dapr#10217](https://github.com/dapr/dapr/issues/10217), fixed in 1.18.2).

| Category | Tool | Registration | Notes |
| --- | --- | --- | --- |
| metadata | [`get_components`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#get_components) | Core | Always call first |
| invoke | [`invoke_service`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#invoke_service) | Core | Service-to-service HTTP invocation |
| actors | [`invoke_actor_method`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#invoke_actor_method) | Core | Virtual-actor method call |
| workflow | [`list_workflows`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#list_workflows) | Core | Read-only; this app's instances, without payloads |
| workflow | [`get_workflow_status`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#get_workflow_status) | Core | Read-only |
| workflow | [`get_workflow_history`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#get_workflow_history) | Core | Read-only; event history, with the event IDs to rerun from |
| workflow | [`start_workflow`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#start_workflow) | Core | Not idempotent without `instanceID` |
| workflow | [`raise_workflow_event`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#raise_workflow_event) | Core | Not idempotent |
| workflow | [`pause_workflow`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#pause_workflow) | Core | Idempotent |
| workflow | [`resume_workflow`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#resume_workflow) | Core | Idempotent |
| workflow | [`rerun_workflow`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#rerun_workflow) | Core | New instance from a scheduling event in the history |
| workflow | [`terminate_workflow`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#terminate_workflow) | Core | Destructive; also terminates child workflows |
| workflow | [`purge_workflow`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#purge_workflow) | Core | Destructive; deletes state and history, not recursive |
| state | [`save_state`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#save_state) | `state.*` | Idempotent save |
| state | [`get_state`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#get_state) | `state.*` | Read-only |
| state | [`get_bulk_state`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#get_bulk_state) | `state.*` | Read-only; several keys |
| state | [`delete_state`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#delete_state) | `state.*` | Destructive, idempotent |
| state | [`execute_transaction`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#execute_transaction) | `state.*` | Atomic batch |
| pubsub | [`publish_event`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#publish_event) | `pubsub.*` | Not idempotent |
| pubsub | [`publish_event_with_metadata`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#publish_event_with_metadata) | `pubsub.*` | Adds headers/TTL |
| bindings | [`invoke_output_binding`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#invoke_output_binding) | `bindings.*` | External-system I/O |
| secrets | [`get_secret`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#get_secret) | `secretstores.*` | Single secret |
| secrets | [`get_bulk_secrets`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#get_bulk_secrets) | `secretstores.*` | High-risk; bulk fetch |
| conversation | [`converse_with_llm`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#converse_with_llm) | `conversation.*` | Delegate to a downstream LLM |
| crypto | [`encrypt_data`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#encrypt_data) | `crypto.*` | RSA encrypt |
| crypto | [`decrypt_data`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#decrypt_data) | `crypto.*` | RSA decrypt |
| lock | [`acquire_lock`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#acquire_lock) | `lock.*` | Distributed mutex |
| lock | [`release_lock`](https://docs.dapr.io/developing-ai/mcp/dapr-mcp-server/dapr-mcp-server-tool-reference/#release_lock) | `lock.*` | Pair with `acquire_lock` |

## Development

```bash
go build -o dapr-mcp-server ./cmd/dapr-mcp-server
go test -race ./...
golangci-lint run ./...
```

See [AGENTS.md](./AGENTS.md) for the full contributor / AI-agent playbook (build commands, tool-addition pattern, testing strategy, DCO flow).

## Contributing

Contributions welcome — open issues via the [issue templates](./.github/ISSUE_TEMPLATE), sign your commits per the [DCO](./CONTRIBUTING.md#developer-certificate-of-origin-signing-your-work), and follow the [pull request template](./.github/pull_request_template.md). See [CONTRIBUTING.md](./CONTRIBUTING.md) for the full workflow.

Security issues: **do not** open a public issue — follow the [Dapr security disclosure process](https://docs.dapr.io/operations/support/support-security-issues/) instead.

## License

Apache 2.0 — see [LICENSE](./LICENSE).
