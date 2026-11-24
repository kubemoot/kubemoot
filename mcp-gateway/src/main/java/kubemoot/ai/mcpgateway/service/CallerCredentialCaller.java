package kubemoot.ai.mcpgateway.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.CallerCredentialConfig;
import kubemoot.ai.mcpgateway.model.CallerIdentity;
import kubemoot.ai.mcpgateway.model.CallerToken;
import kubemoot.ai.mcpgateway.model.McpMessage;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Mono;

import java.io.IOException;
import java.net.URI;
import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Makes a {@code tools/call} to a server in caller-credential mode {@code Required} with the
 * token of the person who asked. The token is leased for this call (see
 * {@link CredentialLeaseClient}), sent on one upstream call and dropped; the server's own
 * registered credential is never sent here, and a call with no lease makes no upstream request.
 *
 * <p>Each call opens its own upstream session (initialize, the call, then a best-effort close),
 * so no session is tied to one person's token. The server's own endpoint is tried first; a 403
 * moves to each fallback endpoint in order with the same token. When every endpoint refuses, the
 * first refusal's text is the tool result. Every failure is a tool result the agent can report;
 * none is thrown.</p>
 */
public class CallerCredentialCaller {

    private static final Logger log = LoggerFactory.getLogger(CallerCredentialCaller.class);
    private static final String SESSION_HEADER = "Mcp-Session-Id";
    private static final String REDACTED = "<redacted>";
    private static final int MAX_REFUSAL_CHARS = 300;
    private static final int HTTP_UNAUTHORIZED = 401;
    private static final int HTTP_FORBIDDEN = 403;
    private static final Map<String, Object> INIT_PARAMS = Map.of(
        "protocolVersion", "2024-11-05",
        "capabilities", Map.of(),
        "clientInfo", Map.of("name", "kubemoot-mcp-gateway", "version", "0.1.0"));

    /** One reply from an upstream endpoint. */
    private record Reply(int status, String session, String body) { }

    /** What one endpoint did with the call. */
    private sealed interface Outcome permits Replied, Refused, Rejected, Failed { }

    private record Replied(McpMessage message) implements Outcome { }

    private record Refused(String text) implements Outcome { }

    private record Rejected() implements Outcome { }

    private record Failed(String text) implements Outcome { }

    private final CredentialLeaseClient leases;
    private final ObjectMapper objectMapper;
    private final WebClient client;
    private final Duration callTimeout;

    public CallerCredentialCaller(McpGatewayProperties properties, WebClient.Builder builder,
                                  ObjectMapper objectMapper) {
        this.leases = new CredentialLeaseClient(properties, builder, objectMapper);
        this.objectMapper = objectMapper;
        // No default headers: nothing the server was registered with can ride along.
        this.client = builder.clone().build();
        this.callTimeout = properties.getCredentialBroker().getCallTimeout();
    }

    /**
     * @param primaryUrl the server's own endpoint, exactly as registered
     * @return the tool result; never an error signal
     */
    public Mono<McpMessage> call(String primaryUrl, CallerCredentialConfig config, McpMessage request,
                                 CallerIdentity who) {
        if (who == null) {
            log.warn("Tool call to a per-person server carried no conversation and principal");
            return Mono.just(toolError(request, noGrant(config)));
        }
        McpMessage outgoing = config.relayContext() ? withContext(request, who) : request;
        return leases.lease(config, who).flatMap(lease -> switch (lease) {
            case CredentialLeaseClient.Granted granted ->
                tryEndpoints(endpoints(primaryUrl, config), 0, granted.token(), outgoing, config, null);
            case CredentialLeaseClient.Denied denied -> Mono.just(toolError(request, denial(denied, config)));
        });
    }

    private static List<String> endpoints(String primaryUrl, CallerCredentialConfig config) {
        var all = new ArrayList<String>();
        all.add(primaryUrl);
        all.addAll(config.onForbidden());
        return all;
    }

