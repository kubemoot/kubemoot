package ai.kubemoot.agent.logging;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.gateway.GatewayClient;
import ai.kubemoot.agent.rag.RagClient;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * The clients that carry a person's question or a tool's output out of the agent: with the switch
 * off their logs hold ids, status and sizes, never the question, the tool arguments, the body of a
 * failed answer or a secret; with the switch on the text appears.
 */
class ContentFreeLogsTest {

    private static final String QUESTION = LogContentTest.QUESTION;
    private static final String SECRET = LogContentTest.SECRET;
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private HttpServer server;

    @AfterEach
    void cleanUp() {
        System.clearProperty(LogContent.PROPERTY);
        if (server != null) {
            server.stop(0);
        }
    }

    /** A server that answers every request with the status and a body quoting the question and the secret. */
    private String serve(int status, String body) throws IOException {
        server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> {
            byte[] bytes = body.getBytes(StandardCharsets.UTF_8);
            exchange.sendResponseHeaders(status, bytes.length);
            exchange.getResponseBody().write(bytes);
            exchange.close();
        });
        server.start();
        return "http://127.0.0.1:" + server.getAddress().getPort();
    }

    private static String quoting() {
        return "error for " + QUESTION + " with " + SECRET;
    }

    private static GatewayClient gatewayAt(String endpoint) {
        var props = mock(AgentProperties.class);
        var gateway = mock(AgentProperties.Gateway.class);
        when(props.gateway()).thenReturn(gateway);
        when(gateway.endpoint()).thenReturn(Optional.of(endpoint));
        return new GatewayClient(props, MAPPER);
    }

    private String gatewayToolCall(int status, String body) throws IOException {
        var client = gatewayAt(serve(status, body));
        try (var capture = new LogCapture()) {
            client.callTool("search", Map.of("q", QUESTION + " " + SECRET), Map.of());
            return capture.text();
        }
    }

    @Test
    void aFailedToolCallLogsItsStatusAndNotTheArgumentsOrTheBody() throws IOException {
        String logs = gatewayToolCall(500, quoting());

        assertTrue(logs.contains("Tool call search failed"), logs);
        assertFalse(logs.contains(QUESTION), logs);
        assertFalse(logs.contains(SECRET), logs);
    }

    @Test
    void aToolAnswerThatIsNotJsonLogsNoQuotedSource() throws IOException {
        String logs = gatewayToolCall(200, "{" + SECRET + " " + QUESTION);

        assertTrue(logs.contains("Tool call search failed"), logs);
        assertFalse(logs.contains(QUESTION), logs);
        assertFalse(logs.contains(SECRET), logs);
    }

    @Test
    void theToolArgumentsAndTheFailureBodyAreLoggedWithTheSwitchOn() throws IOException {
        System.setProperty(LogContent.PROPERTY, "true");

        String logs = gatewayToolCall(500, quoting());

        assertTrue(logs.contains(QUESTION), logs);
        assertTrue(logs.contains(SECRET), logs);
    }

    private String ragQuery(int status, String body) throws IOException {
        var source = mock(AgentProperties.RagSource.class);
        when(source.name()).thenReturn("docs");
        when(source.endpoint()).thenReturn(serve(status, body));
        when(source.topK()).thenReturn(3);
        var props = mock(AgentProperties.class);
        when(props.ragSources()).thenReturn(Optional.of(List.of(source)));
        var client = new RagClient(props, MAPPER);
        try (var capture = new LogCapture()) {
            client.query(QUESTION + " " + SECRET);
            return capture.text();
        }
    }

    @Test
    void aFailedKnowledgeQueryLogsItsStatusAndNotTheBody() throws IOException {
        String logs = ragQuery(500, quoting());

        assertTrue(logs.contains("answered HTTP 500"), logs);
        assertFalse(logs.contains(QUESTION), logs);
        assertFalse(logs.contains(SECRET), logs);
    }

    @Test
    void theKnowledgeFailureBodyIsLoggedWithTheSwitchOn() throws IOException {
        System.setProperty(LogContent.PROPERTY, "true");

        assertTrue(ragQuery(500, quoting()).contains(SECRET));
    }

    private String resumeSearch(int status, String body) throws IOException {
        var props = mock(AgentProperties.class);
        var search = mock(AgentProperties.ResumeSearch.class);
        when(props.resumeSearch()).thenReturn(search);
        when(search.endpoint()).thenReturn(Optional.of(serve(status, body)));
        var client = new ResumeSearchClient(props, MAPPER);
        try (var capture = new LogCapture()) {
            client.searchResumes(QUESTION + " " + SECRET, 5);
            return capture.text();
        }
    }

    @Test
    void aResumeSearchLogsTheQuestionsSizeAndNotItsText() throws IOException {
        String logs = resumeSearch(500, quoting());

        assertTrue(logs.contains("Resume search: POST"), logs);
        assertFalse(logs.contains(QUESTION), logs);
        assertFalse(logs.contains(SECRET), logs);
    }

    @Test
    void theResumeSearchQueryIsLoggedWithTheSwitchOn() throws IOException {
        System.setProperty(LogContent.PROPERTY, "true");

        assertTrue(resumeSearch(500, quoting()).contains(QUESTION));
    }
}
