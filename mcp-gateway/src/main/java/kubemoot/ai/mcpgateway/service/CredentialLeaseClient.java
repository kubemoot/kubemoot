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
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Leases one person's token from the credential broker for one upstream call.
 *
 * <p>The request is {@code POST /internal/v1/lease} with {@code {conversationId, principalId,
 * verifier}}, authenticated with the gateway's own projected ServiceAccount token as bearer
 * (read from its file on every lease, because the kubelet rotates it). The answer is
 * {@code {token, expiresAt}}, or 404 (no grant), 403 (the principal does not own the grant) or
 * 410 (expired). For a server that takes a connected account the body is {@code {conversationId,
 * account}} only: the crew's gateway names the person from the conversation, never from this
 * caller. The answer then also carries {@code tokenHosts}, the only hosts the token may be sent
 * to, and a refusal is 404 (no grant), 409 (the person must connect again), 410 (ended) or 503
 * (could not answer, retry). Nothing is cached: the caller uses the token once and drops it. The token and
 * the ServiceAccount token never reach a log line, an exception message or a {@code toString}.</p>
 */
public class CredentialLeaseClient {

    private static final Logger log = LoggerFactory.getLogger(CredentialLeaseClient.class);
    static final String LEASE_PATH = "/internal/v1/lease";

    /** The result of a lease: a token to use once, or the reason there is none. */
    public sealed interface Lease permits Granted, Denied { }

    /**
     * A token to use once. {@code tokenHosts} is null for a token a verifier vouched for, and for
     * a connected account's token the hosts it may be sent to, never empty.
     */
    public record Granted(CallerToken token, List<String> tokenHosts) implements Lease {
        public Granted(CallerToken token) {
            this(token, null);
        }

        public Granted {
            tokenHosts = tokenHosts == null ? null : List.copyOf(tokenHosts);
        }
    }

    public record Denied(Reason reason) implements Lease { }

    /**
     * Why no token: none was granted, the person must connect the account again, it ran out, or
     * the broker could not answer.
     */
    public enum Reason { NO_GRANT, NEEDS_RECONNECT, EXPIRED, UNAVAILABLE }

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
        boolean account = config.verifier() == null;
        return client.post().uri(endpoint)
            .header(HttpHeaders.AUTHORIZATION, "Bearer " + serviceAccountToken)
            .contentType(MediaType.APPLICATION_JSON)
            .accept(MediaType.APPLICATION_JSON)
            .bodyValue(body(config, who))
            .exchangeToMono(response -> bodyText(response)
                .map(text -> interpret(response.statusCode().value(), text, account)))
            .timeout(broker.getTimeout());
    }

    /**
     * The lease request. A connected account names itself and the conversation only; the person
     * is the one the crew's gateway finds from the conversation, so no principal is sent.
     */
    private static Map<String, String> body(CallerCredentialConfig config, CallerIdentity who) {
        Map<String, String> body = new LinkedHashMap<>();
        body.put("conversationId", who.conversationId());
        if (config.verifier() == null) {
            body.put("account", config.connectedAccount());
        } else {
            body.put("principalId", who.principalId());
            body.put("verifier", config.verifier());
        }
        return body;
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

    private Lease interpret(int status, String body, boolean account) {
        Lease outcome = switch (status) {
            case 200 -> parse(body, account);
            case 404, 403 -> new Denied(Reason.NO_GRANT);
            case 409 -> new Denied(Reason.NEEDS_RECONNECT);
            case 410 -> new Denied(Reason.EXPIRED);
            default -> new Denied(Reason.UNAVAILABLE);
        };
        if (outcome instanceof Denied denied) {
            log.info("Credential lease denied: {}", denied.reason());
        }
        return outcome;
    }

    private Lease parse(String body, boolean account) {
        try {
            JsonNode node = objectMapper.readTree(body);
            List<String> hosts = account ? tokenHosts(node.path("tokenHosts")) : null;
            if (account && hosts.isEmpty()) {
                // No host to send it to means no use for the token: fail closed.
                return new Denied(Reason.UNAVAILABLE);
            }
            String value = node.path("token").asText("");
            JsonNode expires = node.path("expiresAt");
            Instant expiresAt = expires.isTextual() ? Instant.parse(expires.asText()) : null;
            CallerToken token = new CallerToken(value, expiresAt);
            return token.expired(Instant.now()) ? new Denied(Reason.EXPIRED) : new Granted(token, hosts);
        } catch (IOException | DateTimeParseException | IllegalArgumentException e) {
            return new Denied(Reason.UNAVAILABLE);
        }
    }

    private static List<String> tokenHosts(JsonNode node) {
        List<String> hosts = new ArrayList<>();
        if (node.isArray()) {
            node.forEach(host -> {
                if (host.isTextual() && !host.asText().isBlank()) {
                    hosts.add(host.asText().trim());
                }
            });
        }
        return hosts;
    }
}
