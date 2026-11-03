package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallHeaders;
import ai.kubemoot.agent.chat.CallManifest;
import io.nats.client.Connection;
import io.nats.client.api.ObjectInfo;
import io.nats.client.impl.Headers;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * Every tool call stored for one thread, built in code from the headers of the per-call workspace
 * objects (tool, arguments, rows, bytes, format, error). It is the index analysts read by: nothing
 * stored is missing from it, and a header line carries the totals so a missing line shows.
 *
 * <p>{@link #render} gives it one line per call grouped per (agent, tool), and one level coarser
 * (group lines with counts only) when that does not fit the room it is given. It is never cut: a
 * group line is never dropped.
 */
final class ThreadCatalog {

    private static final Logger log = LoggerFactory.getLogger(ThreadCatalog.class);

    /** Opens the totals line. */
    static final String HEADER_PREFIX = "[CATALOG ";
    /** Opens a group line. */
    static final String GROUP_PREFIX = "[GROUP ";
    /** Opens a call line. Not {@code [CALL key=}, so a catalog is never read as a tooler's manifest. */
    static final String ENTRY_PREFIX = "[CATALOG-CALL key=";

    /** One stored call; {@code rows} is -1 when unknown. */
    record Entry(String key, String agent, String tool, String args, long rows, long bytes, String format,
                 boolean error) {

        /** The key pattern that reads every call of this entry's agent and tool as one table. */
        String pattern() {
            return key.substring(0, key.lastIndexOf('/') + 1) + "*-" + DiscussionArtifacts.safe(tool);
        }

        String groupId() {
            return agent + "\u0000" + tool;
        }
    }

    private static final int UNKNOWN_WINDOW_CHARS = 8_000;
    private static final int CHARS_PER_TOKEN = 3;

    private final String prefix;
    private final List<Entry> entries;

    ThreadCatalog(String prefix, List<Entry> entries) {
        this.prefix = prefix;
        this.entries = entries.stream().sorted(Comparator.comparing(Entry::key)).toList();
    }

    /**
     * The catalog of the thread under {@code prefix}, from the headers of the bucket's objects; empty
     * when the bucket cannot be listed.
     */
    static ThreadCatalog load(Connection conn, String prefix) {
        var found = new ArrayList<Entry>();
        try {
            for (ObjectInfo info : conn.objectStore(DiscussionArtifacts.BUCKET).getList()) {
                if (!info.isDeleted() && info.getObjectName().startsWith(prefix)) {
                    entryOf(info).ifPresent(found::add);
                }
            }
        } catch (Exception e) {
            log.warn("catalog: could not list the stored calls under {} ({}): {}", prefix,
                    e.getClass().getSimpleName(), e.getMessage());
        }
        return new ThreadCatalog(prefix, found);
    }

    private static java.util.Optional<Entry> entryOf(ObjectInfo info) {
        Headers h = info.getHeaders();
        if (h == null || !CallHeaders.OPERATION_EXECUTE_TOOL.equals(h.getFirst(CallHeaders.OPERATION_NAME))) {
            return java.util.Optional.empty();
        }
        String key = info.getObjectName();
        String agent = text(h, CallHeaders.AGENT_NAME);
        if (agent.isEmpty()) {
            String[] parts = key.split("/");
            agent = parts.length >= 2 ? parts[parts.length - 2] : "";
        }
        return java.util.Optional.of(new Entry(key, agent, text(h, CallHeaders.TOOL_NAME),
                text(h, CallHeaders.TOOL_CALL_ARGUMENTS), number(h, CallHeaders.ROWS, -1),
                number(h, CallHeaders.BYTES, info.getSize()), text(h, CallHeaders.FORMAT),
                Boolean.parseBoolean(text(h, CallHeaders.ERROR))));
    }

    private static String text(Headers h, String name) {
        String v = h.getFirst(name);
        return v == null ? "" : v;
    }

    private static long number(Headers h, String name, long fallback) {
        try {
            return Long.parseLong(text(h, name));
        } catch (NumberFormatException e) {
            return fallback;
        }
    }

    /** Characters of a quarter of the smallest window (about three characters a token); a fixed size when no window is known. */
    static int roomChars(java.util.OptionalLong windowTokens) {
        return windowTokens.isEmpty() ? UNKNOWN_WINDOW_CHARS
                : (int) Math.min(Integer.MAX_VALUE, windowTokens.getAsLong() / 4 * CHARS_PER_TOKEN);
    }

    List<Entry> entries() {
        return entries;
    }

    boolean isEmpty() {
        return entries.isEmpty();
    }

    int calls() {
        return entries.size();
    }

    long errors() {
        return entries.stream().filter(Entry::error).count();
    }

    long bytes() {
        return entries.stream().mapToLong(Entry::bytes).sum();
    }

    /** The entries grouped per (agent, tool), in key order. */
    Map<String, List<Entry>> groups() {
        var groups = new LinkedHashMap<String, List<Entry>>();
        entries.forEach(e -> groups.computeIfAbsent(e.groupId(), g -> new ArrayList<>()).add(e));
        return groups;
    }

    /**
     * The catalog as text: totals, then per group its line and (when {@code maxChars} allows) its
     * call lines. When the call lines do not fit, only the group lines are given and the legend says
     * how to list a group's calls.
     */
    String render(int maxChars) {
        String full = text(true);
        return full.length() <= maxChars ? full : text(false);
    }

    private String text(boolean withCalls) {
        var sb = new StringBuilder(HEADER_PREFIX).append("calls=").append(calls()).append(" errors=")
                .append(errors()).append(" bytes=").append(bytes()).append(" groups=").append(groups().size())
                .append(" prefix=").append(prefix).append("]\n");
        groups().values().forEach(g -> {
            sb.append(groupLine(g)).append('\n');
            if (withCalls) {
                g.forEach(e -> sb.append(entryLine(e)).append('\n'));
            }
        });
        return sb.append(legend(withCalls)).toString();
    }

    private static String groupLine(List<Entry> g) {
        Entry first = g.get(0);
        long errors = g.stream().filter(Entry::error).count();
        boolean rowsKnown = g.stream().allMatch(e -> e.rows() >= 0);
        long rows = g.stream().filter(e -> e.rows() >= 0).mapToLong(Entry::rows).sum();
        return GROUP_PREFIX + "agent=" + first.agent() + " tool=" + first.tool() + " calls=" + g.size()
                + " errors=" + errors + " bytes=" + g.stream().mapToLong(Entry::bytes).sum()
                + " rows=" + (rowsKnown ? Long.toString(rows) : "unknown") + " pattern=" + first.pattern() + "]";
    }

    private static String entryLine(Entry e) {
        return ENTRY_PREFIX + e.key() + " tool=" + e.tool() + " args=" + CallManifest.argsSummary(e.args())
                + " format=" + (e.format().isEmpty() ? "text" : e.format())
                + " rows=" + (e.rows() >= 0 ? Long.toString(e.rows()) : "unknown")
                + " bytes=" + e.bytes() + " error=" + e.error() + "]";
    }

    private static String legend(boolean withCalls) {
        String how = "Read one call by its key, or every call of a group by its pattern, with workspace_query (SQL) "
                + "or the artifact_* tools, or with execute_code over the file /artifacts/<key>. A pattern reads the "
                + "calls as one table with the columns _call (the call's key), _args (its arguments) and "
                + "_arg_<name> for an argument that differs between the calls (for example _arg_vmid).";
        return withCalls ? how : how + " Per-call lines are not shown; SELECT DISTINCT _call, _args over the pattern lists them.";
    }

    /** The totals and, per tool, the number of calls, on one line: what a synthesis is told when no analyst reads. */
    String counts() {
        var perTool = new LinkedHashMap<String, Integer>();
        entries.forEach(e -> perTool.merge(e.tool(), 1, Integer::sum));
        return HEADER_PREFIX + "calls=" + calls() + " errors=" + errors() + " bytes=" + bytes() + " tools="
                + perTool + "]";
    }
}
