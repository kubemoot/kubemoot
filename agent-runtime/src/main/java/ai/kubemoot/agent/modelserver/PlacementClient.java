package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.atomic.AtomicLong;
import java.util.function.LongSupplier;

/**
 * The HTTP side of a caller of the placement service. Every request that does not get an answer from the
 * service ends as a {@link PlacementUnavailableException}: no connection, a timeout, a 5xx, or a 503
 * from a replica that is not the leader.
 *
 * <p>After a failure the client answers {@link PlacementUnavailableException} at once for
 * {@link #BREAKER}, without touching the network, so an outage costs a call one failed connect and not
 * one per call. Then it lets the next request through to find out whether the service is back.</p>
 */
public final class PlacementClient {

    /** How long after a failure the client does not try again. */
    static final Duration BREAKER = Duration.ofSeconds(2);

    /** A service answer: the status and the JSON body, null when there was none. */
    public record Answer(int status, JsonNode body) {}

    private final HttpClient http;
    private final URI base;
    private final ObjectMapper mapper;
    private final Duration requestTimeout;
    private final LongSupplier nanoClock;
    private final AtomicLong retryAfterNanos = new AtomicLong();
    private volatile boolean everFailed;

    public PlacementClient(String baseUrl, ObjectMapper mapper, Duration connectTimeout, Duration requestTimeout) {
        this(baseUrl, mapper, requestTimeout, System::nanoTime,
                HttpClient.newBuilder().version(HttpClient.Version.HTTP_1_1).connectTimeout(connectTimeout).build());
    }

    PlacementClient(String baseUrl, ObjectMapper mapper, Duration requestTimeout, LongSupplier nanoClock,
                    HttpClient http) {
        this.base = URI.create(baseUrl.endsWith("/") ? baseUrl : baseUrl + "/");
        this.mapper = mapper;
        this.requestTimeout = requestTimeout;
        this.nanoClock = nanoClock;
        this.http = http;
    }

    /** POST {@code body} as JSON to {@code path}. */
    public Answer post(String path, JsonNode body) {
        return post(path, body, requestTimeout);
    }

    /** POST with a request timeout of {@code timeout}, for a call the service holds open. */
    public Answer post(String path, JsonNode body, Duration timeout) {
        return send(request(path, timeout).header("Content-Type", "application/json")
                .POST(HttpRequest.BodyPublishers.ofString(body.toString(), StandardCharsets.UTF_8)).build());
    }

    /** GET {@code path}. */
    public Answer get(String path) {
        return send(request(path, requestTimeout).GET().build());
    }

    /** DELETE {@code path}. */
    public Answer delete(String path) {
        return send(request(path, requestTimeout).DELETE().build());
    }

    /**
     * POST that can be abandoned: the returned future completes with the answer, and cancelling it
     * closes the request, so a caller that no longer wants to wait does not hold a connection.
     */
    public CompletableFuture<Answer> postAsync(String path, JsonNode body, Duration timeout) {
        if (open()) {
            return CompletableFuture.failedFuture(unavailable("recently unreachable", null));
        }
        HttpRequest req = request(path, timeout).header("Content-Type", "application/json")
                .POST(HttpRequest.BodyPublishers.ofString(body.toString(), StandardCharsets.UTF_8)).build();
        CompletableFuture<HttpResponse<String>> raw =
                http.sendAsync(req, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
        CompletableFuture<Answer> result = raw.handle((resp, error) -> {
            if (error != null) {
                throw failed(error);
            }
            return answer(resp);
        });
        // Cancelling the answer closes the request.
        result.whenComplete((v, e) -> {
            if (result.isCancelled()) {
                raw.cancel(true);
            }
        });
        return result;
    }

    /** True while the client is not trying, after a failure. */
    boolean open() {
        return everFailed && nanoClock.getAsLong() < retryAfterNanos.get();
    }

    private HttpRequest.Builder request(String path, Duration timeout) {
        return HttpRequest.newBuilder(base.resolve(path.startsWith("/") ? path.substring(1) : path)).timeout(timeout);
    }

    private Answer send(HttpRequest req) {
        if (open()) {
            throw unavailable("recently unreachable", null);
        }
        try {
            return answer(http.send(req, HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8)));
        } catch (IOException e) {
            throw failed(e);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw failed(e);
        }
    }

    private Answer answer(HttpResponse<String> resp) {
        int status = resp.statusCode();
        if (status >= 500) {
            throw failed(new IOException("answered " + status + ": " + abbreviate(resp.body())));
        }
        everFailed = false;
        return new Answer(status, parse(resp.body()));
    }

    private JsonNode parse(String text) {
        if (text == null || text.isBlank()) {
            return null;
        }
        try {
            return mapper.readTree(text);
        } catch (IOException e) {
            throw failed(e);
        }
    }

    private PlacementUnavailableException failed(Throwable cause) {
        Throwable root = cause instanceof java.util.concurrent.CompletionException && cause.getCause() != null
                ? cause.getCause() : cause;
        retryAfterNanos.set(nanoClock.getAsLong() + BREAKER.toNanos());
        everFailed = true;
        return unavailable(String.valueOf(root.getMessage() == null ? root.getClass().getSimpleName()
                : root.getMessage()), root);
    }

    private static PlacementUnavailableException unavailable(String why, Throwable cause) {
        return new PlacementUnavailableException("placement service unreachable: " + why, cause);
    }

    private static String abbreviate(String s) {
        if (s == null) {
            return "";
        }
        return s.length() > 120 ? s.substring(0, 120) + "..." : s;
    }
}
