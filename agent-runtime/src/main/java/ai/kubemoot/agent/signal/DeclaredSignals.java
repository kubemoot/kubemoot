package ai.kubemoot.agent.signal;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

/**
 * The kinds of signal the crew's archetype declares, as the operator hands them to each agent in
 * {@code KUBEMOOT_DISCUSS_SIGNALS}: a JSON list of {@code {name, meaning, terminal, addressed}}.
 * Empty when the archetype declares none, and the agent then records its verdict as it did before
 * the signal tool existed.
 */
public final class DeclaredSignals {

    private static final Logger log = LoggerFactory.getLogger(DeclaredSignals.class);
    private static final ObjectMapper MAPPER = new ObjectMapper();

    /** No declared kinds: the signal tool is not offered. */
    public static final DeclaredSignals NONE = new DeclaredSignals(Map.of());

    private final Map<String, DeclaredSignal> byName;

    private DeclaredSignals(Map<String, DeclaredSignal> byName) {
        this.byName = byName;
    }

    /** The kinds in {@code json}, in declaration order; {@link #NONE} when it is absent, blank or not a list. */
    public static DeclaredSignals parse(Optional<String> json) {
        return json.map(DeclaredSignals::parse).orElse(NONE);
    }

    /**
     * The kinds in {@code json}. An entry without a name or a meaning is left out (a model cannot
     * choose a kind it is not told about); a repeated name keeps its first entry. Read with
     * {@code readTree}, which works in the native image where record binding does not.
     */
    public static DeclaredSignals parse(String json) {
        if (json == null || json.isBlank()) {
            return NONE;
        }
        try {
            JsonNode root = MAPPER.readTree(json);
            if (!root.isArray()) {
                log.warn("KUBEMOOT_DISCUSS_SIGNALS is not a JSON list; the signal tool is not offered");
                return NONE;
            }
            var byName = new LinkedHashMap<String, DeclaredSignal>();
            root.forEach(node -> addEntry(byName, node));
            return byName.isEmpty() ? NONE : new DeclaredSignals(java.util.Collections.unmodifiableMap(byName));
        } catch (IOException e) {
            log.warn("KUBEMOOT_DISCUSS_SIGNALS is not JSON ({}); the signal tool is not offered", e.getMessage());
            return NONE;
        }
    }

    private static void addEntry(Map<String, DeclaredSignal> byName, JsonNode node) {
        String name = node.path("name").asText("").strip();
        String meaning = node.path("meaning").asText("").strip();
        if (name.isEmpty() || meaning.isEmpty()) {
            return;
        }
        byName.putIfAbsent(name, new DeclaredSignal(name, meaning,
                node.path("terminal").asBoolean(true), node.path("addressed").asBoolean(false)));
    }

    /** True when the archetype declares no kind. */
    public boolean isEmpty() {
        return byName.isEmpty();
    }

    /** The declared kind named {@code name}, or null. */
    public DeclaredSignal find(String name) {
        return name == null ? null : byName.get(name);
    }

    /** The declared kinds, in the order the archetype declares them. */
    public List<DeclaredSignal> all() {
        return List.copyOf(byName.values());
    }

    /** True when the kind named {@code name} is declared and ends the agent's turn. */
    public boolean isTerminal(String name) {
        var s = find(name);
        return s != null && s.terminal();
    }
}
