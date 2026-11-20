package ai.kubemoot.agent.logging;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;

class LogContentTest {

    private static final Logger LOG = LoggerFactory.getLogger(LogContentTest.class);
    static final String QUESTION = "why does the invoice for ACME-7731 fail";
    static final String SECRET = "sk-test0123456789abcdefABCDEF";

    @AfterEach
    void switchOff() {
        System.clearProperty(LogContent.PROPERTY);
    }

    @Test
    void theSwitchIsOffUnlessSetToTrue() {
        for (String value : new String[] {"false", "no", "1", "t", "", "garbage"}) {
            System.setProperty(LogContent.PROPERTY, value);
            assertFalse(LogContent.enabled(), "value '" + value + "'");
        }
        System.setProperty(LogContent.PROPERTY, " TRUE ");
        assertTrue(LogContent.enabled());
    }

    @Test
    void aContentLineIsNotWrittenWithTheSwitchOff() {
        try (var capture = new LogCapture()) {
            LogContent.debug(LOG, "question: {} key: {}", QUESTION, SECRET);
            assertEquals("", capture.text());
        }
    }

    @Test
    void aContentLineIsWrittenAtDebugWithTheSwitchOn() {
        System.setProperty(LogContent.PROPERTY, "true");
        try (var capture = new LogCapture()) {
            LogContent.debug(LOG, "question: {}", QUESTION);
            assertTrue(capture.text().contains(QUESTION), capture.text());
        }
    }

    @Test
    void sizeNamesTheLengthOnly() {
        assertEquals("11 chars", LogContent.size("secret text"));
        assertEquals("0 chars", LogContent.size(null));
    }

    @Test
    void aJsonFailureIsReportedWithoutTheTextItCouldNotParse() {
        var failure = parseFailure("{" + SECRET + "}");
        assertTrue(failure.getMessage().contains(SECRET) || failure.getMessage().contains("Source"),
                "the premise: Jackson's own message quotes the source: " + failure.getMessage());
        assertFalse(LogContent.reason(failure).contains(SECRET), LogContent.reason(failure));
        assertEquals("boom", LogContent.reason(new IllegalStateException("boom")));
    }

    /** The mcp-gateway carries this class too; the two copies differ only in their package line. */
    @Test
    void theCopyInTheMcpGatewayIsTheSameClass() throws IOException {
        Path other = Path.of("..", "mcp-gateway", "src", "main", "java", "kubemoot", "ai", "mcpgateway", "logging",
                "LogContent.java");
        Path mine = Path.of("src", "main", "java", "ai", "kubemoot", "agent", "logging", "LogContent.java");
        assertEquals(withoutPackage(other), withoutPackage(mine));
    }

    private static String withoutPackage(Path file) throws IOException {
        return Files.readString(file).replaceFirst("(?m)^package .*;$", "");
    }

    private static JsonProcessingException parseFailure(String text) {
        try {
            new ObjectMapper().readTree(text);
            throw new AssertionError("should not parse");
        } catch (JsonProcessingException e) {
            return e;
        }
    }
}
