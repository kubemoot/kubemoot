package ai.kubemoot.indexer.source;

import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.WebClientResponseException;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.Arrays;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

class DownloadsTest {

    // Two megabytes, the size of a public-domain encyclopedia of games.
    private static final int BOOK_BYTES = 2 * 1024 * 1024;
    private HttpServer server;
    private String url;

    @BeforeEach
    void start() throws IOException {
        byte[] book = new byte[BOOK_BYTES];
        Arrays.fill(book, (byte) 'a');
        server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/book.txt", exchange -> {
            exchange.getResponseHeaders().add("Content-Type", "text/plain; charset=utf-8");
            exchange.sendResponseHeaders(200, book.length);
            try (OutputStream out = exchange.getResponseBody()) {
                out.write(book);
            }
        });
        server.start();
        url = "http://127.0.0.1:" + server.getAddress().getPort() + "/book.txt";
    }

    @AfterEach
    void stop() {
        server.stop(0);
    }

    @Test
    void fetchesADocumentLargerThanWebClientsDefaultBuffer() {
        String body = Downloads.client(WebClient.builder()).get().uri(url).retrieve().bodyToMono(String.class).block();
        assertEquals(BOOK_BYTES, body.getBytes(StandardCharsets.UTF_8).length);
    }

    @Test
    void aDefaultClientFailsOnTheSameDocument() {
        // Documents the limit the helper exists for.
        WebClient plain = WebClient.builder().build();
        assertThrows(WebClientResponseException.class,
            () -> plain.get().uri(url).retrieve().bodyToMono(String.class).block());
    }

    @Test
    void leavesTheGivenBuilderUnchanged() {
        WebClient.Builder builder = WebClient.builder();
        Downloads.client(builder);
        WebClient plain = builder.build();
        assertThrows(WebClientResponseException.class,
            () -> plain.get().uri(url).retrieve().bodyToMono(String.class).block());
    }
}
