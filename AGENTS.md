# AGENTS.md

Guidance for AI agents working with `dapr-mcp-server`. Two audiences:

1. **Agents contributing to this repo** — Claude Code, Cursor, Cline, Copilot editing Go code in this project. Start at [§ Contributors](#contributors).
2. **Agents consuming this MCP server** — LLM-driven agents that call the tools this server exposes. Start at [§ Consumers](#consumers).

Both sections are short on purpose. Deep content lives in the [docs](https://docs.dapr.io/developing-ai/mcp/) and in the tool schemas the server emits at runtime.

---

## TL;DR

- This repo is a **Go Model Context Protocol (MCP) server** that fronts the [Dapr runtime](https://dapr.io) and exposes Dapr's building blocks (state, pub/sub, secrets, workflows, etc.) as MCP tools.
- Module: `github.com/dapr/dapr-mcp-server`. Go 1.25+. MCP SDK: `github.com/modelcontextprotocol/go-sdk`.
- Transports: **stdio** (default, for Claude Desktop / Cursor) and **streamable HTTP** (`--http <addr>`, for remote clients).
- Tools register **conditionally** based on which Dapr components the sidecar reports via `get_components`. Always call `get_components` before invoking any component-specific tool.

---

## Contributors

For AI agents editing code in this repo.

### Commands

All commands assume the repo root is the working directory.

| Purpose | Command |
| --- | --- |
| Build binary | `go build -o dapr-mcp-server ./cmd/dapr-mcp-server` |
| Build (CI-style, with version) | `CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=$(git rev-parse HEAD)" -o dapr-mcp-server ./cmd/dapr-mcp-server` |
| Run unit tests | `go test -race ./pkg/...` |
| Run with coverage (CI mirror) | `go test -race -coverprofile=coverage.out -covermode=atomic ./pkg/...` |
| Coverage summary | `go tool cover -func=coverage.out` |
| Lint | `golangci-lint run ./...` (config in `.golangci.yml`, gosec + staticcheck + govet among others) |
| Integration tests (Python) | `cd test && uv sync && uv run python app.py` (needs a running `dapr-mcp-server` + Dapr sidecar) |

Minimum coverage threshold expected by reviewers: no hard gate in CI but aim for parity with siblings (see `./pkg/state` as an exemplar).

### Where to add a new MCP tool

Tools live per capability under `pkg/<capability>/`. Pattern:

1. Create (or edit) `pkg/<capability>/tools.go`. Define the input struct with `json` + `jsonschema` tags, then register with `server.AddTool` inside a `RegisterTools(server *mcp.Server, client dapr.Client, metrics *telemetry.ToolMetrics)` function.
2. Write table-driven tests in `pkg/<capability>/tools_test.go` using the `mocks/` stubs.
3. Wire into `cmd/dapr-mcp-server/main.go`:
   - If the tool is **core** (always registered), add an unconditional call at the top of `registerTools` alongside `metadata.RegisterTools`, `invoke.RegisterTools`, `actor.RegisterTools`.
   - If the tool is **conditional on a Dapr component**, extend the component-presence `switch` and the conditional registration block in `registerTools`. Use the Dapr component type prefix (e.g. `state.`, `pubsub.`, `lock.`).
4. Update tool metadata and documentation:
   - Add the tool row to the README's status table.
   - Add a section to the dapr/docs tool reference.
   - Confirm the tool's description embeds its safety properties (`idempotent` / `destructive` / `read-only` / `side-effect`) — the description is the contract AI consumers see.

### Go conventions

- `gofmt` + `goimports` are mandatory. `goimports` local-prefix is `github.com/dapr/dapr-mcp-server` — internal imports go in their own group.
- Small interfaces (1–3 methods). Accept interfaces, return structs.
- Wrap errors with `%w`: `fmt.Errorf("refresh JWKS: %w", err)`.
- `slog` for structured logging, pre-configured with an OTEL-aware handler in `main.go`. Don't re-create loggers in packages — accept one as a parameter or use `slog.Default()`.
- Tests are table-driven with `-race`. Use `t.Parallel()` freely.
- `gosec` exclusions must be per-line with a reason: `//nolint:gosec // URL is from trusted server config, not user input`.

### DCO (required on every commit)

Every commit must carry a `Signed-off-by:` trailer. The PR will be blocked by the DCO check otherwise.

```sh
git commit -s -m "fix(state): guard against empty store name"
```

See `CONTRIBUTING.md` for the full DCO text and the `git commit --amend --no-edit --signoff` recovery if you forget.

### PR flow

- Template: `.github/pull_request_template.md`. Checklist covers build, tests, lint, DCO, docs.
- CI: `.github/workflows/ci.yml` (lint + test + build) and `.github/workflows/dco.yml` (sign-off check).
- Issue templates: `.github/ISSUE_TEMPLATE/` — all use GitHub Issue Forms with an "area" dropdown mirroring `pkg/` names.

---

## Consumers

For AI agents that invoke tools on a running `dapr-mcp-server` instance.

### Start here: call `get_components`

Before invoking anything component-specific (state, pub/sub, secrets, binding, lock, conversation, crypto), call `get_components`. It returns the live list of Dapr components the sidecar knows about — their names, types, versions, and capabilities. Treat it as the **authoritative source** for component names. Do not guess or memoize.

The only always-available tools that don't need a prior `get_components` call are: `get_components` itself, `invoke_service`, `invoke_actor_method`.

### Decision tree: which tool?

- **Persist data?** → `save_state`. Need atomicity across multiple keys? → `execute_transaction`.
- **Read data back?** → `get_state`.
- **Forget data?** → `delete_state`.
- **Publish an event?** → `publish_event`. Need message TTL / routing headers? → `publish_event_with_metadata`.
- **Call another microservice?** → `invoke_service`.
- **Call a stateful actor?** → `invoke_actor_method`.
- **Reach an external system** (webhook, queue, object store)? → `invoke_output_binding`.
- **Fetch a secret?** → `get_secret` (single) or `get_bulk_secrets` (high-risk — only when user explicitly asked to enumerate).
- **Defer reasoning to another LLM?** → `converse_with_llm`.
- **Encrypt / decrypt payload?** → `encrypt_data` / `decrypt_data`.
- **Guard a critical section?** → `acquire_lock`, then `release_lock` (always pair them).

### Safety defaults

Classification matters for retry and rollback logic.

| Property | Tools |
| --- | --- |
| `read-only`, `idempotent` | `get_components`, `get_state`, `get_secret`, `get_bulk_secrets`, `converse_with_llm`, `decrypt_data` |
| `idempotent`, `side-effect` | `save_state`, `acquire_lock` |
| `destructive`, `idempotent` | `delete_state` |
| `destructive`, `not-idempotent`, `side-effect` | `invoke_service`, `invoke_actor_method`, `invoke_output_binding`, `execute_transaction`, `encrypt_data` |
| `not-idempotent`, `side-effect` | `publish_event`, `publish_event_with_metadata`, `release_lock` |

**Rules of thumb**:

- Safe to auto-retry: anything `idempotent`.
- Needs human-in-the-loop confirmation for first call: anything marked `destructive`.
- Never invent component names, keys, topics, actor IDs, secret names, or crypto parameters. Derive them from `get_components` or ask the user.
- `metadata` fields are maps, not JSON-encoded strings. `{}`, never `"{}"`.
- For multi-step workflows, run tools **one at a time** and inspect the result before the next call.

### When NOT to reach for this server

Use `dapr-mcp-server` when you need durable state, distributed coordination, event-driven fan-out, or a production runtime boundary. Skip it for:

- Free-form computation that doesn't need persistence.
- One-off text generation (use the LLM directly rather than going through `converse_with_llm`).
- Local file I/O or process execution — that's an operating-system concern, not a Dapr one.

### Authoritative schema

This file summarizes safety defaults. The **authoritative tool schema** is what the server emits at connection time. For a static, human-readable reference see the [tool reference](https://docs.dapr.io/developing-ai/mcp/mcp-server-tool-reference/) at docs.dapr.io.

---

## Links

- [README](./README.md) — landing page, install, quick start
- [CONTRIBUTING.md](./CONTRIBUTING.md) — DCO, PR workflow, developer guide
- [SECURITY.md](./SECURITY.md) — vulnerability disclosure
- [docs.dapr.io / developing-ai / mcp](https://docs.dapr.io/developing-ai/mcp/) — full documentation
- [Model Context Protocol](https://modelcontextprotocol.io/) — protocol spec
