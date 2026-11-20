package kubemoot.ai.mcpgateway.logging;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.slf4j.Logger;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

class LogContentTest {

    private static final String SECRET = "sk-test0123456789abcdefABCDEF";

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
    void aContentLineIsWrittenOnlyWithTheSwitchOn() {
        Logger log = mock(Logger.class);
        when(log.isDebugEnabled()).thenReturn(true);

        LogContent.debug(log, "text: {}", SECRET);
        verify(log, never()).debug("text: {}", new Object[] {SECRET});

        System.setProperty(LogContent.PROPERTY, "true");
        LogContent.debug(log, "text: {}", SECRET);
        verify(log).debug("text: {}", new Object[] {SECRET});
    }

    @Test
    void sizeNamesTheLengthOnly() {
        assertEquals("11 chars", LogContent.size("secret text"));
        assertEquals("0 chars", LogContent.size(null));
    }

    @Test
    void aJsonFailureIsReportedWithoutTheTextItCouldNotParse() {
        JsonProcessingException failure = null;
        try {
            new ObjectMapper().readTree("{" + SECRET + "}");
        } catch (JsonProcessingException e) {
            failure = e;
        }
        assertFalse(LogContent.reason(failure).contains(SECRET), LogContent.reason(failure));
        assertEquals("boom", LogContent.reason(new IllegalStateException("boom")));
    }

    /** The agent-runtime carries this class too; the two copies differ only in their package line. */
    @Test
    void theCopyInTheAgentRuntimeIsTheSameClass() throws IOException {
        Path other = Path.of("..", "agent-runtime", "src", "main", "java", "ai", "kubemoot", "agent", "logging",
                "LogContent.java");
        Path mine = Path.of("src", "main", "java", "kubemoot", "ai", "mcpgateway", "logging", "LogContent.java");
        assertEquals(withoutPackage(other), withoutPackage(mine));
    }

    private static String withoutPackage(Path file) throws IOException {
        return Files.readString(file).replaceFirst("(?m)^package .*;$", "");
    }
}
