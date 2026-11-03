package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * The stored tool-call objects an agent's tool calls read, kept per thread until the agent
 * publishes its signal, which carries them to the coordinator (metadata {@code readKeys}). The
 * coordinator turns them into the coverage of the thread's evidence. The keys come from the
 * arguments of successful calls, read in code; the model reports nothing.
 *
 * <p>What can be traced: {@code workspace_query} (every value of its {@code artifacts} map),
 * the {@code artifact_*} tools (their {@code key}), and {@code execute_code} when its code names a
 * path under {@code /artifacts/} as text (a literal key, a directory ending in a slash, or a
 * pattern with {@code *}). A path the code builds at run time (string concatenation, a listing of
 * the directory) is not traceable and counts for nothing.
 */
public final class AnalystReads {

    /** Most keys kept for one thread; later ones are dropped. */
    static final int MAX_KEYS = 200;
    /** Most threads kept at once; the least recently used is dropped past this. */
    static final int MAX_THREADS = 200;

    static final String WORKSPACE_QUERY = "workspace_query";
    static final String EXECUTE_CODE = "execute_code";
    /** The artifact read-ops tools; each takes the object key as {@code key}. */
    public static final java.util.Set<String> ARTIFACT_READ_TOOLS = java.util.Set.of(
            "artifact_head", "artifact_tail", "artifact_grep", "artifact_count",
            "artifact_rows", "artifact_select", "artifact_jq");
    private static final String ARTIFACT_MOUNT_PATH = "/artifacts/";
    private static final Pattern MOUNT_PATH = Pattern.compile(ARTIFACT_MOUNT_PATH + "([A-Za-z0-9_./*\\-]+)");
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final Map<String, Set<String>> byThread = Collections.synchronizedMap(
            new LinkedHashMap<String, Set<String>>(16, 0.75f, true) {
                @Override
                protected boolean removeEldestEntry(Map.Entry<String, Set<String>> eldest) {
                    return size() > MAX_THREADS;
                }
            });

    /** Notes one finished tool call; only a successful call of a reading tool adds keys. */
    public void record(String threadId, String tool, String arguments, boolean success) {
        if (!success || threadId == null || threadId.isBlank()) {
            return;
        }
        List<String> keys = keysOf(tool, arguments);
        if (keys.isEmpty()) {
            return;
        }
        synchronized (byThread) {
            Set<String> held = byThread.computeIfAbsent(threadId, t -> new LinkedHashSet<>());
            for (String key : keys) {
                if (held.size() < MAX_KEYS) {
                    held.add(key);
                }
            }
        }
    }

    /** The keys read since the last take for the thread, in the order read; the thread's list is cleared. */
    public List<String> take(String threadId) {
        if (threadId == null) {
            return List.of();
        }
        synchronized (byThread) {
            Set<String> held = byThread.remove(threadId);
            return held == null ? List.of() : List.copyOf(held);
        }
    }

    /** The object keys or key patterns one call of {@code tool} with {@code arguments} reads. */
    public static List<String> keysOf(String tool, String arguments) {
        if (tool == null || arguments == null || arguments.isBlank()) {
            return List.of();
        }
        if (EXECUTE_CODE.equals(tool)) {
            return pathsIn(arguments);
        }
        JsonNode args = parse(arguments);
        if (args == null) {
            return List.of();
        }
        if (WORKSPACE_QUERY.equals(tool)) {
            return queryKeys(args.path("artifacts"));
        }
        if (ARTIFACT_READ_TOOLS.contains(tool)) {
            String key = args.path("key").asText("");
            return key.isBlank() ? List.of() : List.of(key.strip());
        }
        return List.of();
    }

    /** The values of workspace_query's artifacts map, which models sometimes send as a string holding the object. */
    private static List<String> queryKeys(JsonNode artifacts) {
        JsonNode map = artifacts.isTextual() ? parse(artifacts.asText()) : artifacts;
        var keys = new ArrayList<String>();
        if (map != null && map.isObject()) {
            map.elements().forEachRemaining(v -> {
                if (v.isTextual() && !v.asText().isBlank()) {
                    keys.add(v.asText().strip());
                }
            });
        }
        return keys;
    }

    /** The keys of every {@code /artifacts/<key>} path the code text names, trailing dots removed. */
    private static List<String> pathsIn(String code) {
        var keys = new LinkedHashSet<String>();
        Matcher m = MOUNT_PATH.matcher(code);
        while (m.find()) {
            String key = m.group(1).replaceAll("[.]+$", "");
            if (!key.isEmpty()) {
                keys.add(key);
            }
        }
        return List.copyOf(keys);
    }

    private static JsonNode parse(String json) {
        try {
            return MAPPER.readTree(json);
        } catch (Exception e) {
            return null;
        }
    }
}
