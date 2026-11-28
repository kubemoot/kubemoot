package ai.kubemoot.agent.mcp;

import java.util.Map;
import java.util.Optional;

/**
 * The discussion thread a turn runs in, for the tools that read the thread's stored tool results.
 * Agents see and write keys relative to the thread, {@code <agent>/<nnn>-<tool>}; the tools resolve
 * them inside the thread. The chat service binds the thread's key prefix
 * ({@code <namespace>/<crew>/<thread>/}) around a turn's tool loop, and the tool client reads it
 * when it calls a tool, as it does for {@link ai.kubemoot.agent.requester.RequesterScope}.
 *
 * <p>the workspace tools ({@link WorkspaceTools}, under either name) get the prefix in the call's MCP
 * {@code _meta} and resolve the key themselves (the workspace-access server). {@code execute_code} gets
 * it too: the code sandbox shows the program only this thread's folder at {@code /kubemoot/workspace}
 * (so {@code /kubemoot/workspace/<agent>/<nnn>-<tool>} is the file) and resolves the keys the call
 * names in {@code inputs} inside the thread.
 */
public final class WorkspaceScope {

    /** The {@code _meta} entry that carries the thread's key prefix; the MCP gateway and workspace-access read it. */
    public static final String META_KEY = "workspacePrefix";


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
        return WorkspaceTools.isWorkspace(toolName) || WorkspaceTools.CODE_TOOL.equals(baseName(toolName));
    }

    /** The {@code _meta} entries that name the thread for {@code toolName}: empty for any other tool or outside a thread. */
    static Map<String, Object> metaFor(String toolName) {
        String prefix = CURRENT.get();
        if (prefix == null || prefix.isBlank() || !readsStoredResults(toolName)) {
            return Map.of();
        }
        return Map.of(META_KEY, prefix);
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
