import asyncio
import logging

from dapr_agents import DurableAgent
from dapr_agents.agents.configs import (
    AgentExecutionConfig,
    AgentMemoryConfig,
    AgentPubSubConfig,
    AgentRegistryConfig,
    AgentStateConfig,
    AgentObservabilityConfig,
    AgentTracingExporter,
)
from dapr_agents.memory import ConversationDaprStateMemory
from dapr_agents.storage.daprstores.stateservice import StateStoreService
from dapr_agents.tool.mcp import MCPClient
from dapr_agents.workflow.runners import AgentRunner
from dapr_agents.llm import DaprChatClient

async def _load_mcp_tools() -> list:
    client = MCPClient()
    await client.connect_streamable_http("local", url="http://localhost:8080")
    return client.get_all_tools()


def main() -> None:
    logging.basicConfig(level=logging.DEBUG)
    logging.getLogger("opentelemetry.exporter.otlp.proto.grpc.exporter").setLevel(logging.CRITICAL)

    try:
        tools = asyncio.run(_load_mcp_tools())
    except Exception:
        logging.exception("Failed to load MCP tools via SSE")
        return

    asyncio.set_event_loop(asyncio.new_event_loop())

    agent = DurableAgent(
        name="Steve",
        role="Expert Dapr Microservices Client",
        goal=(
            "Translate user intents into precise, deterministic, and safe MCP tool calls. "
            "You MUST strictly adhere to the resource rules and security hints (Annotations) provided by the MCP server for every tool. "
            "Do not invent component names, keys, topics, or arguments; they must be provided by the user or discovered via the 'get_components' tool. "
        ),
        instructions=[
            "Answer clearly and helpfully.",
            "Always use available MCP tools to satisfy the user's requests when appropriate.",
            "Never attempt to perform operations outside of the available tools.",
            "Validate all arguments against the tool's JSON schema before calling.",
            "**Multi-Step Workflow Rule**: Break down complex tasks (e.g., 'encrypt and save') into multiple, sequential tool calls. Do not merge steps.",
            "**Security Principle**: Pay careful attention to 'ReadOnlyHint' and 'DestructiveHint' in the tool schema to gauge the risk of the operation.",
            "Upon errors, parse the tool error and correct your request. Iterate like this until success. Do not ask for input."
        ],
        llm=DaprChatClient(component_name='llm-provider'),
        tools=tools,
        pubsub = AgentPubSubConfig(
            pubsub_name="messagepubsub",
            agent_topic="agent.requests",
            broadcast_topic="agents.broadcast",
        ),
        state = AgentStateConfig(
            store=StateStoreService(store_name="agentstatestore"),
        ),
        registry = AgentRegistryConfig(
            store=StateStoreService(store_name="agentstatestore"),
            team_name="agent-team",
        ),
        execution = AgentExecutionConfig(max_iterations=4),
        memory = AgentMemoryConfig(
            store=ConversationDaprStateMemory(
                store_name="agentstatestore",
                session_id="agent-session",
            )
        ),
        agent_observability=AgentObservabilityConfig(
            enabled=True,
            tracing_enabled=True,
            tracing_exporter=AgentTracingExporter.OTLP_GRPC,
            endpoint="http://localhost:4317",
            auth_token="your-secret-token-here"
        ),
    )

    agent.start()
    runner = AgentRunner()
    try:
        runner.serve(agent, port=8001)
    finally:
        runner.shutdown(agent)

if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
