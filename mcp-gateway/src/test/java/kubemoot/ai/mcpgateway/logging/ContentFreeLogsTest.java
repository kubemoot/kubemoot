package kubemoot.ai.mcpgateway.logging;

import ch.qos.logback.classic.Level;
import ch.qos.logback.classic.Logger;
import ch.qos.logback.classic.spi.ILoggingEvent;
import ch.qos.logback.core.read.ListAppender;
import com.sun.net.httpserver.HttpServer;
import kubemoot.ai.mcpgateway.config.McpGatewayProperties;
import kubemoot.ai.mcpgateway.controller.AdminController;
import kubemoot.ai.mcpgateway.service.ToolSearchService;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.stream.Collectors;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;

/**
 * A tool search carries a person's question to the tool index. With the switch off the gateway's
 * logs hold the size of the question and the outcome, never the question, the index's error body
 * or a secret; with the switch on the question appears.
 */
class ContentFreeLogsTest {

    private static final String QUESTION = "why does the invoice for ACME-7731 fail";
    private static final String SECRET = "sk-test0123456789abcdefABCDEF";

    private HttpServer index;

    @AfterEach
    void cleanUp() {
        System.clearProperty(LogContent.PROPERTY);
        if (index != null) {
            index.stop(0);
        }
    }

    private String search() throws IOException {
        index = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        index.createContext("/", exchange -> {
            byte[] body = ("failed for " + QUESTION + " " + SECRET).getBytes(StandardCharsets.UTF_8);
            exchange.sendResponseHeaders(500, body.length);
            exchange.getResponseBody().write(body);
            exchange.close();
        });
        index.start();
        var properties = new McpGatewayProperties();
        properties.getToolIndex().setEnabled(true);
        properties.getToolIndex().setQueryServiceUrl("http://127.0.0.1:" + index.getAddress().getPort());
        var controller = new AdminController(mock(kubemoot.ai.mcpgateway.service.McpClientManager.class),
                new ToolSearchService(properties));

        // The gateway's own loggers at every level; the libraries' wire logging stays as configured.
        var gateway = (Logger) LoggerFactory.getLogger("kubemoot.ai.mcpgateway");
        var appender = new ListAppender<ILoggingEvent>();
        appender.start();
        Level before = gateway.getLevel();
        gateway.setLevel(Level.ALL);
        gateway.addAppender(appender);
        try {
            controller.searchTools(QUESTION + " " + SECRET, 5);
        } finally {
            gateway.detachAppender(appender);
            gateway.setLevel(before);
        }
        return appender.list.stream().map(ILoggingEvent::getFormattedMessage).collect(Collectors.joining("\n"));
    }

    @Test
    void aToolSearchLogsTheSizeOfTheQuestionAndNotItsText() throws IOException {
        String logs = search();

        assertTrue(logs.contains("Searching tools"), logs);
        assertFalse(logs.contains(QUESTION), logs);
        assertFalse(logs.contains(SECRET), logs);
    }

    @Test
    void theQuestionIsLoggedWithTheSwitchOn() throws IOException {
        System.setProperty(LogContent.PROPERTY, "true");

        assertTrue(search().contains(QUESTION));
    }

    @Test
    void theSwitchIsOffUnlessSetToTrue() {
        assertFalse(LogContent.enabled());
        System.setProperty(LogContent.PROPERTY, " TRUE ");
        assertTrue(LogContent.enabled());
        System.setProperty(LogContent.PROPERTY, "garbage");
        assertFalse(LogContent.enabled());
    }
}
