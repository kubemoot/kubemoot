package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.mcp.McpClientService;
import ai.kubemoot.agent.nats.ChatEventPublisher;
import ai.kubemoot.agent.nats.NatsConnectionProvider;
import ai.kubemoot.agent.rag.RagClient;
import jakarta.inject.Inject;
import jakarta.ws.rs.*;
import jakarta.ws.rs.core.MediaType;
import jakarta.ws.rs.core.Response;

import java.util.List;
import java.util.Map;

/**
 * REST API controller for the Kubemoot Agent Runtime.
 */
@Path("/")
@Produces(MediaType.APPLICATION_JSON)
@Consumes(MediaType.APPLICATION_JSON)
public class ChatController {

    @Inject
    ChatService chatService;

    @Inject
    RagClient ragClient;

    @Inject
    McpClientService mcpClient;

    @Inject
    ChatEventPublisher chatEventPublisher;

    @Inject
    AgentProperties properties;

    @Inject
    ModelWarmupService warmupService;

    @Inject
    NatsConnectionProvider natsConnectionProvider;

    @POST
    @Path("/chat")
    public ChatResponse chat(ChatRequest request) {
        var serviceRequest = new ChatService.ChatRequest(request.conversationId(), request.message(), request.crew());
        var result = chatService.chat(serviceRequest);

        chatEventPublisher.publishChatEvent(
                result.conversationId(), request.message(),
                result.response(), properties.model().model(),
                result.inputTokens(), result.outputTokens());

        return new ChatResponse(result.conversationId(), result.response(), properties.model().model(),
                result.threadId(), result.inputTokens(), result.outputTokens());
    }

    @GET
    @Path("/health")
    public HealthResponse health() {
        boolean ollamaReachable = warmupService.isOllamaReachable();
        boolean natsConnected = natsConnectionProvider.isAvailable();
        return new HealthResponse(true, ollamaReachable, natsConnected,
                ragClient.healthCheck(), mcpClient.healthCheck());
    }

    @GET
    @Path("/ready")
    public ReadyResponse ready() {
        return new ReadyResponse(warmupService.isModelReady());
    }

    @GET
    @Path("/tools")
    public ToolsResponse tools() {
        var tools = mcpClient.listTools();
        return new ToolsResponse(tools.size(),
                tools.stream().map(t -> new ToolInfo(t.name(), t.description())).toList());
    }

    @DELETE
    @Path("/conversations/{conversationId}")
    public Response clearConversation(@PathParam("conversationId") String conversationId) {
        chatService.clearConversation(conversationId);
        return Response.noContent().build();
    }

    public record ChatRequest(String conversationId, String message, String crew) {}
    public record ChatResponse(String conversationId, String response, String model, String threadId,
                                long inputTokens, long outputTokens) {}
    public record HealthResponse(boolean healthy, boolean ollama, boolean natsConnected,
                                     Map<String, Boolean> ragSources, Map<String, Boolean> mcpServers) {}
    public record ReadyResponse(boolean ready) {}
    public record ToolsResponse(int count, List<ToolInfo> tools) {}
    public record ToolInfo(String name, String description) {}
}