    private Mono<McpMessage> tryEndpoints(List<String> urls, int index, CallerToken token, McpMessage request,
                                          CallerCredentialConfig config, String firstRefusal) {
        return callOne(urls.get(index), token, request)
            .onErrorResume(e -> {
                log.warn("Call with a person's token failed: {}", e.getClass().getSimpleName());
                return Mono.just(new Failed("The server could not be reached. Ask again."));
            })
            .flatMap(outcome -> {
                String refusal = outcome instanceof Refused refused && firstRefusal == null
                    ? refused.text() : firstRefusal;
                if (index + 1 < urls.size() && movesOn(outcome, index, refusal)) {
                    return tryEndpoints(urls, index + 1, token, request, config, refusal);
                }
                return Mono.just(finish(outcome, index, request, config, refusal));
            });
    }

    /** A refusal moves to the next endpoint, and so does a fallback that does not know the tool. */
    private static boolean movesOn(Outcome outcome, int index, String refusal) {
        return outcome instanceof Refused || unknownToolOnFallback(outcome, index, refusal);
    }

    private static boolean unknownToolOnFallback(Outcome outcome, int index, String refusal) {
        if (index == 0 || refusal == null) {
            return false;
        }
        return outcome instanceof Replied replied && isUnknownTool(replied.message().error());
    }

    /** JSON-RPC method not found or invalid params, which is how a server answers a tool it lacks. */
    private static boolean isUnknownTool(McpMessage.McpError error) {
        return error != null && (error.code() == -32601 || error.code() == -32602);
    }

    /** The answer once no endpoint is left to try. */
    private static McpMessage finish(Outcome outcome, int index, McpMessage request,
                                     CallerCredentialConfig config, String refusal) {
        return switch (outcome) {
            case Replied replied when !unknownToolOnFallback(outcome, index, refusal) -> replied.message();
            case Rejected ignored -> toolError(request, rejected(config));
            case Failed failed when refusal == null -> toolError(request, failed.text());
            default -> refusalResult(request, refusal);
        };
    }

    private Mono<Outcome> callOne(String url, CallerToken token, McpMessage call) {
        return Mono.defer(() -> callAt(URI.create(url), token, call));
    }

    private Mono<Outcome> callAt(URI endpoint, CallerToken token, McpMessage call) {
        return exchange(endpoint, token, null, McpMessage.request(1, "initialize", INIT_PARAMS))
            .flatMap(init -> {
                Outcome early = byStatus(init, token);
                if (early != null) {
                    return Mono.just(early);
                }
                String session = init.session();
                return exchange(endpoint, token, session, McpMessage.notification("notifications/initialized"))
                    .then(Mono.defer(() -> exchange(endpoint, token, session, call)))
                    .map(reply -> toOutcome(reply, token))
                    .flatMap(outcome -> closeSession(endpoint, token, session).thenReturn(outcome));
            })
            .timeout(callTimeout);
    }

    private Outcome byStatus(Reply reply, CallerToken token) {
        if (reply.status() == HTTP_UNAUTHORIZED) {
            return new Rejected();
        }
        if (reply.status() == HTTP_FORBIDDEN) {
            return new Refused(refusalText(reply.body(), token));
        }
        if (reply.status() < 200 || reply.status() >= 300) {
            return new Failed("The server answered with an error (HTTP " + reply.status() + "). Ask again.");
        }
        return null;
    }

    private Outcome toOutcome(Reply reply, CallerToken token) {
        Outcome early = byStatus(reply, token);
        if (early != null) {
            return early;
        }
        McpMessage message = parse(reply.body());
        return message == null ? new Failed("The server's reply could not be read. Ask again.")
            : new Replied(message);
    }

    private Mono<Void> closeSession(URI endpoint, CallerToken token, String session) {
        if (session == null) {
            return Mono.empty();
        }
        return client.delete().uri(endpoint)
            .header(HttpHeaders.AUTHORIZATION, "Bearer " + token.value())
            .header(SESSION_HEADER, session)
            .exchangeToMono(response -> response.releaseBody())
            .timeout(Duration.ofSeconds(5))
            .onErrorResume(e -> Mono.empty());
    }

