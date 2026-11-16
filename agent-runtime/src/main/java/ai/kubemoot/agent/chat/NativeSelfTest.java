package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.ObjectMapper;
import io.quarkus.runtime.Quarkus;
import io.quarkus.runtime.StartupEvent;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.enterprise.event.Observes;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import org.eclipse.microprofile.config.inject.ConfigProperty;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * Runs the code paths that native compilation can break, inside the running binary, when
 * {@code kubemoot.native-selftest} (env {@code KUBEMOOT_NATIVE_SELFTEST}) is true: result type
 * detection (JSON through Jackson, YAML through SnakeYAML, the table and CSV sniffers), reading
 * JSON with {@code readTree}, and binding a record that the reflection configuration lists. The
 * CI native check starts the binary with it set and fails unless the log holds {@link #OK_MARKER};
 * on a failed check the process logs {@link #FAILED_MARKER} and exits with status 1. Left unset,
 * it does nothing.
 */
@ApplicationScoped
public class NativeSelfTest {

    static final String OK_MARKER = "NATIVE_SELFTEST_OK";
    static final String FAILED_MARKER = "NATIVE_SELFTEST_FAILED";

    private static final Logger log = LoggerFactory.getLogger(NativeSelfTest.class);
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final boolean enabled;

    public NativeSelfTest(@ConfigProperty(name = "kubemoot.native-selftest", defaultValue = "false") boolean enabled) {
        this.enabled = enabled;
    }

    void onStart(@Observes StartupEvent event) {
        if (!enabled) {
            return;
        }
        List<String> failures = run();
        if (failures.isEmpty()) {
            log.info("{} checks={}", OK_MARKER, checks().size());
            return;
        }
        failures.forEach(f -> log.error("{} {}", FAILED_MARKER, f));
        Quarkus.asyncExit(1);
    }

    /** Runs every check and returns one line per failure; empty when all passed. */
    List<String> run() {
        return runAll(checks());
    }

    static List<String> runAll(Map<String, Runnable> checks) {
        List<String> failures = new ArrayList<>();
        checks.forEach((name, body) -> {
            try {
                body.run();
            } catch (Throwable t) {
                failures.add(name + ": " + t);
            }
        });
        return failures;
    }

    private static Map<String, Runnable> checks() {
        Map<String, Runnable> checks = new LinkedHashMap<>();
        checks.put("json object", () -> expectType(ResultType.JSON, "{\"items\": [{\"name\": \"a-1\"}]}"));
        checks.put("json list", () -> expectType(ResultType.JSON, "[1, 2, 3]"));
        checks.put("broken json is not json", () -> expectType(ResultType.TEXT, "{\"items\": [1, 2"));
        checks.put("yaml mapping", () -> expectType(ResultType.YAML, "kind: Pod\nmetadata:\n  name: web-1\n"));
        checks.put("yaml list", () -> expectType(ResultType.YAML, "- name: a\n  ready: true\n- name: b\n  ready: false\n"));
        checks.put("yaml documents", () -> expectType(ResultType.YAML, "kind: Pod\n---\nkind: Service\n"));
        checks.put("yaml duplicate key", () -> expectType(ResultType.TEXT, "name: a\nname: b\n"));
        checks.put("kubectl table", () -> expectType(ResultType.KUBECTL_TABLE, "NAME    STATUS   AGE\nweb-1   Running  3d\n"));
        checks.put("markdown table", () -> expectType(ResultType.MARKDOWN_TABLE, "| a | b |\n|---|---|\n| 1 | 2 |\n"));
        checks.put("csv", () -> expectType(ResultType.CSV, "a,b\n1,2\n3,4\n"));
        checks.put("readTree of a tool schema", NativeSelfTest::readTreeOfToolSchema);
        checks.put("record bound from json", NativeSelfTest::bindListedRecord);
        return checks;
    }

    private static void expectType(String want, String text) {
        String got = ResultType.of(text);
        if (!want.equals(got)) {
            throw new IllegalStateException("type " + got + ", want " + want);
        }
    }

    private static void readTreeOfToolSchema() {
        try {
            var node = MAPPER.readTree("{\"type\":\"object\",\"properties\":{\"namespace\":{\"type\":\"string\"}},"
                    + "\"required\":[\"namespace\"]}");
            if (!"string".equals(node.get("properties").get("namespace").get("type").asText())
                    || !"namespace".equals(node.get("required").get(0).asText())) {
                throw new IllegalStateException("schema fields not read");
            }
        } catch (com.fasterxml.jackson.core.JsonProcessingException e) {
            throw new IllegalStateException(e);
        }
    }

    private static void bindListedRecord() {
        try {
            var request = MAPPER.readValue("{\"conversationId\":\"c-1\",\"message\":\"hi\"}", ChatController.ChatRequest.class);
            if (!"c-1".equals(request.conversationId()) || !"hi".equals(request.message())) {
                throw new IllegalStateException("record fields not bound: " + request);
            }
            var json = MAPPER.writeValueAsString(new ChatController.ToolInfo("t-1", "d"));
            if (!json.contains("\"name\":\"t-1\"")) {
                throw new IllegalStateException("record not written: " + json);
            }
        } catch (com.fasterxml.jackson.core.JsonProcessingException e) {
            throw new IllegalStateException(e);
        }
    }
}
