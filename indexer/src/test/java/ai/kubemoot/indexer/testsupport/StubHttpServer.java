package ai.kubemoot.indexer.testsupport;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.List;
import java.util.function.Function;

/**
 * A real HTTP server on a free local port for tests of the code that fetches over HTTP.
 * Routes are registered by method and path; an unregistered route answers 404.
 */
public final class StubHttpServer implements AutoCloseable {

    /** What a route answers. */
    public record Reply(int status, Map<String, String> headers, String body) {
        public static Reply ok(String body) {
            return new Reply(200, Map.of(), body);
        }

        public static Reply status(int status) {
            return new Reply(status, Map.of(), "");
        }

        public static Reply withHeaders(Map<String, String> headers) {
            return new Reply(200, headers, "");
        }
    }

    private final HttpServer server;
    private final Map<String, Function<String, Reply>> routes = new LinkedHashMap<>();
    private final List<String> requests = new CopyOnWriteArrayList<>();

    public StubHttpServer() {
        try {
            server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        } catch (IOException e) {
            throw new IllegalStateException("cannot start the stub server", e);
        }
        server.createContext("/", this::handle);
        server.start();
    }

    /** Answer {@code method path} with a fixed reply. */
    public StubHttpServer route(String method, String path, Reply reply) {
        return route(method, path, requestBody -> reply);
    }

    /** Answer {@code method path} with a reply computed from the request body. */
    public StubHttpServer route(String method, String path, Function<String, Reply> reply) {
        routes.put(method + " " + path, reply);
        return this;
    }

    public String baseUrl() {
        return "http://127.0.0.1:" + server.getAddress().getPort();
    }

    /** The "METHOD path" of every request received so far. */
    public List<String> requests() {
        return List.copyOf(requests);
    }

    private void handle(HttpExchange exchange) throws IOException {
        String key = exchange.getRequestMethod() + " " + exchange.getRequestURI().getPath();
        requests.add(key);
        String requestBody = new String(exchange.getRequestBody().readAllBytes(), StandardCharsets.UTF_8);
        Reply reply = routes.getOrDefault(key, body -> Reply.status(404)).apply(requestBody);
        reply.headers().forEach((name, value) -> exchange.getResponseHeaders().add(name, value));
        byte[] bytes = reply.body().getBytes(StandardCharsets.UTF_8);
        boolean head = "HEAD".equals(exchange.getRequestMethod());
        exchange.sendResponseHeaders(reply.status(), head || bytes.length == 0 ? -1 : bytes.length);
        if (!head && bytes.length > 0) {
            exchange.getResponseBody().write(bytes);
        }
        exchange.close();
    }

    @Override
    public void close() {
        server.stop(0);
    }
}
