package kubemoot.ai.mcpgateway.config;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;

import java.io.IOException;
import java.net.ServerSocket;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;

import static org.junit.jupiter.api.Assertions.assertEquals;

/** The gateway answers the tool port and the admin port with the split the filter enforces. */
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
class AdminPortIntegrationTest {

    private static final String TOKEN = "integration-token";
    private static final int ADMIN_PORT = freePort();
    private static final String BODY = "{\"name\":\"x\",\"url\":\"http://127.0.0.1:1\",\"transport\":\"sse\"}";

    @Value("${local.server.port}")
    private int toolPort;

    @DynamicPropertySource
    static void admin(DynamicPropertyRegistry registry) {
        registry.add("mcp.gateway.admin.port", () -> ADMIN_PORT);
        registry.add("mcp.gateway.admin.token", () -> TOKEN);
    }

    private static int freePort() {
        try (ServerSocket socket = new ServerSocket(0)) {
            return socket.getLocalPort();
        } catch (IOException e) {
            throw new IllegalStateException(e);
        }
    }

    private int call(String method, int port, String path, String token, String body) throws Exception {
        HttpRequest.Builder builder = HttpRequest.newBuilder(URI.create("http://127.0.0.1:" + port + path));
        builder.method(method, body == null ? HttpRequest.BodyPublishers.noBody() : HttpRequest.BodyPublishers.ofString(body));
        if (body != null) {
            builder.header("Content-Type", "application/json");
        }
        if (token != null) {
            builder.header("Authorization", "Bearer " + token);
        }
        return HttpClient.newHttpClient().send(builder.build(), HttpResponse.BodyHandlers.discarding()).statusCode();
    }

    @Test
    void registrationWithoutTokenIs401() throws Exception {
        assertEquals(401, call("POST", ADMIN_PORT, "/admin/servers", null, BODY));
    }

    @Test
    void registrationWithWrongTokenIs401() throws Exception {
        assertEquals(401, call("POST", ADMIN_PORT, "/admin/servers", "wrong", BODY));
    }

    @Test
    void adminPortWithRightTokenServesAdminRoutes() throws Exception {
        assertEquals(200, call("GET", ADMIN_PORT, "/admin/servers", TOKEN, null));
        assertEquals(200, call("GET", ADMIN_PORT, "/admin/feedback", TOKEN, null));
    }

    @Test
    void adminPortServesNothingElse() throws Exception {
        assertEquals(404, call("GET", ADMIN_PORT, "/actuator/health", TOKEN, null));
    }

    @Test
    void toolListOnToolPortNeedsNoToken() throws Exception {
        assertEquals(200, call("GET", toolPort, "/admin/tools", null, null));
    }

    @Test
    void mutatingAdminRoutesAreNotServedOnToolPort() throws Exception {
        assertEquals(404, call("POST", toolPort, "/admin/servers", TOKEN, BODY));
        assertEquals(404, call("DELETE", toolPort, "/admin/servers/x", TOKEN, null));
        assertEquals(404, call("GET", toolPort, "/admin/servers", TOKEN, null));
        assertEquals(404, call("GET", toolPort, "/admin/feedback", TOKEN, null));
    }
}
