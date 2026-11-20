package kubemoot.ai.mcpgateway.logging;

import com.fasterxml.jackson.core.JsonProcessingException;
import org.slf4j.Logger;

/**
 * The one switch for content in logs. Logs carry ids, sizes, timings and outcomes. They do not
 * carry what people asked, what the crew answered, prompt text, tool arguments or tool results.
 *
 * <p>An installer who needs that text to debug turns it on with the environment variable
 * {@value #ENV} set to {@code true} (the system property {@value #PROPERTY} does the same) and
 * the logger to DEBUG. Content then appears in lines written through {@link #debug}, and only
 * there. With the switch off those lines are not written at all.</p>
 *
 * <p>The agent-runtime and the mcp-gateway each carry this class, apart from its package line: the
 * two services are built apart and share no library, and both read the same variable. A test in
 * each service keeps the two copies equal.</p>
 */
public final class LogContent {

    /** The environment variable that turns content in logs on. */
    public static final String ENV = "KUBEMOOT_LOG_CONTENT";
    /** The system property that does the same, for tests and for Quarkus config. */
    public static final String PROPERTY = "kubemoot.log.content";

    private LogContent() {
    }

    /** Whether the installer has turned content in logs on. Off unless set to {@code true}. */
    public static boolean enabled() {
        String value = System.getProperty(PROPERTY);
        if (value == null) {
            value = System.getenv(ENV);
        }
        return Boolean.parseBoolean(value == null ? "" : value.trim());
    }

    /** Writes a line that carries content, at DEBUG, and only when the switch is on. */
    public static void debug(Logger log, String format, Object... args) {
        if (enabled() && log.isDebugEnabled()) {
            log.debug(format, args);
        }
    }

    /**
     * Why a call failed, safe for a line that must not carry content. A JSON parse failure's own
     * message quotes the text it could not parse, so its original message, without the quoted
     * source, stands in for it.
     */
    public static String reason(Throwable failure) {
        if (failure instanceof JsonProcessingException json) {
            return json.getOriginalMessage();
        }
        return failure.getMessage();
    }

    /** How big a text is, for a line that must not carry the text: {@code 42 chars}. */
    public static String size(CharSequence text) {
        return (text == null ? 0 : text.length()) + " chars";
    }
}
