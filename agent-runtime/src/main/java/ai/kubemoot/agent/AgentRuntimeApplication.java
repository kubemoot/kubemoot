package ai.kubemoot.agent;

import io.quarkus.runtime.Quarkus;
import io.quarkus.runtime.annotations.QuarkusMain;

/**
 * Kubemoot Agent Runtime - LangChain4j based agent execution environment.
 *
 * <p>Provides HTTP API for AI agent interactions with support for:
 * <ul>
 *   <li>LLM chat via Ollama or OpenAI-compatible endpoints</li>
 *   <li>RAG context retrieval from vector databases</li>
 *   <li>MCP tool discovery and execution</li>
 *   <li>MCP Gateway health monitoring for readiness probes</li>
 * </ul>
 */
@QuarkusMain
public class AgentRuntimeApplication {

    public static void main(String[] args) {
        Quarkus.run(args);
    }
}
