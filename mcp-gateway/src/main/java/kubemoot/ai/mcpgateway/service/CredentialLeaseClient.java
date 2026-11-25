package kubemoot.ai.mcpgateway.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.model.CallerCredentialConfig;
import kubemoot.ai.mcpgateway.model.CallerIdentity;
import kubemoot.ai.mcpgateway.model.CallerToken;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Mono;
import reactor.core.scheduler.Schedulers;

import java.io.IOException;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.time.format.DateTimeParseException;
import java.util.Map;

/**
 * Leases one person's token from the credential broker for one upstream call.
 *
 * <p>The request is {@code POST /internal/v1/lease} with {@code {conversationId, principalId,
 * verifier}}, authenticated with the gateway's own projected ServiceAccount token as bearer
 * (read from its file on every lease, because the kubelet rotates it). The answer is
 * {@code {token, expiresAt}}, or 404 (no grant), 403 (the principal does not own the grant) or
 * 410 (expired). Nothing is cached: the caller uses the token once and drops it. The token and
 * the ServiceAccount token never reach a log line, an exception message or a {@code toString}.</p>
 */
public class CredentialLeaseClient {

    private static final Logger log = LoggerFactory.getLogger(CredentialLeaseClient.class);
    static final String LEASE_PATH = "/internal/v1/lease";

    /** The result of a lease: a token to use once, or the reason there is none. */
    public sealed interface Lease permits Granted, Denied { }

    public record Granted(CallerToken token) implements Lease { }

    public record Denied(Reason reason) implements Lease { }

    /** Why no token: none was granted, it ran out, or the broker could not answer. */
    public enum Reason { NO_GRANT, EXPIRED, UNAVAILABLE }

    private final McpGatewayProperties.CredentialBroker broker;
    private final ObjectMapper objectMapper;
    private final WebClient client;

    public CredentialLeaseClient(McpGatewayProperties properties, WebClient.Builder builder,
                                 ObjectMapper objectMapper) {
        this.broker = properties.getCredentialBroker();
        this.objectMapper = objectMapper;
        this.client = builder.clone().build();
    }

    public Mono<Lease> lease(CallerCredentialConfig config, CallerIdentity who) {
        return Mono.fromCallable(() -> new Request(endpoint(config), serviceAccountToken()))
            .subscribeOn(Schedulers.boundedElastic())
            .flatMap(request -> send(request, config, who))
            .onErrorResume(e -> {
                log.warn("Credential lease failed: {}", e.getClass().getSimpleName());
                return Mono.just(new Denied(Reason.UNAVAILABLE));
            });
    }

    /** The lease address and the gateway's own token; either may be missing. */
    private record Request(URI endpoint, String serviceAccountToken) { }

    private Mono<Lease> send(Request request, CallerCredentialConfig config, CallerIdentity who) {
        URI endpoint = request.endpoint();
        String serviceAccountToken = request.serviceAccountToken();
        if (endpoint == null || serviceAccountToken == null) {
            log.warn("Credential lease not possible: the broker address or the gateway's own token is missing");
            return Mono.just(new Denied(Reason.UNAVAILABLE));
        }
        if (config.verifier() == null) {
            // A connected account's token comes from a broker that does not exist yet; sending the
            // person's ids to the verifier broker would only be refused.
            log.warn("Credential lease not possible: the server takes a connected account, which no broker leases");
            return Mono.just(new Denied(Reason.UNAVAILABLE));
        }
        Map<String, String> body = Map.of(
            "conversationId", who.conversationId(),
            "principalId", who.principalId(),
            "verifier", config.verifier());
        return client.post().uri(endpoint)
            .header(HttpHeaders.AUTHORIZATION, "Bearer " + serviceAccountToken)
            .contentType(MediaType.APPLICATION_JSON)
            .accept(MediaType.APPLICATION_JSON)
            .bodyValue(body)
            .exchangeToMono(response -> bodyText(response)
                .map(text -> interpret(response.statusCode().value(), text)))
            .timeout(broker.getTimeout());
    }

    /**
     * The reply body as text, read as bytes: the codec that decodes a String logs the decoded text
     * at TRACE, and this body holds a person's token.
     */
    static Mono<String> bodyText(ClientResponse response) {
        return response.bodyToMono(byte[].class).defaultIfEmpty(new byte[0])
            .map(bytes -> new String(bytes, StandardCharsets.UTF_8));
    }

    private URI endpoint(CallerCredentialConfig config) {
        String base = broker.getUrl();
        if (base == null || base.isBlank()) {
            return null;
        }
        URI brokerUri = URI.create(Urls.trimTrailingSlashes(base));
        if (config.leaseUrl() == null) {
            return URI.create(brokerUri + LEASE_PATH);
        }
        // The gateway's own token goes only to the broker it is configured with, whatever a
        // registration says: a lease address must share that address's scheme, host and port.
        URI requested = URI.create(config.leaseUrl());
        boolean sameOrigin = brokerUri.getScheme().equalsIgnoreCase(requested.getScheme())
            && brokerUri.getHost().equalsIgnoreCase(requested.getHost())
            && brokerUri.getPort() == requested.getPort();
        if (!sameOrigin) {
            log.warn("Credential lease refused: the registered lease address is not the configured broker");
            return null;
        }
        return requested;
    }

    private String serviceAccountToken() {
        try {
            String text = Files.readString(Path.of(broker.getTokenFile())).trim();
            return text.isEmpty() ? null : text;
        } catch (IOException | RuntimeException e) {
            return null;
        }
    }

    private Lease interpret(int status, String body) {
        Lease outcome = switch (status) {
            case 200 -> parse(body);
            case 404, 403 -> new Denied(Reason.NO_GRANT);
            case 410 -> new Denied(Reason.EXPIRED);
            default -> new Denied(Reason.UNAVAILABLE);
        };
        if (outcome instanceof Denied denied) {
            log.info("Credential lease denied: {}", denied.reason());
        }
        return outcome;
    }

    private Lease parse(String body) {
        try {
            JsonNode node = objectMapper.readTree(body);
            String value = node.path("token").asText("");
            JsonNode expires = node.path("expiresAt");
            Instant expiresAt = expires.isTextual() ? Instant.parse(expires.asText()) : null;
            CallerToken token = new CallerToken(value, expiresAt);
            return token.expired(Instant.now()) ? new Denied(Reason.EXPIRED) : new Granted(token);
        } catch (IOException | DateTimeParseException | IllegalArgumentException e) {
            return new Denied(Reason.UNAVAILABLE);
        }
    }
}
