package ai.kubemoot.agent.mcp;

import java.util.Map;
import java.util.Set;

/**
 * The one registry of the discussion workspace's tool names. The workspace server serves each read
 * tool under its current name and, for one release, under the name it had before the artifacts became
 * the workspace; {@link #canonical} maps an old name to the current one. Every place that must know
 * whether a tool reads the workspace (the thread scope sent to it, the compute contract's read check,
 * the oversize-result hint) asks here, and the prompt texts take their tool names from here, so the
 * names cannot drift apart.
 */
public final class WorkspaceTools {

    public static final String LIST = "workspace_list";
    public static final String DESCRIBE = "workspace_describe";
    public static final String QUERY = "workspace_query";
    public static final String HEAD = "workspace_head";
    public static final String TAIL = "workspace_tail";
    public static final String GREP = "workspace_grep";

    /** The tools that read stored data (not the catalog tools): the ones a compute agent may satisfy its read with. */
    private static final Set<String> DATA_TOOLS = Set.of(QUERY, HEAD, TAIL, GREP);
    /** Every workspace tool by its current name. */
    private static final Set<String> ALL = Set.of(LIST, DESCRIBE, QUERY, HEAD, TAIL, GREP);
    /** Each old tool name and the tool that replaces it. */
    private static final Map<String, String> LEGACY = Map.of(
            "artifact_head", HEAD, "artifact_tail", TAIL, "artifact_grep", GREP,
            "artifact_count", QUERY, "artifact_rows", QUERY, "artifact_select", QUERY, "artifact_jq", QUERY);

    /** How the prompts name the tools that read a stored result: the same words wherever they are listed. */
    public static final String READ_TOOLS_TEXT = QUERY + " (SQL), " + HEAD + ", " + TAIL + " or " + GREP;

    private WorkspaceTools() {
    }

    /** The current name of a workspace tool, given its current or its old name; any other name unchanged. */
    public static String canonical(String name) {
        return LEGACY.getOrDefault(name, name);
    }

    /** True for a workspace tool under either name. A server prefix ({@code server.tool}) is ignored. */
    public static boolean isWorkspace(String name) {
        return name != null && ALL.contains(canonical(baseName(name)));
    }

    /** True for a workspace tool that reads stored data, under either name. */
    public static boolean readsData(String name) {
        return name != null && DATA_TOOLS.contains(canonical(baseName(name)));
    }

    private static String baseName(String name) {
        int dot = name.lastIndexOf('.');
        return dot >= 0 ? name.substring(dot + 1) : name;
    }
}