    private Mono<Reply> exchange(URI endpoint, CallerToken token, String session, Object body) {
        var spec = client.post().uri(endpoint)
            .header(HttpHeaders.AUTHORIZATION, "Bearer " + token.value())
            .header(HttpHeaders.ACCEPT, "application/json, text/event-stream")
            .contentType(MediaType.APPLICATION_JSON);
        if (session != null) {
            spec = spec.header(SESSION_HEADER, session);
        }
        return spec.bodyValue(body)
            .exchangeToMono(response -> CredentialLeaseClient.bodyText(response)
                .map(text -> new Reply(response.statusCode().value(),
                    response.headers().asHttpHeaders().getFirst(SESSION_HEADER), text)));
    }

    /** The JSON-RPC message in a reply, plain or as a server-sent event; null when none can be read. */
    private McpMessage parse(String body) {
        String text = body == null ? "" : body.trim();
        try {
            if (text.startsWith("{")) {
                return objectMapper.readValue(text, McpMessage.class);
            }
            for (String line : text.split("\\R")) {
                if (line.startsWith("data:") && line.substring(5).trim().startsWith("{")) {
                    return objectMapper.readValue(line.substring(5).trim(), McpMessage.class);
                }
            }
        } catch (IOException e) {
            log.warn("A reply from a per-person server could not be parsed");
        }
        return null;
    }

    /** The upstream's own reason for a refusal, bounded, with the token scrubbed from it. */
    private String refusalText(String body, CallerToken token) {
        String reason = reasonIn(body == null ? "" : body.trim());
        reason = reason.replace(token.value(), REDACTED).replaceAll("\\p{Cntrl}+", " ").trim();
        if (reason.length() > MAX_REFUSAL_CHARS) {
            reason = reason.substring(0, MAX_REFUSAL_CHARS);
        }
        return reason.isEmpty() ? "The server refused this call (HTTP 403)." : reason;
    }

    private String reasonIn(String body) {
        if (!body.startsWith("{")) {
            return body;
        }
        try {
            JsonNode node = objectMapper.readTree(body);
            for (String field : List.of("message", "error_description", "detail", "error")) {
                JsonNode value = node.path(field);
                if (value.isTextual() && !value.asText().isBlank()) {
                    return value.asText();
                }
                if (value.path("message").isTextual()) {
                    return value.path("message").asText();
                }
            }
        } catch (IOException e) {
            return "";
        }
        return body;
    }

    /** The request with the person's two ids in its {@code _meta}; ids only, never a token. */
    static McpMessage withContext(McpMessage request, CallerIdentity who) {
        var params = new LinkedHashMap<String, Object>(request.params() == null ? Map.of() : request.params());
        var meta = new LinkedHashMap<String, Object>();
        if (params.get("_meta") instanceof Map<?, ?> existing) {
            existing.forEach((k, v) -> meta.put(String.valueOf(k), v));
        }
        meta.put(CallerIdentity.CONVERSATION_KEY, who.conversationId());
        meta.put(CallerIdentity.PRINCIPAL_KEY, who.principalId());
        params.put("_meta", meta);
        return request.withParams(params);
    }

    private static String noGrant(CallerCredentialConfig config) {
        return "This tool uses the asking person's own " + config.account()
            + " account and none is connected for this question. Connect your " + config.account()
            + " account and ask again.";
    }

    private static String denial(CredentialLeaseClient.Denied denied, CallerCredentialConfig config) {
        return switch (denied.reason()) {
            case NO_GRANT -> noGrant(config);
            case EXPIRED -> "Your " + config.account() + " sign-in expired. Sign in again and ask again.";
            case UNAVAILABLE -> "Sign-in is unavailable right now: the credential service could not be reached."
                + " Ask again.";
        };
    }

    private static String rejected(CallerCredentialConfig config) {
        return config.account() + " rejected your sign-in. Connect your " + config.account()
            + " account again and ask again.";
    }

    private static McpMessage refusalResult(McpMessage request, String upstreamText) {
        return toolError(request, upstreamText + " The asking person's account does not include this tool.");
    }

    private static McpMessage toolError(McpMessage request, String text) {
        return McpMessage.response(request.id(), Map.of(
            "isError", true,
            "content", List.of(Map.of("type", "text", "text", text))));
    }
}
