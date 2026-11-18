package ai.kubemoot.agent.mcp;

import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Optional;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * The discussion thread a turn runs in, for the tools that read the thread's stored tool results.
 * Agents see and write keys relative to the thread, {@code <agent>/<nnn>-<tool>}; the tools resolve
 * them inside the thread. The chat service binds the thread's key prefix
 * ({@code <namespace>/<crew>/<thread>/}) around a turn's tool loop, and the tool client reads it
 * when it calls a tool, as it does for {@link ai.kubemoot.agent.requester.RequesterScope}.
 *
 * <p>the workspace tools ({@link WorkspaceTools}, under either name) get the prefix in the call's MCP
 * {@code _meta} and resolve the key themselves (the workspace-access server). {@code execute_code}
 * runs in a sandbox that knows no thread, so the code's {@code /workspace/<agent>/<nnn>-<tool>}
 * paths are written out in full here before the call.
 */
public final class WorkspaceScope {

    /** The {@code _meta} entry that carries the thread's key prefix; the MCP gateway and workspace-access read it. */
    public static final String META_KEY = "workspacePrefix";

    private static final String EXECUTE_CODE = "execute_code";
    private static final String CODE_ARGUMENT = "code";

    /**
     * {@code /workspace/<agent>/<call file or pattern>} with an agent part and no thread part before it.
     * The old mount name, {@code /artifacts/}, is read the same way for one release.
     */
    private static final Pattern SHORT_PATH = Pattern.compile(
            "/(?:workspace|artifacts)/([A-Za-z0-9._-]+)/(\\d{3,}-[A-Za-z0-9_.-]+|\\*[A-Za-z0-9_.*-]*)(?![A-Za-z0-9_.*/-])");

    private static final ThreadLocal<String> CURRENT = new ThreadLocal<>();

    private WorkspaceScope() {
    }

    /** Binds the thread prefix (null for none) until the returned scope is closed, then restores the previous one. */
    public static Scope bind(String prefix) {
        String previous = CURRENT.get();
        set(prefix);
        return () -> set(previous);
    }

    public static Optional<String> current() {
        return Optional.ofNullable(CURRENT.get());
    }

    /** True for the tools that read stored tool results by key. */
    static boolean readsStoredResults(String toolName) {
        return WorkspaceTools.isWorkspace(toolName) || EXECUTE_CODE.equals(baseName(toolName));
    }

    /** The {@code _meta} entries that name the thread for {@code toolName}: empty for any other tool or outside a thread. */
    static Map<String, Object> metaFor(String toolName) {
        String prefix = CURRENT.get();
        if (prefix == null || prefix.isBlank() || !readsStoredResults(toolName)) {
            return Map.of();
        }
        return Map.of(META_KEY, prefix);
    }

    /**
     * The arguments to send: for {@code execute_code} inside a thread, the code with every
     * {@code /workspace/<agent>/<nnn>-<tool>} path (or the old {@code /artifacts/...}) written out under the thread's prefix; for any
     * other call, {@code arguments} unchanged.
     */
    static Map<String, Object> argumentsFor(String toolName, Map<String, Object> arguments) {
        String prefix = CURRENT.get();
        if (prefix == null || prefix.isBlank() || arguments == null) {
            return arguments;
        }
        if (!EXECUTE_CODE.equals(baseName(toolName)) || !(arguments.get(CODE_ARGUMENT) instanceof String code)) {
            return arguments;
        }
        String expanded = expandPaths(code, prefix);
        if (expanded.equals(code)) {
            return arguments;
        }
        var out = new LinkedHashMap<>(arguments);
        out.put(CODE_ARGUMENT, expanded);
        return out;
    }

    /** {@code code} with each short {@code /workspace/} or old {@code /artifacts/} path under {@code prefix}, written with {@code /workspace/}; a path that already names a thread is left alone. */
    static String expandPaths(String code, String prefix) {
        Matcher m = SHORT_PATH.matcher(code);
        return m.replaceAll(r -> "/workspace/" + Matcher.quoteReplacement(prefix)
                + Matcher.quoteReplacement(r.group(1) + "/" + r.group(2)));
    }

    private static String baseName(String toolName) {
        int dot = toolName.indexOf('.');
        return dot > 0 ? toolName.substring(dot + 1) : toolName;
    }

    private static void set(String prefix) {
        if (prefix == null) {
            CURRENT.remove();
        } else {
            CURRENT.set(prefix);
        }
    }

    /** Closing it ends the binding; it throws nothing. */
    @FunctionalInterface
    public interface Scope extends AutoCloseable {
        @Override
        void close();
    }
}
