package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.function.Function;

/** A stand-in for the placement service: answers each route with a programmed reply and records every request. */
final class FakePlacementServer implements AutoCloseable {

    private static final ObjectMapper M = new ObjectMapper();

    /** A request the server received. */
    record Seen(String method, String pathAndQuery, JsonNode body) {}

    /** What a route answers; {@code holdMs} keeps the request open that long first. */
    record Reply(int status, String json, long holdMs) {
        static Reply json(int status, String json) {
            return new Reply(status, json, 0);
        }

        static Reply ok(String json) {
            return json(200, json);
        }

        static Reply noContent() {
            return new Reply(204, "", 0);
        }
    }

    private final HttpServer server;
    final List<Seen> requests = new CopyOnWriteArrayList<>();
    private final Map<String, Function<Seen, Reply>> routes = new ConcurrentHashMap<>();
    private volatile CountDownLatch arrivals = new CountDownLatch(0);

    FakePlacementServer() throws IOException {
        server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.setExecutor(Executors.newCachedThreadPool());
        server.createContext("/", this::handle);
        server.start();
    }

    String url() {
        return "http://127.0.0.1:" + server.getAddress().getPort();
    }

    int port() {
        return server.getAddress().getPort();
    }

    /** Answers {@code method path} (the path without its query) with {@code reply}. */
    void on(String method, String path, Reply reply) {
        routes.put(method + " " + path, seen -> reply);
    }

    void on(String method, String path, Function<Seen, Reply> reply) {
        routes.put(method + " " + path, reply);
    }

    /** The next {@code n} requests are counted down on the returned latch. */
    CountDownLatch expect(int n) {
        arrivals = new CountDownLatch(n);
        return arrivals;
    }

    List<Seen> seen(String method, String pathPrefix) {
        return requests.stream().filter(s -> s.method().equals(method) && s.pathAndQuery().startsWith(pathPrefix))
                .toList();
    }

    private void handle(HttpExchange ex) throws IOException {
        String raw = new String(ex.getRequestBody().readAllBytes(), StandardCharsets.UTF_8);
        JsonNode body = raw.isBlank() ? null : M.readTree(raw);
        String uri = ex.getRequestURI().toString();
        Seen seen = new Seen(ex.getRequestMethod(), uri, body);
        requests.add(seen);
        Function<Seen, Reply> route = routes.get(ex.getRequestMethod() + " " + ex.getRequestURI().getPath());
        Reply reply = route == null ? Reply.json(404, "{\"code\":\"unknown\"}") : route.apply(seen);
        try {
            if (reply.holdMs() > 0) {
                Thread.sleep(reply.holdMs());
            }
            byte[] out = reply.json().getBytes(StandardCharsets.UTF_8);
            ex.getResponseHeaders().add("Content-Type", "application/json");
            ex.sendResponseHeaders(reply.status(), out.length == 0 ? -1 : out.length);
            if (out.length > 0) {
                ex.getResponseBody().write(out);
            }
        } catch (InterruptedException | IOException e) {
            // the caller went away
        } finally {
            ex.close();
            arrivals.countDown();
        }
    }

    @Override
    public void close() {
        server.stop(0);
    }

    static boolean await(CountDownLatch latch) throws InterruptedException {
        return latch.await(5, TimeUnit.SECONDS);
    }
}
