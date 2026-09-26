package kubemoot.ai.mcpgateway.controller;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.MediaType;
import org.springframework.http.codec.ServerSentEvent;
import org.springframework.web.bind.annotation.*;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.core.publisher.Sinks;
import kubemoot.ai.mcpgateway.model.McpMessage;
import kubemoot.ai.mcpgateway.service.ToolRouter;

import java.time.Duration;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;

@RestController
@RequestMapping("/mcp")
public class McpServerController {

    private static final Logger log = LoggerFactory.getLogger(McpServerController.class);

    private final ToolRouter toolRouter;
    private final ObjectMapper objectMapper;
    private final Map<String, Sinks.Many<ServerSentEvent<String>>> clientSinks = new ConcurrentHashMap<>();

    public McpServerController(ToolRouter toolRouter, ObjectMapper objectMapper) {
        this.toolRouter = toolRouter;
        this.objectMapper = objectMapper;
    }

    @GetMapping(produces = MediaType.TEXT_EVENT_STREAM_VALUE)
    public Flux<ServerSentEvent<String>> connectSse() {
        String clientId = UUID.randomUUID().toString();
        log.info("New SSE client connected: {}", clientId);

        Sinks.Many<ServerSentEvent<String>> sink = Sinks.many().multicast().onBackpressureBuffer();
        clientSinks.put(clientId, sink);

        // Send initial connection event with endpoint info
        try {
            Map<String, Object> endpointInfo = Map.of(
                "endpoint", "/mcp/messages?sessionId=" + clientId
            );
            sink.tryEmitNext(ServerSentEvent.<String>builder()
                .event("endpoint")
                .data(objectMapper.writeValueAsString(endpointInfo))
                .build());
        } catch (JsonProcessingException e) {
            log.error("Failed to serialize endpoint info", e);
        }

        return sink.asFlux()
            .mergeWith(keepAlive())
            .doOnCancel(() -> {
                log.info("SSE client disconnected: {}", clientId);
                clientSinks.remove(clientId);
            })
            .doOnTerminate(() -> {
                log.info("SSE stream terminated for client: {}", clientId);
                clientSinks.remove(clientId);
            });
    }

    @PostMapping(value = "/messages", consumes = MediaType.APPLICATION_JSON_VALUE, produces = MediaType.APPLICATION_JSON_VALUE)
    public Mono<McpMessage> handleMessage(
            @RequestParam(value = "sessionId", required = false) String sessionId,
            @RequestBody McpMessage request) {

        log.debug("Received MCP message: method={}, id={}", request.method(), request.id());

        return toolRouter.handleRequest(request)
            .doOnNext(response -> {
                // If there's an active SSE session, also send the response via SSE
                if (sessionId != null && clientSinks.containsKey(sessionId)) {
                    try {
                        String jsonResponse = objectMapper.writeValueAsString(response);
                        clientSinks.get(sessionId).tryEmitNext(
                            ServerSentEvent.<String>builder()
                                .event("message")
                                .data(jsonResponse)
                                .build()
                        );
                    } catch (JsonProcessingException e) {
                        log.error("Failed to serialize response for SSE", e);
                    }
                }
            });
    }

    private Flux<ServerSentEvent<String>> keepAlive() {
        return Flux.interval(Duration.ofSeconds(30))
            .map(i -> ServerSentEvent.<String>builder()
                .comment("keepalive")
                .build());
    }
}
