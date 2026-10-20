package ai.kubemoot.agent.gateway;

import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** The gateway client says why a tool-list read failed, since it answers a failure with an empty list. */
class GatewayClientListErrorTest {

    private HttpServer server;

    @AfterEach
    void stop() {
        if (server != null) {
            server.stop(0);
        }
    }

    private GatewayClient clientFor(String endpoint) {
        var props = mock(AgentProperties.class);
        var gateway = mock(AgentProperties.Gateway.class);
        when(props.gateway()).thenReturn(gateway);
        when(gateway.endpoint()).thenReturn(Optional.of(endpoint));
        return new GatewayClient(props, new ObjectMapper());
    }

    private String serve(int status, String body) throws IOException {
        server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/admin/tools", exchange -> {
            byte[] bytes = body.getBytes(StandardCharsets.UTF_8);
            exchange.sendResponseHeaders(status, bytes.length);
            exchange.getResponseBody().write(bytes);
            exchange.close();
        });
        server.start();
        return "http://127.0.0.1:" + server.getAddress().getPort();
    }

    @Test
    void theEndpointIsReportedAsConfigured_andEmptyWhenNone() {
        assertEquals("http://gateway:8080", clientFor("http://gateway:8080").endpoint());
        assertEquals("", clientFor("").endpoint());
    }

    @Test
    void aGoodRead_hasNoError() throws Exception {
        var client = clientFor(serve(200, "{\"count\":1,\"tools\":[{\"name\":\"t\"}]}"));
        assertEquals(1, client.listTools().size());
        assertTrue(client.lastListError().isEmpty());
    }

    @Test
    void anErrorStatus_isReportedAndTheListIsEmpty() throws Exception {
        var client = clientFor(serve(502, "{\"error\":\"bad gateway\"}"));
        assertTrue(client.listTools().isEmpty());
        assertEquals("gateway answered HTTP 502", client.lastListError().orElseThrow());
    }

    @Test
    void anUnreachableGateway_isReported() throws Exception {
        var url = serve(200, "{}");
        server.stop(0);
        var client = clientFor(url);
        assertTrue(client.listTools().isEmpty());
        assertTrue(client.lastListError().orElseThrow().startsWith("gateway unreachable"));
    }

    @Test
    void aLaterGoodRead_clearsTheError() throws Exception {
        var client = clientFor(serve(500, "{}"));
        client.listTools();
        assertTrue(client.lastListError().isPresent());
        server.removeContext("/admin/tools");
        server.createContext("/admin/tools", exchange -> {
            byte[] bytes = "{\"tools\":[]}".getBytes(StandardCharsets.UTF_8);
            exchange.sendResponseHeaders(200, bytes.length);
            exchange.getResponseBody().write(bytes);
            exchange.close();
        });
        client.listTools();
        assertTrue(client.lastListError().isEmpty());
    }
}
